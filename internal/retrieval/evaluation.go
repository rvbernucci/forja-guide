package retrieval

import (
	"fmt"
	"math"
	"sort"
)

// EvaluationCase contains only the stable entity identities expected for one
// offline retrieval evaluation. Query bodies, cards, and private holdout
// answers stay in the evaluation corpus and are intentionally not part of the
// runtime retrieval contract.
type EvaluationCase struct {
	CaseID             string   `json:"case_id"`
	RequiredEntityIDs  []string `json:"required_entity_ids,omitempty"`
	ExpectedNoAccepted bool     `json:"expected_no_accepted,omitempty"`
}

// EvaluationOutcome is the canonically accepted entity sequence captured from
// one retrieval run. It is evaluated offline; the production service never
// receives expected entities or evaluation labels.
type EvaluationOutcome struct {
	CaseID            string   `json:"case_id"`
	AcceptedEntityIDs []string `json:"accepted_entity_ids"`
}

// RankingMetrics reports macro-averaged ranking quality at a fixed bounded K.
// Precision uses the number of returned positions up to K as its denominator,
// which measures whether the selected context is relevant without penalizing a
// governed result for intentionally returning fewer than K safe candidates.
type RankingMetrics struct {
	Cases                   int     `json:"cases"`
	RecallAtK               float64 `json:"recall_at_k"`
	PrecisionAtK            float64 `json:"precision_at_k"`
	MeanReciprocalRank      float64 `json:"mean_reciprocal_rank"`
	NDCGAtK                 float64 `json:"ndcg_at_k"`
	ExpectedNoAcceptedCases int     `json:"expected_no_accepted_cases"`
	ExpectedNoAcceptedPass  int     `json:"expected_no_accepted_pass"`
}

// ScoreRankings validates a complete, unique offline result set and computes
// deterministic macro metrics. It rejects duplicate or unknown case IDs so a
// missing difficult case cannot improve a report by omission.
func ScoreRankings(cases []EvaluationCase, outcomes []EvaluationOutcome, k int) (RankingMetrics, error) {
	if len(cases) == 0 || len(cases) > 100_000 || k < 1 || k > 1_000 || len(outcomes) != len(cases) {
		return RankingMetrics{}, fmt.Errorf("retrieval evaluation input is invalid")
	}
	expected := make(map[string]EvaluationCase, len(cases))
	for _, evaluationCase := range cases {
		if err := validateEvaluationCase(evaluationCase); err != nil {
			return RankingMetrics{}, err
		}
		if _, exists := expected[evaluationCase.CaseID]; exists {
			return RankingMetrics{}, fmt.Errorf("retrieval evaluation case IDs must be unique")
		}
		expected[evaluationCase.CaseID] = evaluationCase
	}
	metrics := RankingMetrics{Cases: len(cases)}
	seenOutcomes := make(map[string]struct{}, len(outcomes))
	for _, outcome := range outcomes {
		evaluationCase, found := expected[outcome.CaseID]
		if !found || outcome.CaseID == "" || len(outcome.AcceptedEntityIDs) > 1_000 || !uniqueNonEmpty(outcome.AcceptedEntityIDs) {
			return RankingMetrics{}, fmt.Errorf("retrieval evaluation outcome is invalid")
		}
		if _, exists := seenOutcomes[outcome.CaseID]; exists {
			return RankingMetrics{}, fmt.Errorf("retrieval evaluation outcomes must be unique")
		}
		seenOutcomes[outcome.CaseID] = struct{}{}
		if evaluationCase.ExpectedNoAccepted {
			metrics.ExpectedNoAcceptedCases++
			if len(outcome.AcceptedEntityIDs) == 0 {
				metrics.ExpectedNoAcceptedPass++
			}
			continue
		}
		relevant := stringSet(evaluationCase.RequiredEntityIDs)
		limit := min(k, len(outcome.AcceptedEntityIDs))
		hits := 0
		firstRank := 0
		dcg := 0.0
		for index := 0; index < limit; index++ {
			if _, ok := relevant[outcome.AcceptedEntityIDs[index]]; !ok {
				continue
			}
			hits++
			if firstRank == 0 {
				firstRank = index + 1
			}
			dcg += 1 / math.Log2(float64(index)+2)
		}
		metrics.RecallAtK += float64(hits) / float64(len(relevant))
		if limit > 0 {
			metrics.PrecisionAtK += float64(hits) / float64(limit)
		}
		if firstRank > 0 {
			metrics.MeanReciprocalRank += 1 / float64(firstRank)
		}
		idealCount := min(k, len(relevant))
		idealDCG := 0.0
		for index := 0; index < idealCount; index++ {
			idealDCG += 1 / math.Log2(float64(index)+2)
		}
		metrics.NDCGAtK += dcg / idealDCG
	}
	for caseID := range expected {
		if _, found := seenOutcomes[caseID]; !found {
			return RankingMetrics{}, fmt.Errorf("retrieval evaluation outcome is missing")
		}
	}
	positiveCases := metrics.Cases - metrics.ExpectedNoAcceptedCases
	if positiveCases > 0 {
		metrics.RecallAtK /= float64(positiveCases)
		metrics.PrecisionAtK /= float64(positiveCases)
		metrics.MeanReciprocalRank /= float64(positiveCases)
		metrics.NDCGAtK /= float64(positiveCases)
	}
	return metrics, nil
}

func validateEvaluationCase(evaluationCase EvaluationCase) error {
	if evaluationCase.CaseID == "" || len(evaluationCase.CaseID) > 200 || !uniqueNonEmpty(evaluationCase.RequiredEntityIDs) || len(evaluationCase.RequiredEntityIDs) > 1_000 {
		return fmt.Errorf("retrieval evaluation case is invalid")
	}
	if evaluationCase.ExpectedNoAccepted == (len(evaluationCase.RequiredEntityIDs) != 0) {
		return fmt.Errorf("retrieval evaluation expectation is invalid")
	}
	return nil
}

func uniqueNonEmpty(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

// SortedCaseIDs returns a stable case order for reports and corpus manifests.
func SortedCaseIDs(cases []EvaluationCase) []string {
	result := make([]string, 0, len(cases))
	for _, evaluationCase := range cases {
		result = append(result, evaluationCase.CaseID)
	}
	sort.Strings(result)
	return result
}
