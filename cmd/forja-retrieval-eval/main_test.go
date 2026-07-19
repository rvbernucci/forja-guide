package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const evaluationPolicyHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestRunScoresValidatedCorpusAndWritesAtomicReport(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	corpusPath := filepath.Join(directory, "corpus.json")
	outcomesPath := filepath.Join(directory, "outcomes.json")
	reportPath := filepath.Join(directory, "report.json")
	writeEvaluationFixture(t, corpusPath, `{
  "schema_version":"1.0","corpus_id":"retrieval_eval_fixture","split":"public",
  "cases":[
    {"case_id":"positive","required_entity_ids":["symbol_one"]},
    {"case_id":"safety","expected_no_accepted":true}
  ]
}`)
	writeEvaluationFixture(t, outcomesPath, `{
  "schema_version":"1.0","corpus_id":"retrieval_eval_fixture",
  "outcomes":[
    {"case_id":"positive","accepted_entity_ids":["symbol_one"]},
    {"case_id":"safety","accepted_entity_ids":[]}
  ]
}`)
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), evaluationArguments(corpusPath, outcomesPath, reportPath), &stdout, &stderr)
	if err != nil || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("run err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report evaluationReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.CorpusID != "retrieval_eval_fixture" || report.SampleCount != 2 || report.Metrics.ExpectedNoAcceptedPass != 1 || report.Metrics.RecallAtK != 1 || report.Embedding.Dimensions != 3 {
		t.Fatalf("report=%#v", report)
	}
}

func TestRunRejectsMismatchedCorpusAndInvalidRequiredMetadata(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	corpusPath := filepath.Join(directory, "corpus.json")
	outcomesPath := filepath.Join(directory, "outcomes.json")
	writeEvaluationFixture(t, corpusPath, `{"schema_version":"1.0","corpus_id":"retrieval_eval_fixture","split":"public","cases":[{"case_id":"safety","expected_no_accepted":true}]}`)
	writeEvaluationFixture(t, outcomesPath, `{"schema_version":"1.0","corpus_id":"retrieval_eval_other","outcomes":[{"case_id":"safety","accepted_entity_ids":[]}]}`)
	arguments := evaluationArguments(corpusPath, outcomesPath, "-")
	if err := run(context.Background(), arguments, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched corpus error=%v", err)
	}
	arguments[len(arguments)-1] = "not-a-hash"
	if err := run(context.Background(), arguments, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "policy hash") {
		t.Fatalf("invalid metadata error=%v", err)
	}
}

func evaluationArguments(corpusPath, outcomesPath, outputPath string) []string {
	return []string{
		"--corpus", corpusPath, "--outcomes", outcomesPath, "--output", outputPath,
		"--k", "10", "--commit", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"--embedding-model", "fixture", "--embedding-version", "v1",
		"--embedding-dimensions", "3", "--sparse-encoder-version", "sparse-fixture-v1",
		"--policy-hash", evaluationPolicyHash,
	}
}

func writeEvaluationFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
