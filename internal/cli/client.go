// Package cli implements the forja command-line client.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

// CommandMetadata is the stable identity reused for one logical command.
type CommandMetadata = runstate.CommandMetadata

// Client calls the forjad HTTP boundary.
type Client struct {
	baseURL     *url.URL
	httpClient  *http.Client
	registry    *contracts.Registry
	bearerToken string
}

// NewClient creates a validating API client.
func NewClient(
	endpoint string,
	bearerToken string,
	httpClient *http.Client,
	registry *contracts.Registry,
) (*Client, error) {
	baseURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint: %w", err)
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return nil, fmt.Errorf("endpoint must use http or https")
	}
	if baseURL.Host == "" {
		return nil, fmt.Errorf("endpoint host is required")
	}
	if baseURL.Scheme == "http" && !isLoopbackHost(baseURL.Hostname()) {
		return nil, fmt.Errorf("non-loopback endpoints must use https")
	}
	if strings.TrimSpace(bearerToken) == "" {
		return nil, fmt.Errorf("FORJA_HTTP_BEARER_TOKEN is required")
	}
	if httpClient == nil {
		// Command contexts own the deadline so FORJA_TIMEOUT remains authoritative.
		httpClient = &http.Client{}
	}
	client := *httpClient
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		// API redirects are unexpected and can move the reusable bearer to a
		// different authority or downgrade HTTPS to plaintext.
		return http.ErrUseLastResponse
	}
	if registry == nil {
		return nil, fmt.Errorf("contract registry is required")
	}
	return &Client{
		baseURL:     baseURL,
		httpClient:  &client,
		registry:    registry,
		bearerToken: bearerToken,
	}, nil
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// PrepareCommand creates or validates metadata before a command is attempted.
// Callers can retain the result and reuse it after an ambiguous transport error.
func PrepareCommand(idempotencyKey string) (CommandMetadata, error) {
	commandID, err := identity.NewRunID()
	if err != nil {
		return CommandMetadata{}, fmt.Errorf("generate command identifier: %w", err)
	}
	key := strings.TrimSpace(idempotencyKey)
	if key == "" {
		key = commandID.String()
	}
	metadata := CommandMetadata{
		IdempotencyKey: key,
		ActorType:      "human",
		ActorID:        "forja",
		CorrelationID:  commandID.String(),
	}
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return CommandMetadata{}, fmt.Errorf("validate command metadata: %w", err)
	}
	return metadata, nil
}

// CreateRun creates a draft run.
func (c *Client) CreateRun(ctx context.Context, objective string) (contracts.Run, error) {
	metadata, err := PrepareCommand("")
	if err != nil {
		return contracts.Run{}, err
	}
	return c.CreateRunWithCommand(ctx, objective, metadata)
}

// CreateRunWithCommand creates a run using caller-retained command metadata.
func (c *Client) CreateRunWithCommand(
	ctx context.Context,
	objective string,
	metadata CommandMetadata,
) (contracts.Run, error) {
	return c.doRun(
		ctx,
		http.MethodPost,
		"/v1/runs",
		map[string]string{"objective": objective},
		&metadata,
	)
}

// GetRun retrieves a run.
func (c *Client) GetRun(ctx context.Context, runID string) (contracts.Run, error) {
	return c.doRun(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(runID), nil, nil)
}

// TransitionRun applies an optimistic state transition.
func (c *Client) TransitionRun(
	ctx context.Context,
	runID string,
	expectedVersion int,
	targetState string,
) (contracts.Run, error) {
	metadata, err := PrepareCommand("")
	if err != nil {
		return contracts.Run{}, err
	}
	return c.TransitionRunWithCommand(
		ctx,
		runID,
		expectedVersion,
		targetState,
		metadata,
	)
}

// TransitionRunWithCommand transitions a run with reusable command metadata.
func (c *Client) TransitionRunWithCommand(
	ctx context.Context,
	runID string,
	expectedVersion int,
	targetState string,
	metadata CommandMetadata,
) (contracts.Run, error) {
	return c.doRun(
		ctx,
		http.MethodPost,
		"/v1/runs/"+url.PathEscape(runID)+"/transitions",
		map[string]any{
			"expected_version": expectedVersion,
			"target_state":     targetState,
		},
		&metadata,
	)
}

func (c *Client) doRun(
	ctx context.Context,
	method string,
	path string,
	payload any,
	metadata *CommandMetadata,
) (contracts.Run, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return contracts.Run{}, fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	endpoint := c.baseURL.ResolveReference(&url.URL{Path: path})
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return contracts.Run{}, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.bearerToken)
	if payload != nil {
		if metadata == nil {
			return contracts.Run{}, fmt.Errorf("command metadata is required")
		}
		if err := runstate.ValidateCommandMetadata(*metadata); err != nil {
			return contracts.Run{}, fmt.Errorf("validate command metadata: %w", err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", metadata.IdempotencyKey)
		request.Header.Set("Forja-Correlation-ID", metadata.CorrelationID)
		if metadata.CausationID != nil {
			request.Header.Set("Forja-Causation-ID", *metadata.CausationID)
		}
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return contracts.Run{}, fmt.Errorf("call daemon: %w", err)
	}
	defer response.Body.Close()

	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return contracts.Run{}, fmt.Errorf("read daemon response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return contracts.Run{}, fmt.Errorf(
			"daemon returned %s: %s",
			response.Status,
			strings.TrimSpace(string(data)),
		)
	}
	run, err := contracts.DecodeStrict[contracts.Run](
		c.registry,
		"run.schema.json",
		data,
	)
	if err != nil {
		return contracts.Run{}, fmt.Errorf("validate daemon response: %w", err)
	}
	return run, nil
}
