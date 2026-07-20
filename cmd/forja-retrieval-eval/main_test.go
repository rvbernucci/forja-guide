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
    {"case_id":"safety","expected_no_accepted":true,"safety_class":"stale"}
  ]
}`)
	writeEvaluationFixture(t, outcomesPath, `{
  "schema_version":"1.0","corpus_id":"retrieval_eval_fixture",
  "outcomes":[
    {"case_id":"positive","accepted_entity_ids":["symbol_one"],"latency_milliseconds":1,"projection_lag_events":0},
    {"case_id":"safety","accepted_entity_ids":[],"latency_milliseconds":1,"projection_lag_events":0}
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
	if report.CorpusID != "retrieval_eval_fixture" || report.SampleCount != 2 || report.Metrics.ExpectedNoAcceptedPass != 1 || report.Metrics.StaleRejectionPass != 1 || report.Metrics.EntityResolutionAccuracy != 1 || report.Metrics.RecallAtK != 1 || report.Embedding.Dimensions != 3 {
		t.Fatalf("report=%#v", report)
	}
}

func TestRunRejectsMismatchedCorpusAndInvalidRequiredMetadata(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	corpusPath := filepath.Join(directory, "corpus.json")
	outcomesPath := filepath.Join(directory, "outcomes.json")
	writeEvaluationFixture(t, corpusPath, `{"schema_version":"1.0","corpus_id":"retrieval_eval_fixture","split":"public","cases":[{"case_id":"safety","expected_no_accepted":true,"safety_class":"unauthorized"}]}`)
	writeEvaluationFixture(t, outcomesPath, `{"schema_version":"1.0","corpus_id":"retrieval_eval_other","outcomes":[{"case_id":"safety","accepted_entity_ids":[],"latency_milliseconds":1,"projection_lag_events":0}]}`)
	arguments := evaluationArguments(corpusPath, outcomesPath, "-")
	if err := run(context.Background(), arguments, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched corpus error=%v", err)
	}
	arguments[len(arguments)-1] = "not-a-hash"
	if err := run(context.Background(), arguments, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "policy hash") {
		t.Fatalf("invalid metadata error=%v", err)
	}
}

func TestRunRejectsOutcomeWithoutOperationalMeasurements(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	corpusPath := filepath.Join(directory, "corpus.json")
	outcomesPath := filepath.Join(directory, "outcomes.json")
	writeEvaluationFixture(t, corpusPath, `{
  "schema_version":"1.0","corpus_id":"retrieval_eval_fixture","split":"tuning",
  "cases":[{"case_id":"positive","required_entity_ids":["symbol_one"]}]
}`)
	writeEvaluationFixture(t, outcomesPath, `{
  "schema_version":"1.0","corpus_id":"retrieval_eval_fixture",
  "outcomes":[{"case_id":"positive","accepted_entity_ids":["symbol_one"]}]
}`)
	if err := run(context.Background(), evaluationArguments(corpusPath, outcomesPath, "-"), &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "latency_milliseconds") {
		t.Fatalf("missing measurement error=%v", err)
	}
}

func TestRunComparesEveryRequiredBaselineWithoutSelectingOne(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	corpusPath := filepath.Join(directory, "corpus.json")
	comparisonPath := filepath.Join(directory, "comparison.json")
	reportPath := filepath.Join(directory, "comparison-report.json")
	writeEvaluationFixture(t, corpusPath, `{
  "schema_version":"1.0","corpus_id":"retrieval_eval_fixture","split":"tuning",
  "cases":[
    {"case_id":"positive","required_entity_ids":["symbol_one"]},
    {"case_id":"safety","expected_no_accepted":true,"safety_class":"cross_tenant"}
  ]
}`)
	writeEvaluationFixture(t, comparisonPath, `{
  "schema_version":"1.0","corpus_id":"retrieval_eval_fixture",
  "variants":[
    {"name":"lexical_only","policy_hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","outcomes":[{"case_id":"positive","accepted_entity_ids":["symbol_one"],"latency_milliseconds":1,"projection_lag_events":0},{"case_id":"safety","accepted_entity_ids":[],"latency_milliseconds":1,"projection_lag_events":0}]},
    {"name":"dense_only","policy_hash":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","outcomes":[{"case_id":"positive","accepted_entity_ids":[],"latency_milliseconds":1,"projection_lag_events":0},{"case_id":"safety","accepted_entity_ids":["leak"],"latency_milliseconds":1,"projection_lag_events":0}]},
    {"name":"rrf_unweighted","policy_hash":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","outcomes":[{"case_id":"positive","accepted_entity_ids":["symbol_one"],"latency_milliseconds":1,"projection_lag_events":0},{"case_id":"safety","accepted_entity_ids":[],"latency_milliseconds":1,"projection_lag_events":0}]},
    {"name":"rrf_weighted","policy_hash":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","outcomes":[{"case_id":"positive","accepted_entity_ids":["symbol_one"],"latency_milliseconds":1,"projection_lag_events":0},{"case_id":"safety","accepted_entity_ids":[],"latency_milliseconds":1,"projection_lag_events":0}]}
  ]
}`)
	arguments := comparisonArguments(corpusPath, comparisonPath, reportPath)
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), arguments, &stdout, &stderr); err != nil || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("run err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report evaluationComparisonReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Comparisons) != 4 || report.Comparisons[0].Name != "lexical_only" || report.Comparisons[1].Metrics.ExpectedNoAcceptedPass != 0 || report.Comparisons[3].Name != "rrf_weighted" {
		t.Fatalf("report=%#v", report)
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

func comparisonArguments(corpusPath, comparisonPath, outputPath string) []string {
	return []string{
		"--corpus", corpusPath, "--comparison", comparisonPath, "--output", outputPath,
		"--k", "10", "--commit", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"--embedding-model", "fixture", "--embedding-version", "v1",
		"--embedding-dimensions", "3", "--sparse-encoder-version", "sparse-fixture-v1",
	}
}

func writeEvaluationFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
