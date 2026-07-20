package retrieval

import (
	"context"
	"fmt"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

// EvaluationQueryCase is a private query template for one evaluation case.
// It intentionally has no expected entity IDs or safety labels; those stay in
// the separately access-controlled scoring corpus.
type EvaluationQueryCase struct {
	CaseID string
	Query  contracts.RetrievalQuery
}

// EvaluationCapturePolicy binds a required baseline name to its exact query
// policy. Its hash is derived during capture rather than trusted from input.
type EvaluationCapturePolicy struct {
	Name   string
	Policy contracts.RetrievalPolicy
}

// CaptureRequiredRankings executes every required baseline against the same
// private query templates. It produces only canonical accepted entity IDs and
// bounded scalar telemetry; it never receives scoring labels or writes a
// serving-policy decision.
func CaptureRequiredRankings(
	ctx context.Context,
	service QueryService,
	cases []EvaluationQueryCase,
	policies []EvaluationCapturePolicy,
) ([]EvaluationVariant, error) {
	if err := validateEvaluationQueryCases(cases); err != nil {
		return nil, err
	}
	ordered, err := orderedCapturePolicies(policies)
	if err != nil {
		return nil, err
	}
	result := make([]EvaluationVariant, 0, len(ordered))
	for _, capturePolicy := range ordered {
		policyHash, err := contracts.RetrievalPolicyHash(capturePolicy.Policy)
		if err != nil {
			return nil, fmt.Errorf("hash retrieval baseline %s: %w", capturePolicy.Name, err)
		}
		outcomes := make([]EvaluationOutcome, 0, len(cases))
		for _, evaluationCase := range cases {
			query := evaluationCase.Query
			query.Policy = capturePolicy.Policy
			if err := contracts.ValidateRetrievalQuery(query); err != nil {
				return nil, fmt.Errorf("validate evaluation query %s: %w", evaluationCase.CaseID, err)
			}
			started := time.Now()
			searchResult, err := service.Search(ctx, query)
			latency := time.Since(started).Milliseconds()
			if err != nil {
				return nil, fmt.Errorf("capture retrieval query %s for %s: %w", evaluationCase.CaseID, capturePolicy.Name, err)
			}
			if latency < 0 || latency > 30_000 {
				return nil, fmt.Errorf("capture retrieval query %s exceeded the bounded latency", evaluationCase.CaseID)
			}
			accepted := make([]string, 0, len(searchResult.Accepted))
			for _, candidate := range searchResult.Accepted {
				accepted = append(accepted, candidate.EntityID)
			}
			outcomes = append(outcomes, EvaluationOutcome{
				CaseID: evaluationCase.CaseID, AcceptedEntityIDs: accepted,
				LatencyMilliseconds: latency, ProjectionLagEvents: searchResult.ProjectionLagEvents,
			})
		}
		result = append(result, EvaluationVariant{
			Name: capturePolicy.Name, PolicyHash: policyHash, Outcomes: outcomes,
		})
	}
	return result, nil
}

func validateEvaluationQueryCases(cases []EvaluationQueryCase) error {
	if len(cases) < 1 || len(cases) > 1_000 {
		return fmt.Errorf("evaluation query case count is invalid")
	}
	seen := make(map[string]struct{}, len(cases))
	for _, evaluationCase := range cases {
		if evaluationCase.CaseID == "" || len(evaluationCase.CaseID) > 200 {
			return fmt.Errorf("evaluation query case ID is invalid")
		}
		if _, exists := seen[evaluationCase.CaseID]; exists {
			return fmt.Errorf("evaluation query case IDs must be unique")
		}
		seen[evaluationCase.CaseID] = struct{}{}
	}
	return nil
}

func orderedCapturePolicies(policies []EvaluationCapturePolicy) ([]EvaluationCapturePolicy, error) {
	if len(policies) != len(RequiredRetrievalBaselines) {
		return nil, fmt.Errorf("evaluation capture must contain every required baseline")
	}
	byName := make(map[string]EvaluationCapturePolicy, len(policies))
	for _, capturePolicy := range policies {
		if _, exists := byName[capturePolicy.Name]; exists || !validCaptureBaseline(capturePolicy.Name, capturePolicy.Policy) {
			return nil, fmt.Errorf("evaluation capture baseline is invalid")
		}
		byName[capturePolicy.Name] = capturePolicy
	}
	result := make([]EvaluationCapturePolicy, 0, len(RequiredRetrievalBaselines))
	for _, name := range RequiredRetrievalBaselines {
		capturePolicy, found := byName[name]
		if !found {
			return nil, fmt.Errorf("required retrieval baseline %q is missing", name)
		}
		result = append(result, capturePolicy)
	}
	return result, nil
}

func validCaptureBaseline(name string, policy contracts.RetrievalPolicy) bool {
	if _, err := contracts.RetrievalPolicyHash(policy); err != nil {
		return false
	}
	switch name {
	case "lexical_only":
		return policy.DenseWeight == 0 && policy.SparseWeight > 0
	case "dense_only":
		return policy.DenseWeight > 0 && policy.SparseWeight == 0
	case "rrf_unweighted":
		return policy.DenseWeight > 0 && policy.DenseWeight == policy.SparseWeight
	case "rrf_weighted":
		return policy.DenseWeight > 0 && policy.SparseWeight > 0 && policy.DenseWeight != policy.SparseWeight
	default:
		return false
	}
}
