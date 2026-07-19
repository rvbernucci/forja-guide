package retrieval

import (
	"encoding/json"
	"math"
	"os"
	"slices"
	"testing"
)

func TestScoreRankingsComputesMacroQualityAndSafetyCases(t *testing.T) {
	cases := []EvaluationCase{
		{CaseID: "lexical", RequiredEntityIDs: []string{"symbol_login", "test_login"}},
		{CaseID: "semantic", RequiredEntityIDs: []string{"decision_auth"}},
		{CaseID: "stale", ExpectedNoAccepted: true},
	}
	metrics, err := ScoreRankings(cases, []EvaluationOutcome{
		{CaseID: "lexical", AcceptedEntityIDs: []string{"symbol_login", "noise", "test_login"}},
		{CaseID: "semantic", AcceptedEntityIDs: []string{"noise", "decision_auth"}},
		{CaseID: "stale", AcceptedEntityIDs: []string{}},
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
	outcomes := make([]EvaluationOutcome, 0, len(corpus.Cases))
	for _, evaluationCase := range corpus.Cases {
		outcome := EvaluationOutcome{CaseID: evaluationCase.CaseID, AcceptedEntityIDs: append([]string(nil), evaluationCase.RequiredEntityIDs...)}
		outcomes = append(outcomes, outcome)
	}
	metrics, err := ScoreRankings(corpus.Cases, outcomes, 10)
	if err != nil || metrics.ExpectedNoAcceptedPass != 2 || !approximately(metrics.RecallAtK, 1) {
		t.Fatalf("metrics=%#v err=%v", metrics, err)
	}
}

func approximately(left, right float64) bool {
	return math.Abs(left-right) < 0.000001
}
