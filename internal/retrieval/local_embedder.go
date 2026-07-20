package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

const maxLocalEmbeddingResponseBytes = 4 << 20

// LocalHTTPEmbeddingConfig pins a local OpenAI-compatible embedding endpoint.
// It is intended for Radeon/ROCm deployments where vLLM or SGLang serves the
// model on loopback; remote hosts are rejected during construction.
type LocalHTTPEmbeddingConfig struct {
	Endpoint             string
	Model                string
	Version              string
	Dimensions           int
	SparseEncoderVersion string
	HTTPClient           *http.Client
	Now                  func() time.Time
}

// LocalHTTPEmbedder calls only a configured loopback embeddings endpoint.
type LocalHTTPEmbedder struct {
	endpoint   string
	client     *http.Client
	descriptor contracts.EmbeddingDescriptor
}

func NewLocalHTTPEmbedder(config LocalHTTPEmbeddingConfig) (*LocalHTTPEmbedder, error) {
	endpoint, err := validateLocalEmbeddingConfig(config)
	if err != nil {
		return nil, err
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &LocalHTTPEmbedder{
		endpoint: endpoint,
		client:   client,
		descriptor: contracts.EmbeddingDescriptor{
			Model: config.Model, Version: config.Version, Dimensions: config.Dimensions,
			SparseEncoderVersion: config.SparseEncoderVersion, EmbeddedAt: now().UTC(),
		},
	}, nil
}

func (e *LocalHTTPEmbedder) Descriptor() contracts.EmbeddingDescriptor {
	return e.descriptor
}

func (e *LocalHTTPEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	if e == nil || e.client == nil || strings.TrimSpace(e.endpoint) == "" {
		return nil, errors.New("local embedding endpoint is not configured")
	}
	if len(text) == 0 || len(text) > MaxCardTextBytes {
		return nil, errors.New("embedding input is outside the governed size boundary")
	}
	body, err := json.Marshal(struct {
		Model          string `json:"model"`
		Input          string `json:"input"`
		EncodingFormat string `json:"encoding_format"`
	}{Model: e.descriptor.Model, Input: text, EncodingFormat: "float"})
	if err != nil {
		return nil, errors.New("encode local embedding request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("build local embedding request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := e.client.Do(request)
	if err != nil {
		return nil, errors.New("local embedding request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("local embedding request failed: status %d", response.StatusCode)
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxLocalEmbeddingResponseBytes+1))
	if err != nil {
		return nil, errors.New("read local embedding response")
	}
	if len(responseBody) == 0 || len(responseBody) > maxLocalEmbeddingResponseBytes {
		return nil, errors.New("local embedding response is invalid")
	}
	var payload struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return nil, errors.New("decode local embedding response")
	}
	if len(payload.Data) != 1 || len(payload.Data[0].Embedding) != e.descriptor.Dimensions {
		return nil, errors.New("local embedding dimensions are invalid")
	}
	for _, value := range payload.Data[0].Embedding {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, errors.New("local embedding contains non-finite values")
		}
	}
	return append([]float64(nil), payload.Data[0].Embedding...), nil
}

func validateLocalEmbeddingConfig(config LocalHTTPEmbeddingConfig) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(config.Endpoint))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("local embedding endpoint is invalid")
	}
	if !isLoopbackHost(parsed.Hostname()) {
		return "", errors.New("local embedding endpoint must use a loopback host")
	}
	if strings.TrimSpace(config.Model) == "" || len(config.Model) > 200 ||
		strings.TrimSpace(config.Version) == "" || len(config.Version) > 160 ||
		strings.TrimSpace(config.SparseEncoderVersion) == "" || len(config.SparseEncoderVersion) > 160 ||
		config.Dimensions < 1 || config.Dimensions > 4096 {
		return "", errors.New("local embedding configuration is invalid")
	}
	return strings.TrimRight(parsed.String(), "/") + "/v1/embeddings", nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
