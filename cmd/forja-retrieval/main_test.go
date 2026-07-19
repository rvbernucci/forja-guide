package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunRejectsUnsafeOperationShapesBeforeOpeningDependencies(t *testing.T) {
	for _, arguments := range [][]string{
		nil,
		{"unknown"},
		{"query", "--input", "-", "--output", "result.json"},
		{"project-once", "--worker-id", "worker", "--output", "receipt.json", "--timeout", "31s"},
	} {
		var stdout, stderr bytes.Buffer
		if err := run(context.Background(), arguments, &stdout, &stderr, func(string) (string, bool) { return "", false }); err == nil {
			t.Fatalf("arguments %v unexpectedly succeeded", arguments)
		}
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
	}
	lookup := func(key string) (string, bool) { value, found := valid[key]; return value, found }
	config, err := runtimeConfigFromEnv(lookup)
	if err != nil || config.qdrantPort != 6334 || !config.qdrantTLS {
		t.Fatalf("valid config=%#v err=%v", config, err)
	}
	delete(valid, "FORJA_QDRANT_API_KEY")
	if _, err := runtimeConfigFromEnv(lookup); err == nil || strings.Contains(err.Error(), "secret-not-printed") {
		t.Fatalf("unsafe remote Qdrant error=%v", err)
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
