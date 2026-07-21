package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	qdrant "github.com/qdrant/go-client/qdrant"

	"github.com/rvbernucci/forja-guide/internal/retrieval"
)

func TestRunRejectsUnsafeOperationShapesBeforeOpeningDependencies(t *testing.T) {
	for _, arguments := range [][]string{
		nil,
		{"unknown"},
		{"query", "--input", "-", "--output", "result.json"},
		{"capture", "--plan", "-", "--output", "comparison.json"},
		{"capture", "--plan", "plan.json", "--output", "comparison.json", "--query-timeout", "31s"},
		{"preflight", "--output", "receipt.json", "--timeout", "31s"},
		{"project-once", "--worker-id", "worker", "--output", "receipt.json", "--timeout", "31s"},
	} {
		var stdout, stderr bytes.Buffer
		if err := run(context.Background(), arguments, &stdout, &stderr, func(string) (string, bool) { return "", false }); err == nil {
			t.Fatalf("arguments %v unexpectedly succeeded", arguments)
		}
	}
}

func TestPreflightReceiptIsRedactedPrivateAndSchemaValid(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "preflight.json")
	if err := writePreflightReceipt(output, retrievalPreflightReceipt{
		SchemaVersion: "1.0", Generation: "retrieval_generation_" + strings.Repeat("a", 64),
		PostgresReady: true, QdrantVerified: true, EmbeddingProvider: "bedrock",
		EmbeddingDimensions: 1024, BedrockDimensions: 1024,
	}); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"credential", "identity", "collection", "host", "vector"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("preflight receipt leaked %q: %s", forbidden, encoded)
		}
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("preflight permissions=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestPreflightCollectionNameResolvesAliasWithoutTreatingErrorsAsAbsent(t *testing.T) {
	t.Parallel()
	client := testAliasInspector{aliases: []*qdrant.AliasDescription{{AliasName: "forja_retrieval", CollectionName: "forja_retrieval_green"}}}
	target, err := preflightCollectionName(t.Context(), client, "forja_retrieval")
	if err != nil || target != "forja_retrieval_green" {
		t.Fatalf("target=%q err=%v", target, err)
	}
	target, err = preflightCollectionName(t.Context(), testAliasInspector{}, "forja_retrieval_green")
	if err != nil || target != "forja_retrieval_green" {
		t.Fatalf("physical target=%q err=%v", target, err)
	}
	if _, err := preflightCollectionName(t.Context(), testAliasInspector{err: os.ErrPermission}, "forja_retrieval"); err == nil {
		t.Fatal("alias inspection failure was treated as an absent alias")
	}
}

type testAliasInspector struct {
	aliases []*qdrant.AliasDescription
	err     error
}

func (c testAliasInspector) ListAliases(context.Context) ([]*qdrant.AliasDescription, error) {
	return c.aliases, c.err
}

