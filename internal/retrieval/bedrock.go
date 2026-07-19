package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/rvbernucci/forja-guide/internal/contracts"
)

const (
	// BedrockTitanTextEmbeddingV2Model is the current AWS model ID selected for
	// governed retrieval. Operators must still verify model access in the region.
	BedrockTitanTextEmbeddingV2Model      = "amazon.titan-embed-g1-text-02"
	BedrockTitanTextEmbeddingV2Version    = "g1-text-v2-1024"
	BedrockTitanTextEmbeddingV2Dimensions = 1024
	maxBedrockEmbeddingResponseBytes      = 1 << 20
)

// BedrockRuntimeInvoker is deliberately narrow so the retrieval package does
// not gain general Bedrock or AWS control-plane authority.
type BedrockRuntimeInvoker interface {
	InvokeModel(context.Context, *bedrockruntime.InvokeModelInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error)
}

// BedrockTitanConfig pins the semantic vector contract independently from AWS
// credentials. Authentication comes only from the AWS SDK default chain.
type BedrockTitanConfig struct {
	Region               string
	Model                string
	Version              string
	Dimensions           int
	SparseEncoderVersion string
	Now                  func() time.Time
}

// DefaultBedrockTitanConfig returns the pinned production candidate. A caller
// must provide the sparse encoder version used by its collection generation.
func DefaultBedrockTitanConfig(sparseEncoderVersion string) BedrockTitanConfig {
	return BedrockTitanConfig{
		Model:                BedrockTitanTextEmbeddingV2Model,
		Version:              BedrockTitanTextEmbeddingV2Version,
		Dimensions:           BedrockTitanTextEmbeddingV2Dimensions,
		SparseEncoderVersion: sparseEncoderVersion,
	}
}

// BedrockTitanEmbedder invokes only the configured Titan embedding model.
// It never reads application-specific bearer-token variables or logs card text.
type BedrockTitanEmbedder struct {
	client     BedrockRuntimeInvoker
	descriptor contracts.EmbeddingDescriptor
}

// NewBedrockTitanEmbedder resolves standard AWS SDK credentials (for example a
// task role, web identity, or an explicitly configured local profile). It does
// not make an AWS call during construction.
func NewBedrockTitanEmbedder(ctx context.Context, config BedrockTitanConfig) (*BedrockTitanEmbedder, error) {
	if err := validateBedrockTitanConfig(config); err != nil {
		return nil, err
	}
	sdkConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(config.Region))
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	return NewBedrockTitanEmbedderWithClient(config, bedrockruntime.NewFromConfig(sdkConfig))
}

// NewBedrockTitanEmbedderWithClient supports deterministic tests and explicit
// dependency injection. It must only receive a Bedrock Runtime data-plane client.
func NewBedrockTitanEmbedderWithClient(config BedrockTitanConfig, client BedrockRuntimeInvoker) (*BedrockTitanEmbedder, error) {
	if err := validateBedrockTitanConfig(config); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("Bedrock Runtime client is required")
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &BedrockTitanEmbedder{
		client: client,
		descriptor: contracts.EmbeddingDescriptor{
			Model: config.Model, Version: config.Version, Dimensions: config.Dimensions,
			SparseEncoderVersion: config.SparseEncoderVersion, EmbeddedAt: now().UTC(),
		},
	}, nil
}

func (e *BedrockTitanEmbedder) Descriptor() contracts.EmbeddingDescriptor {
	return e.descriptor
}

// Embed submits one bounded canonical card or query and validates all provider
// output before it can enter a retrieval point or Qdrant request.
func (e *BedrockTitanEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	if e == nil || e.client == nil {
		return nil, errors.New("Bedrock Titan embedder is not configured")
	}
	if len(text) == 0 || len(text) > MaxCardTextBytes {
		return nil, errors.New("embedding input is outside the governed size boundary")
	}
	body, err := json.Marshal(struct {
		InputText  string `json:"inputText"`
		Dimensions int    `json:"dimensions"`
		Normalize  bool   `json:"normalize"`
	}{InputText: text, Dimensions: e.descriptor.Dimensions, Normalize: true})
	if err != nil {
		return nil, errors.New("encode Bedrock Titan embedding request")
	}
	output, err := e.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId: aws.String(e.descriptor.Model), ContentType: aws.String("application/json"),
		Accept: aws.String("application/json"), Body: body,
	})
	if err != nil {
		return nil, errors.New("Bedrock Titan embedding request failed")
	}
	if output == nil || output.ContentType == nil || *output.ContentType != "application/json" ||
		len(output.Body) == 0 || len(output.Body) > maxBedrockEmbeddingResponseBytes {
		return nil, errors.New("Bedrock Titan embedding response is invalid")
	}
	var response struct {
		Embedding []float64 `json:"embedding"`
	}
	if err := json.Unmarshal(output.Body, &response); err != nil {
		return nil, errors.New("decode Bedrock Titan embedding response")
	}
	if len(response.Embedding) != e.descriptor.Dimensions {
		return nil, errors.New("Bedrock Titan embedding dimensions are invalid")
	}
	for _, value := range response.Embedding {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, errors.New("Bedrock Titan embedding contains non-finite values")
		}
	}
	return append([]float64(nil), response.Embedding...), nil
}

func validateBedrockTitanConfig(config BedrockTitanConfig) error {
	if strings.TrimSpace(config.Region) == "" || len(config.Region) > 64 ||
		strings.TrimSpace(config.Model) == "" || len(config.Model) > 200 ||
		strings.TrimSpace(config.Version) == "" || len(config.Version) > 160 ||
		strings.TrimSpace(config.SparseEncoderVersion) == "" || len(config.SparseEncoderVersion) > 160 ||
		config.Dimensions < 1 || config.Dimensions > 65536 {
		return errors.New("Bedrock Titan embedding configuration is invalid")
	}
	return nil
}
