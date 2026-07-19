package retrieval

import (
	"fmt"
	"math"
	"regexp"
	"sort"
)

var evaluationContentHashPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// RequiredRetrievalBaselines are the fixed policy families that every
// controlled retrieval comparison must retain. The comparison reports metrics;
// it deliberately does not choose a winner or mutate any serving policy.
var RequiredRetrievalBaselines = []string{
	"lexical_only",
	"dense_only",
	"rrf_unweighted",
	"rrf_weighted",
}

const (
	// EvaluationSafetyClassStale identifies a query whose source boundary is no
	// longer current. It must never yield an accepted canonical entity.
	EvaluationSafetyClassStale = "stale"
	// EvaluationSafetyClassCrossTenant identifies a tenant-isolation probe.
	EvaluationSafetyClassCrossTenant = "cross_tenant"
	// EvaluationSafetyClassUnauthorized identifies a scope or authority probe.
	EvaluationSafetyClassUnauthorized = "unauthorized"
	// EvaluationSafetyClassMalformed identifies a malformed or untrusted
	// candidate/payload probe.
	EvaluationSafetyClassMalformed = "malformed"
	// EvaluationSafetyClassScopeBypass identifies an attempted path, language,
	// or artifact-family scope bypass.
	EvaluationSafetyClassScopeBypass = "scope_bypass"
)

// EvaluationCase contains only the stable entity identities expected for one
// offline retrieval evaluation. Query bodies, cards, and private holdout
// answers stay in the evaluation corpus and are intentionally not part of the
// runtime retrieval contract.
type EvaluationCase struct {
	CaseID             string   `json:"case_id"`
	RequiredEntityIDs  []string `json:"required_entity_ids,omitempty"`
	ExpectedNoAccepted bool     `json:"expected_no_accepted,omitempty"`
	SafetyClass        string   `json:"safety_class,omitempty"`
}

// EvaluationOutcome is the canonically accepted entity sequence captured from
// one retrieval run. It is evaluated offline; the production service never
// receives expected entities or evaluation labels.
type EvaluationOutcome struct {
	CaseID            string   `json:"case_id"`
	AcceptedEntityIDs []string `json:"accepted_entity_ids"`
	// LatencyMilliseconds and ProjectionLagEvents are scalar capture metadata.
	// They intentionally exclude query text, vectors, entity names, and Qdrant
	// payloads so a private evaluation can retain performance evidence safely.
	LatencyMilliseconds int64 `json:"latency_milliseconds,omitempty"`
	ProjectionLagEvents int64 `json:"projection_lag_events,omitempty"`
}

// EvaluationVariant captures one complete accepted-entity sequence for a
// frozen policy. PolicyHash binds the result to the exact ranking policy rather
// than treating a human-readable baseline name as sufficient evidence.
type EvaluationVariant struct {
	Name       string              `json:"name"`
	PolicyHash string              `json:"policy_hash"`
	Outcomes   []EvaluationOutcome `json:"outcomes"`
}

// RankingComparison is a deterministic metric record for one required
// baseline. A caller may use tuning results to propose a policy, but holdout,
// OOD, and adversarial outputs must remain reporting evidence only.
type RankingComparison struct {
	Name       string         `json:"name"`
	PolicyHash string         `json:"policy_hash"`
	Metrics    RankingMetrics `json:"metrics"`
}

