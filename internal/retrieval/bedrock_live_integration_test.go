package retrieval

import (
	"os"
	"testing"
)

// TestLiveBedrockTitanEmbedding is opt-in because it invokes a real provider
// and may incur a small Bedrock charge. It records no text or vector values.
func TestLiveBedrockTitanEmbedding(t *testing.T) {
	if os.Getenv("FORJA_BEDROCK_LIVE") != "1" {
		t.Skip("set FORJA_BEDROCK_LIVE=1 with an AWS workload identity to run")
	}
	region := os.Getenv("FORJA_BEDROCK_REGION")
	if region == "" {
		t.Fatal("FORJA_BEDROCK_REGION is required for the live test")
	}
	config := DefaultBedrockTitanConfig(SparseEncoderVersion)
	config.Region = region
	embedder, err := NewBedrockTitanEmbedder(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	vector, err := embedder.Embed(t.Context(), "Forja governed retrieval compatibility check.")
	if err != nil {
		t.Fatal(err)
	}
	if len(vector) != BedrockTitanTextEmbeddingV2Dimensions {
		t.Fatalf("Bedrock vector dimensions=%d", len(vector))
	}
}