func TestEvaluationCapturePlanAndComparisonStayPrivateAndSchemaValid(t *testing.T) {
	directory := t.TempDir()
	planPath := filepath.Join(directory, "capture-plan.json")
	plan := `{
  "schema_version":"1.0",
  "corpus_id":"retrieval_eval_private_fixture",
  "queries":[{
    "case_id":"case_001",
    "query":{
      "request_id":"retrieval_request_fixture","schema_version":"1.0",
      "tenant_id":"tenant_00000000-0000-4000-8000-000000000001",
      "repository_id":"repo_00000000-0000-4000-8000-000000000002",
      "query":"find repository entrypoint",
      "scope":{"source_commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","allowed_paths":["**"]},
      "filters":{"artifact_families":["symbol"],"authority_classes":["canonical"]},
      "policy":{"limit":5,"dense_limit":10,"sparse_limit":10,"dense_weight":1,"sparse_weight":1,"rrf_k":60}
    }
  }],
  "policies":[
    {"name":"lexical_only","policy":{"limit":5,"dense_limit":10,"sparse_limit":10,"dense_weight":0,"sparse_weight":1,"rrf_k":60}},
    {"name":"dense_only","policy":{"limit":5,"dense_limit":10,"sparse_limit":10,"dense_weight":1,"sparse_weight":0,"rrf_k":60}},
    {"name":"rrf_unweighted","policy":{"limit":5,"dense_limit":10,"sparse_limit":10,"dense_weight":1,"sparse_weight":1,"rrf_k":60}},
    {"name":"rrf_weighted","policy":{"limit":5,"dense_limit":10,"sparse_limit":10,"dense_weight":2,"sparse_weight":1,"rrf_k":60}}
  ]
}`
	if err := os.WriteFile(planPath, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	decoded, err := readEvaluationCapturePlan(planPath)
	if err != nil || decoded.CorpusID != "retrieval_eval_private_fixture" || len(decoded.Queries) != 1 || len(decoded.Policies) != 4 {
		t.Fatalf("plan=%#v err=%v", decoded, err)
	}
	output := filepath.Join(directory, "comparison.json")
	variants := make([]retrieval.EvaluationVariant, 0, 4)
	for _, name := range []string{"lexical_only", "dense_only", "rrf_unweighted", "rrf_weighted"} {
		variants = append(variants, retrieval.EvaluationVariant{
			Name: name, PolicyHash: "sha256:" + strings.Repeat("a", 64),
			Outcomes: []retrieval.EvaluationOutcome{{CaseID: "case_001", AcceptedEntityIDs: []string{}}},
		})
	}
	if err := writeEvaluationComparison(output, evaluationComparisonCapture{
		SchemaVersion: "1.0", CorpusID: decoded.CorpusID, Variants: variants,
	}); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"latency_milliseconds": 0`) || !strings.Contains(string(encoded), `"projection_lag_events": 0`) {
		t.Fatalf("required zero capture metadata omitted: %s", encoded)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("comparison permissions=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestRuntimeConfigRequiresEnvironmentBoundariesAndRejectsUnsafeRemoteQdrant(t *testing.T) {
	valid := map[string]string{
		"FORJA_DATABASE_URL":         "postgresql://example.invalid/forja",
		"FORJA_TENANT_ID":            "tenant_00000000-0000-4000-8000-000000000001",
		"FORJA_REPOSITORY_ID":        "repo_00000000-0000-4000-8000-000000000002",
		"FORJA_QDRANT_HOST":          "qdrant.internal",
		"FORJA_QDRANT_API_KEY":       "secret-not-printed",
		"FORJA_QDRANT_TLS":           "true",
		"FORJA_RETRIEVAL_COLLECTION": "forja_retrieval_v1",
		"AWS_REGION":                 "us-east-1",
		"FORJA_S3_BUCKET":            "forja-artifacts",
		"FORJA_S3_REGION":            "us-east-1",
	}
	lookup := func(key string) (string, bool) { value, found := valid[key]; return value, found }
	config, err := runtimeConfigFromEnv(lookup)
	if err != nil || config.qdrantPort != 6334 || !config.qdrantTLS {
		t.Fatalf("valid config=%#v err=%v", config, err)
	}
	if config.embeddingProvider != "bedrock" {
		t.Fatalf("default embedding provider=%q", config.embeddingProvider)
	}
	delete(valid, "FORJA_QDRANT_API_KEY")
	if _, err := runtimeConfigFromEnv(lookup); err == nil || strings.Contains(err.Error(), "secret-not-printed") {
		t.Fatalf("unsafe remote Qdrant error=%v", err)
	}
	valid["FORJA_QDRANT_API_KEY"] = "secret-not-printed"
	valid["CHAVE_API_AWS_BEDROCK"] = "legacy-secret-not-printed"
	if _, err := runtimeConfigFromEnv(lookup); err == nil || strings.Contains(err.Error(), "legacy-secret-not-printed") {
		t.Fatalf("legacy Bedrock credential error=%v", err)
	}
	delete(valid, "CHAVE_API_AWS_BEDROCK")
	valid["AWS_BEARER_TOKEN_BEDROCK"] = "legacy-bearer-not-printed"
	if _, err := runtimeConfigFromEnv(lookup); err == nil || strings.Contains(err.Error(), "legacy-bearer-not-printed") {
		t.Fatalf("legacy Bedrock bearer error=%v", err)
	}
}

func TestRuntimeConfigSupportsLocalRadeonEmbeddingProvider(t *testing.T) {
	valid := map[string]string{
		"FORJA_DATABASE_URL":                 "postgresql://example.invalid/forja",
		"FORJA_TENANT_ID":                    "tenant_00000000-0000-4000-8000-000000000001",
		"FORJA_REPOSITORY_ID":                "repo_00000000-0000-4000-8000-000000000002",
		"FORJA_QDRANT_HOST":                  "127.0.0.1",
		"FORJA_RETRIEVAL_COLLECTION":         "forja_retrieval_v1",
		"FORJA_S3_BUCKET":                    "forja-artifacts",
		"FORJA_S3_REGION":                    "us-east-1",
		"FORJA_RETRIEVAL_EMBEDDING_PROVIDER": "local",
		"FORJA_LOCAL_EMBEDDING_ENDPOINT":     "http://127.0.0.1:8000",
		"FORJA_LOCAL_EMBEDDING_MODEL":        "local-bge",
		"FORJA_LOCAL_EMBEDDING_VERSION":      "rocm-q8",
		"FORJA_LOCAL_EMBEDDING_DIMENSIONS":   "768",
	}
	lookup := func(key string) (string, bool) { value, found := valid[key]; return value, found }
	config, err := runtimeConfigFromEnv(lookup)
	if err != nil {
		t.Fatalf("local config error=%v", err)
	}
	if config.embeddingProvider != "local" || config.region != "" ||
		config.localEmbeddingDimensions != 768 {
		t.Fatalf("local config=%#v", config)
	}
	valid["FORJA_RETRIEVAL_EMBEDDING_PROVIDER"] = "remote"
	if _, err := runtimeConfigFromEnv(lookup); err == nil {
		t.Fatal("invalid embedding provider accepted")
	}
}

func TestReadQueryAndPrivateWriterEnforceContractAndPermissions(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "query.json")
	query := `{
  "request_id":"retrieval_request_fixture","schema_version":"1.0",
  "tenant_id":"tenant_00000000-0000-4000-8000-000000000001",
  "repository_id":"repo_00000000-0000-4000-8000-000000000002",
  "query":"find repository entrypoint",
  "scope":{"source_commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","allowed_paths":["**"]},
  "filters":{"artifact_families":["symbol"],"authority_classes":["canonical"]},
  "policy":{"limit":5,"dense_limit":10,"sparse_limit":10,"dense_weight":1,"sparse_weight":1,"rrf_k":60}
}`
	if err := os.WriteFile(input, []byte(query), 0o600); err != nil {
		t.Fatal(err)
	}
	decoded, err := readQuery(input)
	if err != nil || decoded.RequestID != "retrieval_request_fixture" {
		t.Fatalf("query=%#v err=%v", decoded, err)
	}
	output := filepath.Join(directory, "result.json")
	if err := writePrivateJSON(output, projectionReceipt{SchemaVersion: "1.0", Generation: "retrieval_generation_" + strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("output permissions=%v", info.Mode().Perm())
	}
	if err := os.Chmod(input, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readQuery(input); err == nil || !strings.Contains(err.Error(), "private regular file") {
		t.Fatalf("public query error=%v", err)
	}
}

func TestOperationTimeoutStaysWithinServiceLimit(t *testing.T) {
	if validOperationTimeout(0) || validOperationTimeout(maximumOperationTime+time.Nanosecond) || !validOperationTimeout(maximumOperationTime) {
		t.Fatal("operation timeout boundary is invalid")
	}
}

func TestEvaluationCaptureTimeoutStaysBounded(t *testing.T) {
	if validEvaluationCaptureTimeout(0) || validEvaluationCaptureTimeout(maximumEvaluationCaptureTime+time.Nanosecond) || !validEvaluationCaptureTimeout(maximumEvaluationCaptureTime) {
		t.Fatal("evaluation capture timeout boundary is invalid")
	}
}
