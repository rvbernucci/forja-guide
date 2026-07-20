package retrieval

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLocalHTTPEmbedderUsesLoopbackOpenAICompatibleEndpoint(t *testing.T) {
	t.Parallel()
	var requestModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Method != http.MethodPost {
			t.Fatalf("request path=%s method=%s", r.URL.Path, r.Method)
		}
		var request struct {
			Model          string `json:"model"`
			Input          string `json:"input"`
			EncodingFormat string `json:"encoding_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		requestModel = request.Model
		if request.Input != "canonical card" || request.EncodingFormat != "float" {
			t.Fatalf("request=%#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.25,0.5,0.75]}]}`))
	}))
	t.Cleanup(server.Close)
	embedder, err := NewLocalHTTPEmbedder(LocalHTTPEmbeddingConfig{
		Endpoint: server.URL, Model: "local-bge", Version: "rocm-q8",
		Dimensions: 3, SparseEncoderVersion: SparseEncoderVersion,
		Now: func() time.Time { return time.Date(2026, 7, 20, 14, 30, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}

	vector, err := embedder.Embed(t.Context(), "canonical card")
	if err != nil {
		t.Fatal(err)
	}
	if requestModel != "local-bge" || len(vector) != 3 || vector[0] != 0.25 {
		t.Fatalf("model=%s vector=%v", requestModel, vector)
	}
	descriptor := embedder.Descriptor()
	if descriptor.Model != "local-bge" || descriptor.Version != "rocm-q8" ||
		descriptor.Dimensions != 3 || descriptor.EmbeddedAt.IsZero() {
		t.Fatalf("descriptor=%#v", descriptor)
	}
}

func TestLocalHTTPEmbedderRejectsRemoteEndpointAndBadResponses(t *testing.T) {
	t.Parallel()
	if _, err := NewLocalHTTPEmbedder(LocalHTTPEmbeddingConfig{
		Endpoint: "https://api.example.com", Model: "model", Version: "v1",
		Dimensions: 3, SparseEncoderVersion: SparseEncoderVersion,
	}); err == nil {
		t.Fatal("remote embedding endpoint accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1]}]}`))
	}))
	t.Cleanup(server.Close)
	embedder, err := NewLocalHTTPEmbedder(LocalHTTPEmbeddingConfig{
		Endpoint: server.URL, Model: "model", Version: "v1",
		Dimensions: 3, SparseEncoderVersion: SparseEncoderVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := embedder.Embed(t.Context(), "card"); err == nil {
		t.Fatal("short local vector accepted")
	}
}

func TestLocalHTTPEmbedderFailsClosedWithoutLeakingInput(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "provider saw secret-card-text", http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)
	embedder, err := NewLocalHTTPEmbedder(LocalHTTPEmbeddingConfig{
		Endpoint: server.URL, Model: "model", Version: "v1",
		Dimensions: 3, SparseEncoderVersion: SparseEncoderVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = embedder.Embed(t.Context(), "secret-card-text")
	if err == nil || strings.Contains(err.Error(), "secret-card-text") || strings.Contains(err.Error(), "provider saw") {
		t.Fatalf("local provider error leaked into boundary: %v", err)
	}
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"embedding":[` + strings.Repeat("0,", 4095) + `0]}]}`))
	})
	if _, err := embedder.Embed(t.Context(), strings.Repeat("x", MaxCardTextBytes+1)); err == nil {
		t.Fatal("oversized local embedding input accepted")
	}
}

func TestLocalHTTPEmbedderRejectsNonFiniteValues(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body, _ := json.Marshal(map[string]any{
			"data": []map[string]any{{"embedding": []float64{math.Inf(1)}}},
		})
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	embedder, err := NewLocalHTTPEmbedder(LocalHTTPEmbeddingConfig{
		Endpoint: server.URL, Model: "model", Version: "v1",
		Dimensions: 1, SparseEncoderVersion: SparseEncoderVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := embedder.Embed(t.Context(), "card"); err == nil {
		t.Fatal("non-finite vector accepted")
	}
}