// RankingMetrics reports macro-averaged ranking quality at a fixed bounded K.
// Precision uses the number of returned positions up to K as its denominator,
// which measures whether the selected context is relevant without penalizing a
// governed result for intentionally returning fewer than K safe candidates.
type RankingMetrics struct {
	Cases                      int     `json:"cases"`
	RecallAtK                  float64 `json:"recall_at_k"`
	PrecisionAtK               float64 `json:"precision_at_k"`
	MeanReciprocalRank         float64 `json:"mean_reciprocal_rank"`
	NDCGAtK                    float64 `json:"ndcg_at_k"`
	AcceptedEntityCount        int     `json:"accepted_entity_count"`
	ResolvedEntityCount        int     `json:"resolved_entity_count"`
	EntityResolutionAccuracy   float64 `json:"entity_resolution_accuracy"`
	ExpectedNoAcceptedCases    int     `json:"expected_no_accepted_cases"`
	ExpectedNoAcceptedPass     int     `json:"expected_no_accepted_pass"`
	StaleRejectionCases        int     `json:"stale_rejection_cases"`
	StaleRejectionPass         int     `json:"stale_rejection_pass"`
	CrossTenantRejectionCases  int     `json:"cross_tenant_rejection_cases"`
	CrossTenantRejectionPass   int     `json:"cross_tenant_rejection_pass"`
	UnauthorizedRejectionCases int     `json:"unauthorized_rejection_cases"`
	UnauthorizedRejectionPass  int     `json:"unauthorized_rejection_pass"`
	MeanLatencyMilliseconds    float64 `json:"mean_latency_milliseconds"`
	P95LatencyMilliseconds     int64   `json:"p95_latency_milliseconds"`
	MaxLatencyMilliseconds     int64   `json:"max_latency_milliseconds"`
	MeanProjectionLagEvents    float64 `json:"mean_projection_lag_events"`
	MaxProjectionLagEvents     int64   `json:"max_projection_lag_events"`
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
	latencies := make([]int64, 0, len(outcomes))
	var totalLatency, totalProjectionLag int64
	seenOutcomes := make(map[string]struct{}, len(outcomes))
	for _, outcome := range outcomes {
		evaluationCase, found := expected[outcome.CaseID]
		if !found || outcome.CaseID == "" || len(outcome.AcceptedEntityIDs) > 1_000 ||
			!uniqueNonEmpty(outcome.AcceptedEntityIDs) || outcome.LatencyMilliseconds < 0 ||
			outcome.LatencyMilliseconds > 30_000 || outcome.ProjectionLagEvents < 0 ||
			outcome.ProjectionLagEvents > 1_000_000 {
			return RankingMetrics{}, fmt.Errorf("retrieval evaluation outcome is invalid")
		}
		if _, exists := seenOutcomes[outcome.CaseID]; exists {
			return RankingMetrics{}, fmt.Errorf("retrieval evaluation outcomes must be unique")
		}
		seenOutcomes[outcome.CaseID] = struct{}{}
		latencies = append(latencies, outcome.LatencyMilliseconds)
		totalLatency += outcome.LatencyMilliseconds
		totalProjectionLag += outcome.ProjectionLagEvents
		if outcome.LatencyMilliseconds > metrics.MaxLatencyMilliseconds {
			metrics.MaxLatencyMilliseconds = outcome.LatencyMilliseconds
		}
		if outcome.ProjectionLagEvents > metrics.MaxProjectionLagEvents {
			metrics.MaxProjectionLagEvents = outcome.ProjectionLagEvents
		}
		if evaluationCase.ExpectedNoAccepted {
			metrics.ExpectedNoAcceptedCases++
			if len(outcome.AcceptedEntityIDs) == 0 {
				metrics.ExpectedNoAcceptedPass++
			}
			recordSafetyClass(&metrics, evaluationCase.SafetyClass, len(outcome.AcceptedEntityIDs) == 0)
			continue
		}
		relevant := stringSet(evaluationCase.RequiredEntityIDs)
		limit := min(k, len(outcome.AcceptedEntityIDs))
		hits := 0
		firstRank := 0
		dcg := 0.0
		for index := 0; index < limit; index++ {
			metrics.AcceptedEntityCount++
			if _, ok := relevant[outcome.AcceptedEntityIDs[index]]; !ok {
				continue
			}
			hits++
			metrics.ResolvedEntityCount++
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
	if metrics.AcceptedEntityCount == 0 {
		// No accepted entity cannot be an invalid resolution. Recall and
		// precision retain the availability signal for that degenerate case.
		metrics.EntityResolutionAccuracy = 1
	} else {
		metrics.EntityResolutionAccuracy = float64(metrics.ResolvedEntityCount) / float64(metrics.AcceptedEntityCount)
	}
	metrics.MeanLatencyMilliseconds = float64(totalLatency) / float64(metrics.Cases)
	metrics.MeanProjectionLagEvents = float64(totalProjectionLag) / float64(metrics.Cases)
	sort.Slice(latencies, func(left, right int) bool { return latencies[left] < latencies[right] })
	metrics.P95LatencyMilliseconds = latencies[percentileIndex(len(latencies), 0.95)]
	return metrics, nil
}

// CompareRequiredRankings scores the fixed lexical, dense, unweighted-RRF,
// and weighted-RRF baseline set against exactly one corpus. It rejects missing,
// duplicate, renamed, or unbound variants so a report cannot silently omit a
// weaker baseline. The returned order is stable and never depends on metrics.
func CompareRequiredRankings(cases []EvaluationCase, variants []EvaluationVariant, k int) ([]RankingComparison, error) {
	if len(variants) != len(RequiredRetrievalBaselines) {
		return nil, fmt.Errorf("retrieval comparison must contain every required baseline")
	}
	byName := make(map[string]EvaluationVariant, len(variants))
	for _, variant := range variants {
		if !evaluationContentHashPattern.MatchString(variant.PolicyHash) || variant.Name == "" || len(variant.Name) > 100 {
			return nil, fmt.Errorf("retrieval evaluation variant is invalid")
		}
		if _, exists := byName[variant.Name]; exists {
			return nil, fmt.Errorf("retrieval evaluation variants must be unique")
		}
		byName[variant.Name] = variant
	}
	result := make([]RankingComparison, 0, len(RequiredRetrievalBaselines))
	for _, name := range RequiredRetrievalBaselines {
		variant, found := byName[name]
		if !found {
			return nil, fmt.Errorf("required retrieval baseline %q is missing", name)
		}
		metrics, err := ScoreRankings(cases, variant.Outcomes, k)
		if err != nil {
			return nil, fmt.Errorf("score retrieval baseline %s: %w", name, err)
		}
		result = append(result, RankingComparison{Name: name, PolicyHash: variant.PolicyHash, Metrics: metrics})
	}
	return result, nil
}

func validateEvaluationCase(evaluationCase EvaluationCase) error {
	if evaluationCase.CaseID == "" || len(evaluationCase.CaseID) > 200 || !uniqueNonEmpty(evaluationCase.RequiredEntityIDs) || len(evaluationCase.RequiredEntityIDs) > 1_000 {
		return fmt.Errorf("retrieval evaluation case is invalid")
	}
	if evaluationCase.ExpectedNoAccepted == (len(evaluationCase.RequiredEntityIDs) != 0) {
		return fmt.Errorf("retrieval evaluation expectation is invalid")
	}
	if evaluationCase.ExpectedNoAccepted {
		if !validEvaluationSafetyClass(evaluationCase.SafetyClass) {
			return fmt.Errorf("retrieval evaluation safety class is invalid")
		}
	} else if evaluationCase.SafetyClass != "" {
		return fmt.Errorf("positive retrieval evaluation case cannot have a safety class")
	}
	return nil
}

func recordSafetyClass(metrics *RankingMetrics, safetyClass string, passed bool) {
	switch safetyClass {
	case EvaluationSafetyClassStale:
		metrics.StaleRejectionCases++
		if passed {
			metrics.StaleRejectionPass++
		}
	case EvaluationSafetyClassCrossTenant:
		metrics.CrossTenantRejectionCases++
		if passed {
			metrics.CrossTenantRejectionPass++
		}
	case EvaluationSafetyClassUnauthorized:
		metrics.UnauthorizedRejectionCases++
		if passed {
			metrics.UnauthorizedRejectionPass++
		}
	}
}

func validEvaluationSafetyClass(value string) bool {
	switch value {
	case EvaluationSafetyClassStale, EvaluationSafetyClassCrossTenant,
		EvaluationSafetyClassUnauthorized, EvaluationSafetyClassMalformed,
		EvaluationSafetyClassScopeBypass:
		return true
	default:
		return false
	}
}

func percentileIndex(length int, percentile float64) int {
	if length < 1 {
		return 0
	}
	index := int(math.Ceil(percentile*float64(length))) - 1
	return min(max(index, 0), length-1)
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
