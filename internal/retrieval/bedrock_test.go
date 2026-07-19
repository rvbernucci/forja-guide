package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

func TestBedrockTitanEmbedderPinsRequestAndDescriptor(t *testing.T) {
	t.Parallel()
	client := &recordingBedrockRuntime{output: titanEmbeddingOutput(BedrockTitanTextEmbeddingV2Dimensions)}
	config := DefaultBedrockTitanConfig(SparseEncoderVersion)
	config.Region = "us-east-1"
	config.Now = func() time.Time { return time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC) }
	embedder, err := NewBedrockTitanEmbedderWithClient(config, client)
	if err != nil {
		t.Fatal(err)
	}

	vector, err := embedder.Embed(t.Context(), "canonical card")
	if err != nil {
		t.Fatal(err)
	}
	if len(vector) != BedrockTitanTextEmbeddingV2Dimensions || vector[0] != 0.25 {
		t.Fatalf("vector contract=%d first=%v", len(vector), vector[0])
	}
	if client.input == nil || *client.input.ModelId != BedrockTitanTextEmbeddingV2Model ||
		*client.input.ContentType != "application/json" || *client.input.Accept != "application/json" {
		t.Fatalf("Bedrock request=%#v", client.input)
	}
	var request struct {
		InputText  string `json:"inputText"`
		Dimensions int    `json:"dimensions"`
		Normalize  bool   `json:"normalize"`
	}
	if err := json.Unmarshal(client.input.Body, &request); err != nil {
		t.Fatal(err)
	}
	if request.InputText != "canonical card" || request.Dimensions != 1024 || !request.Normalize {
		t.Fatalf("Titan request=%#v", request)
	}
	descriptor := embedder.Descriptor()
	if descriptor.Version != BedrockTitanTextEmbeddingV2Version || descriptor.EmbeddedAt.IsZero() || descriptor.SparseEncoderVersion != SparseEncoderVersion {
		t.Fatalf("descriptor=%#v", descriptor)
	}
}

func TestBedrockTitanEmbedderFailsClosedWithoutLeakingInput(t *testing.T) {
	t.Parallel()
	client := &recordingBedrockRuntime{err: errors.New("provider rejected secret-card-text")}
	config := DefaultBedrockTitanConfig(SparseEncoderVersion)
	config.Region = "us-east-1"
	embedder, err := NewBedrockTitanEmbedderWithClient(config, client)
	if err != nil {
		t.Fatal(err)
	}
	_, err = embedder.Embed(t.Context(), "secret-card-text")
	if err == nil || strings.Contains(err.Error(), "secret-card-text") || strings.Contains(err.Error(), "provider rejected") {
		t.Fatalf("provider error leaked into boundary: %v", err)
	}
	if _, err := embedder.Embed(t.Context(), strings.Repeat("x", MaxCardTextBytes+1)); err == nil {
		t.Fatal("oversized card accepted")
	}
}

func TestBedrockTitanEmbedderRejectsInvalidResponseAndConfig(t *testing.T) {
	t.Parallel()
	config := DefaultBedrockTitanConfig(SparseEncoderVersion)
	config.Region = "us-east-1"
	client := &recordingBedrockRuntime{output: &bedrockruntime.InvokeModelOutput{Body: []byte(`{"embedding":[0.1]}`)}}
	embedder, err := NewBedrockTitanEmbedderWithClient(config, client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := embedder.Embed(t.Context(), "card"); err == nil {
		t.Fatal("short Bedrock vector accepted")
	}
	config.Model = ""
	if _, err := NewBedrockTitanEmbedderWithClient(config, client); err == nil {
		t.Fatal("invalid config accepted")
	}
}

type recordingBedrockRuntime struct {
	input  *bedrockruntime.InvokeModelInput
	output *bedrockruntime.InvokeModelOutput
	err    error
}

func (r *recordingBedrockRuntime) InvokeModel(_ context.Context, input *bedrockruntime.InvokeModelInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	r.input = input
	return r.output, r.err
}

func titanEmbeddingOutput(dimensions int) *bedrockruntime.InvokeModelOutput {
	vector := make([]float64, dimensions)
	vector[0] = 0.25
	body, err := json.Marshal(map[string]any{"embedding": vector})
	if err != nil {
		panic(err)
	}
	contentType := "application/json"
	return &bedrockruntime.InvokeModelOutput{Body: body, ContentType: &contentType}
}
