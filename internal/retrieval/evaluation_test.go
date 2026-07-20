package retrieval

import (
	"encoding/json"
	"math"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

func TestScoreRankingsComputesMacroQualityAndSafetyCases(t *testing.T) {
	cases := []EvaluationCase{
		{CaseID: "lexical", RequiredEntityIDs: []string{"symbol_login", "test_login"}},
		{CaseID: "semantic", RequiredEntityIDs: []string{"decision_auth"}},
		{CaseID: "stale", ExpectedNoAccepted: true, SafetyClass: EvaluationSafetyClassStale},
	}
	metrics, err := ScoreRankings(cases, []EvaluationOutcome{
		{CaseID: "lexical", AcceptedEntityIDs: []string{"symbol_login", "noise", "test_login"}, LatencyMilliseconds: 4, ProjectionLagEvents: 1},
		{CaseID: "semantic", AcceptedEntityIDs: []string{"noise", "decision_auth"}, LatencyMilliseconds: 10, ProjectionLagEvents: 2},
		{CaseID: "stale", AcceptedEntityIDs: []string{}, LatencyMilliseconds: 6, ProjectionLagEvents: 3},
	}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Cases != 3 || metrics.ExpectedNoAcceptedCases != 1 || metrics.ExpectedNoAcceptedPass != 1 {
		t.Fatalf("metrics=%#v", metrics)
	}
	if !approximately(metrics.RecallAtK, 1) || !approximately(metrics.PrecisionAtK, (2.0/3.0+1.0/2.0)/2.0) || !approximately(metrics.MeanReciprocalRank, (1.0+0.5)/2.0) {
		t.Fatalf("metrics=%#v", metrics)
	}
	if metrics.NDCGAtK <= 0 || metrics.NDCGAtK >= 1 {
		t.Fatalf("nDCG=%f", metrics.NDCGAtK)
	}
	if metrics.AcceptedEntityCount != 5 || metrics.ResolvedEntityCount != 3 ||
		!approximately(metrics.EntityResolutionAccuracy, 0.6) ||
		metrics.StaleRejectionCases != 1 || metrics.StaleRejectionPass != 1 ||
		!approximately(metrics.MeanLatencyMilliseconds, 20.0/3.0) ||
		metrics.P95LatencyMilliseconds != 10 || metrics.MaxLatencyMilliseconds != 10 ||
		!approximately(metrics.MeanProjectionLagEvents, 2) || metrics.MaxProjectionLagEvents != 3 {
		t.Fatalf("extended metrics=%#v", metrics)
	}
}

func TestScoreRankingsRejectsMissingDuplicateAndInvalidOutcomes(t *testing.T) {
	cases := []EvaluationCase{{CaseID: "case", RequiredEntityIDs: []string{"symbol_one"}}}
	if _, err := ScoreRankings(cases, nil, 1); err == nil {
		t.Fatal("missing outcome accepted")
	}
	if _, err := ScoreRankings(cases, []EvaluationOutcome{{CaseID: "case", AcceptedEntityIDs: []string{"symbol_one", "symbol_one"}}}, 1); err == nil {
		t.Fatal("duplicate accepted entity accepted")
	}
	if _, err := ScoreRankings(cases, []EvaluationOutcome{{CaseID: "other", AcceptedEntityIDs: []string{"symbol_one"}}}, 1); err == nil {
		t.Fatal("unknown outcome accepted")
	}
}

func TestScoreRankingsReportsSafetyFailureWithoutDiscardingTheCase(t *testing.T) {
	metrics, err := ScoreRankings(
		[]EvaluationCase{{CaseID: "cross_tenant", ExpectedNoAccepted: true, SafetyClass: EvaluationSafetyClassCrossTenant}},
		[]EvaluationOutcome{{CaseID: "cross_tenant", AcceptedEntityIDs: []string{"leaked_entity"}}},
		10,
	)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.ExpectedNoAcceptedCases != 1 || metrics.ExpectedNoAcceptedPass != 0 ||
		metrics.CrossTenantRejectionCases != 1 || metrics.CrossTenantRejectionPass != 0 {
		t.Fatalf("metrics=%#v", metrics)
	}
}

func TestCompareRequiredRankingsKeepsEveryBaselineInStableOrder(t *testing.T) {
	cases := []EvaluationCase{
		{CaseID: "positive", RequiredEntityIDs: []string{"symbol_one"}},
		{CaseID: "safety", ExpectedNoAccepted: true, SafetyClass: EvaluationSafetyClassUnauthorized},
	}
	perfect := []EvaluationOutcome{
		{CaseID: "positive", AcceptedEntityIDs: []string{"symbol_one"}},
		{CaseID: "safety", AcceptedEntityIDs: []string{}},
	}
	missed := []EvaluationOutcome{
		{CaseID: "positive", AcceptedEntityIDs: []string{"noise"}},
		{CaseID: "safety", AcceptedEntityIDs: []string{"leak"}},
	}
	variants := []EvaluationVariant{
		{Name: "rrf_weighted", PolicyHash: testEvaluationHash("d"), Outcomes: perfect},
		{Name: "dense_only", PolicyHash: testEvaluationHash("b"), Outcomes: missed},
		{Name: "lexical_only", PolicyHash: testEvaluationHash("a"), Outcomes: perfect},
		{Name: "rrf_unweighted", PolicyHash: testEvaluationHash("c"), Outcomes: perfect},
	}
	comparisons, err := CompareRequiredRankings(cases, variants, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(comparisons) != 4 || comparisons[0].Name != "lexical_only" || comparisons[1].Name != "dense_only" || comparisons[1].Metrics.RecallAtK != 0 || comparisons[3].Name != "rrf_weighted" {
		t.Fatalf("comparisons=%#v", comparisons)
	}
	if _, err := CompareRequiredRankings(cases, variants[:3], 2); err == nil {
		t.Fatal("missing baseline accepted")
	}
	variants[3].Name = "dense_only"
	if _, err := CompareRequiredRankings(cases, variants, 2); err == nil {
		t.Fatal("duplicate baseline accepted")
	}
}

func TestScoreRankingsRejectsMissingOrInvalidSafetyAndOperationalFields(t *testing.T) {
	valid := []EvaluationCase{{CaseID: "positive", RequiredEntityIDs: []string{"symbol_one"}}}
	if _, err := ScoreRankings([]EvaluationCase{{CaseID: "safety", ExpectedNoAccepted: true}}, []EvaluationOutcome{{CaseID: "safety"}}, 1); err == nil {
		t.Fatal("safety case without class accepted")
	}
	if _, err := ScoreRankings(valid, []EvaluationOutcome{{CaseID: "positive", LatencyMilliseconds: 30_001}}, 1); err == nil {
		t.Fatal("over-budget latency accepted")
	}
	if _, err := ScoreRankings(valid, []EvaluationOutcome{{CaseID: "positive", ProjectionLagEvents: -1}}, 1); err == nil {
		t.Fatal("negative projection lag accepted")
	}
}

func TestSortedCaseIDsIsStable(t *testing.T) {
	got := SortedCaseIDs([]EvaluationCase{{CaseID: "z"}, {CaseID: "a"}, {CaseID: "m"}})
	if !slices.Equal(got, []string{"a", "m", "z"}) {
		t.Fatalf("order=%v", got)
	}
}

func TestPublicEvaluationCorpusIsScoreableAndContainsSafetyCases(t *testing.T) {
	data, err := os.ReadFile("testdata/retrieval_evaluation_public_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		SchemaVersion string           `json:"schema_version"`
		CorpusID      string           `json:"corpus_id"`
		Split         string           `json:"split"`
		Cases         []EvaluationCase `json:"cases"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.SchemaVersion != "1.0" || corpus.CorpusID != "retrieval_eval_public_synthetic_v1" || corpus.Split != "public" || len(corpus.Cases) < 4 {
		t.Fatalf("corpus=%#v", corpus)
	}
	outcomeData, err := os.ReadFile("testdata/retrieval_evaluation_public_outcomes_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		CorpusID string              `json:"corpus_id"`
		Outcomes []EvaluationOutcome `json:"outcomes"`
	}
	if err := registry.ValidateJSON("retrieval-evaluation-outcomes.schema.json", outcomeData); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(outcomeData, &capture); err != nil {
		t.Fatal(err)
	}
	if capture.CorpusID != corpus.CorpusID {
		t.Fatalf("outcome corpus=%q, want %q", capture.CorpusID, corpus.CorpusID)
	}
	metrics, err := ScoreRankings(corpus.Cases, capture.Outcomes, 10)
	if err != nil || metrics.ExpectedNoAcceptedPass != 2 || !approximately(metrics.RecallAtK, 1) {
		t.Fatalf("metrics=%#v err=%v", metrics, err)
	}
}

func TestPublicEvaluationComparisonKeepsRequiredBaselinesAndSafety(t *testing.T) {
	corpusData, err := os.ReadFile("testdata/retrieval_evaluation_public_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	comparisonData, err := os.ReadFile("testdata/retrieval_evaluation_public_comparison_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Cases []EvaluationCase `json:"cases"`
	}
	var comparison struct {
		Variants []EvaluationVariant `json:"variants"`
	}
	if err := json.Unmarshal(corpusData, &corpus); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(comparisonData, &comparison); err != nil {
		t.Fatal(err)
	}
	results, err := CompareRequiredRankings(corpus.Cases, comparison.Variants, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(RequiredRetrievalBaselines) || results[0].Metrics.RecallAtK != 0.5 || results[1].Metrics.RecallAtK != 0.75 || results[2].Metrics.ExpectedNoAcceptedPass != 2 || results[3].Metrics.NDCGAtK != 1 {
		t.Fatalf("results=%#v", results)
	}
}

func approximately(left, right float64) bool {
	return math.Abs(left-right) < 0.000001
}

func testEvaluationHash(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
