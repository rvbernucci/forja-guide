// Package cli implements the forja command-line client.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

// Client calls the forjad HTTP boundary.
type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
	registry   *contracts.Registry
}

// NewClient creates a validating API client.
func NewClient(
	endpoint string,
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
	if httpClient == nil {
		// Command contexts own the deadline so FORJA_TIMEOUT remains authoritative.
		httpClient = &http.Client{}
	}
	if registry == nil {
		return nil, fmt.Errorf("contract registry is required")
	}
	return &Client{
		baseURL:    baseURL,
		httpClient: httpClient,
		registry:   registry,
	}, nil
}

// CreateRun creates a draft run.
func (c *Client) CreateRun(ctx context.Context, objective string) (contracts.Run, error) {
	return c.doRun(
		ctx,
		http.MethodPost,
		"/v1/runs",
		map[string]string{"objective": objective},
	)
}

// GetRun retrieves a run.
func (c *Client) GetRun(ctx context.Context, runID string) (contracts.Run, error) {
	return c.doRun(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(runID), nil)
}

// TransitionRun applies an optimistic state transition.
func (c *Client) TransitionRun(
	ctx context.Context,
	runID string,
	expectedVersion int,
	targetState string,
) (contracts.Run, error) {
	return c.doRun(
		ctx,
		http.MethodPost,
		"/v1/runs/"+url.PathEscape(runID)+"/transitions",
		map[string]any{
			"expected_version": expectedVersion,
			"target_state":     targetState,
		},
	)
}

func (c *Client) doRun(
	ctx context.Context,
	method string,
	path string,
	payload any,
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
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
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
