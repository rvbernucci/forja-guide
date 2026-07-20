package contracts

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

const (
	retrievalTestTenant     = "tenant_00010203-0405-4607-8809-0a0b0c0d0e0f"
	retrievalTestRepository = "repo_11121314-1516-4718-891a-1b1c1d1e1f20"
	retrievalTestCommit     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestValidateRetrievalPointAndSchema(t *testing.T) {
	t.Parallel()
	point := validRetrievalPoint(t, "symbol_alpha", "sha256:"+strings.Repeat("a", 64), "alpha")
	if err := ValidateRetrievalPoint(point); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(point)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateJSON("retrieval-point.schema.json", encoded); err != nil {
		t.Fatalf("valid retrieval point rejected by schema: %v", err)
	}
}

func TestValidateRetrievalPointRejectsProviderAndIdentityDrift(t *testing.T) {
	t.Parallel()
	base := validRetrievalPoint(t, "symbol_alpha", "sha256:"+strings.Repeat("a", 64), "alpha")
	for name, mutate := range map[string]func(*RetrievalPoint){
		"dimension mismatch":      func(point *RetrievalPoint) { point.Dense = []float64{0.1} },
		"nonfinite dense value":   func(point *RetrievalPoint) { point.Dense[1] = math.NaN() },
		"unordered sparse":        func(point *RetrievalPoint) { point.Sparse.Indices[1] = point.Sparse.Indices[0] },
		"card hash mismatch":      func(point *RetrievalPoint) { point.CardText = "drift" },
		"point identity mismatch": func(point *RetrievalPoint) { point.PointID = StableRetrievalID("retrieval", "wrong") },
		"invalid repository":      func(point *RetrievalPoint) { point.RepositoryID = "repo_wrong" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := cloneRetrievalPoint(base)
			mutate(&copy)
			if err := ValidateRetrievalPoint(copy); err == nil {
				t.Fatal("expected invalid retrieval point")
			}
		})
	}
}

func TestRetrievalCommitAndScopeSemanticsAreFamilyBound(t *testing.T) {
	sourceBound := validRetrievalPoint(t, "symbol_alpha", "sha256:"+strings.Repeat("a", 64), "alpha")
	sourceBound.SourceCommit = nil
	if err := ValidateRetrievalPoint(sourceBound); err == nil {
		t.Fatal("source-bound point accepted without source commit")
	}

	global := validRetrievalPoint(t, "decision_global", "sha256:"+strings.Repeat("b", 64), "decision")
	global.ArtifactFamily = "decision"
	global.SourceCommit = nil
	global.PointID = RetrievalPointID(global.CollectionGeneration, global.EntityID, global.SourceHash)
	if err := ValidateRetrievalPoint(global); err != nil {
		t.Fatalf("repository-global point rejected: %v", err)
	}
	commit := retrievalTestCommit
	global.SourceCommit = &commit
	if err := ValidateRetrievalPoint(global); err == nil {
		t.Fatal("repository-global point accepted with a source commit")
	}

	query := validRetrievalQuery()
	query.Filters.ArtifactFamilies = []string{"incident"}
	if err := ValidateRetrievalQuery(query); err == nil {
		t.Fatal("repository-global query accepted a narrow path scope")
	}
	query.Scope.AllowedPaths = []string{"**"}
	if err := ValidateRetrievalQuery(query); err != nil {
		t.Fatalf("repository-wide global query rejected: %v", err)
	}
}

func TestValidateRetrievalQueryAndResult(t *testing.T) {
	t.Parallel()
	query := validRetrievalQuery()
	if err := ValidateRetrievalQuery(query); err != nil {
		t.Fatal(err)
	}
	first := validRetrievalPoint(t, "symbol_alpha", "sha256:"+strings.Repeat("a", 64), "alpha")
	second := validRetrievalPoint(t, "symbol_beta", "sha256:"+strings.Repeat("b", 64), "beta")
	resultCandidates, err := FuseRetrievalRanks([]RetrievalPoint{first, second}, []RetrievalPoint{second, first}, query.Policy)
	if err != nil {
		t.Fatal(err)
	}
	policyHash, err := RetrievalPolicyHash(query.Policy)
	if err != nil {
		t.Fatal(err)
	}
	generation := first.CollectionGeneration
	result := RetrievalResult{
		RequestID: query.RequestID, SchemaVersion: RetrievalSchemaVersion,
		Status: "complete", ProjectionFreshness: "fresh", CollectionGeneration: &generation,
		Accepted:   resultCandidates,
		Rejections: []RetrievalRejection{},
		Receipt: RetrievalReceipt{
			DenseCandidates: len([]RetrievalPoint{first, second}), SparseCandidates: len([]RetrievalPoint{second, first}),
			FusedCandidates: len(resultCandidates), ResolvedCandidates: len(resultCandidates),
			RejectedCandidates: 0, PolicyHash: policyHash,
		},
	}
	if err := ValidateRetrievalResult(query, result); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	encodedQuery, err := json.Marshal(query)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateJSON("retrieval-query.schema.json", encodedQuery); err != nil {
		t.Fatalf("valid query rejected by schema: %v", err)
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateJSON("retrieval-result.schema.json", encodedResult); err != nil {
		t.Fatalf("valid result rejected by schema: %v", err)
	}
}

func TestValidateRetrievalQueryFailsClosed(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*RetrievalQuery){
		"missing allowed paths": func(query *RetrievalQuery) { query.Scope.AllowedPaths = nil },
		"path traversal":        func(query *RetrievalQuery) { query.Scope.AllowedPaths = []string{"../private"} },
		"unbounded fusion":      func(query *RetrievalQuery) { query.Policy.DenseLimit = 201 },
		"both rank paths disabled": func(query *RetrievalQuery) {
			query.Policy.DenseWeight = 0
			query.Policy.SparseWeight = 0
		},
		"invalid tenant": func(query *RetrievalQuery) { query.TenantID = "tenant_wrong" },
	} {
		t.Run(name, func(t *testing.T) {
			query := validRetrievalQuery()
			mutate(&query)
			if err := ValidateRetrievalQuery(query); err == nil {
				t.Fatal("expected query validation failure")
			}
		})
	}
}

func TestValidateRetrievalQueryAllowsOneExplicitBaselineRankPath(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RetrievalQuery){
		"lexical only": func(query *RetrievalQuery) { query.Policy.DenseWeight = 0 },
		"dense only":   func(query *RetrievalQuery) { query.Policy.SparseWeight = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			query := validRetrievalQuery()
			mutate(&query)
			if err := ValidateRetrievalQuery(query); err != nil {
				t.Fatalf("baseline query rejected: %v", err)
			}
			encoded, err := json.Marshal(query)
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.ValidateJSON("retrieval-query.schema.json", encoded); err != nil {
				t.Fatalf("baseline query rejected by schema: %v", err)
			}
		})
	}
}

func TestValidateRetrievalResultAllowsOnlyBoundedSortedAmbiguities(t *testing.T) {
	query := validRetrievalQuery()
	point := validRetrievalPoint(t, "symbol_alpha", "sha256:"+strings.Repeat("a", 64), "alpha")
	policyHash, err := RetrievalPolicyHash(query.Policy)
	if err != nil {
		t.Fatal(err)
	}
	result := RetrievalResult{
		RequestID: query.RequestID, SchemaVersion: RetrievalSchemaVersion, Status: "complete", ProjectionFreshness: "fresh",
		Accepted: []RetrievalCandidate{}, Rejections: []RetrievalRejection{{PointID: point.PointID, Reason: "ambiguous_identity"}},
		Ambiguities: []RetrievalAmbiguity{{PointID: point.PointID, AlternativeEntityIDs: []string{"symbol_alpha", "symbol_beta"}}},
		Receipt:     RetrievalReceipt{RejectedCandidates: 1, PolicyHash: policyHash},
	}
	if err := ValidateRetrievalResult(query, result); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateJSON("retrieval-result.schema.json", encoded); err != nil {
		t.Fatalf("valid ambiguity result rejected by schema: %v", err)
	}
	result.Ambiguities[0].AlternativeEntityIDs = []string{"symbol_beta", "symbol_alpha"}
	if err := ValidateRetrievalResult(query, result); err == nil {
		t.Fatal("unsorted ambiguity alternatives accepted")
	}
	result.Ambiguities[0].AlternativeEntityIDs = []string{"symbol_alpha", "symbol_beta"}
	result.Rejections = nil
	result.Receipt.RejectedCandidates = 0
	if err := ValidateRetrievalResult(query, result); err == nil {
		t.Fatal("ambiguity without an explicit rejection accepted")
	}
}

func TestValidateRetrievalResultBindsFreshnessToProjectionLag(t *testing.T) {
	t.Parallel()
	query := validRetrievalQuery()
	policyHash, err := RetrievalPolicyHash(query.Policy)
	if err != nil {
		t.Fatal(err)
	}
	result := RetrievalResult{
		RequestID: query.RequestID, SchemaVersion: RetrievalSchemaVersion,
		Status: "complete", ProjectionFreshness: "fresh", ProjectionLagEvents: 1,
		Accepted: []RetrievalCandidate{}, Rejections: []RetrievalRejection{},
		Receipt: RetrievalReceipt{PolicyHash: policyHash},
	}
	if err := ValidateRetrievalResult(query, result); err == nil {
		t.Fatal("fresh result with projection lag accepted")
	}
	result.Status = "degraded"
	result.ProjectionFreshness = "stale"
	result.ProjectionLagEvents = 0
	if err := ValidateRetrievalResult(query, result); err == nil {
		t.Fatal("stale result without projection lag accepted")
	}
	result.ProjectionLagEvents = 1
	if err := ValidateRetrievalResult(query, result); err != nil {
		t.Fatalf("stale result with projection lag rejected: %v", err)
	}
}

func TestFuseRetrievalRanksIsWeightedAndStable(t *testing.T) {
	t.Parallel()
	policy := validRetrievalQuery().Policy
	alpha := validRetrievalPoint(t, "symbol_alpha", "sha256:"+strings.Repeat("a", 64), "alpha")
	beta := validRetrievalPoint(t, "symbol_beta", "sha256:"+strings.Repeat("b", 64), "beta")
	gamma := validRetrievalPoint(t, "symbol_gamma", "sha256:"+strings.Repeat("c", 64), "gamma")
	actual, err := FuseRetrievalRanks([]RetrievalPoint{alpha, beta}, []RetrievalPoint{beta, gamma}, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(actual) != 3 || actual[0].EntityID != beta.EntityID {
		t.Fatalf("fused order = %#v, want beta first", actual)
	}
	if actual[0].DenseRank == nil || actual[0].SparseRank == nil || actual[0].FusedScore <= actual[1].FusedScore {
		t.Fatalf("fused beta lacks combined rank advantage: %#v", actual[0])
	}
	if _, err := FuseRetrievalRanks([]RetrievalPoint{alpha, alpha}, nil, policy); err == nil {
		t.Fatal("duplicate rank-list point accepted")
	}
}

func TestFuseRetrievalRanksExcludesDisabledPath(t *testing.T) {
	t.Parallel()
	policy := validRetrievalQuery().Policy
	policy.DenseWeight = 0
	alpha := validRetrievalPoint(t, "symbol_alpha", "sha256:"+strings.Repeat("a", 64), "alpha")
	beta := validRetrievalPoint(t, "symbol_beta", "sha256:"+strings.Repeat("b", 64), "beta")
	actual, err := FuseRetrievalRanks([]RetrievalPoint{alpha}, []RetrievalPoint{beta}, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(actual) != 1 || actual[0].EntityID != beta.EntityID || actual[0].DenseRank != nil || actual[0].SparseRank == nil || actual[0].FusedScore <= 0 {
		t.Fatalf("disabled dense path contributed to fused result: %#v", actual)
	}
}

func validRetrievalPoint(t *testing.T, entityID, sourceHash, text string) RetrievalPoint {
	t.Helper()
	generation := RetrievalGenerationID("fixture-dense", "2026-07", 3, "sparse-fixture-v1")
	commit := retrievalTestCommit
	path := "internal/example.go"
	point := RetrievalPoint{
		SchemaVersion: RetrievalSchemaVersion, CollectionGeneration: generation,
		TenantID: retrievalTestTenant, RepositoryID: retrievalTestRepository,
		EntityID: entityID, ArtifactFamily: "symbol", SourceCommit: &commit,
		SourceHash: sourceHash, CardText: text, CardTextHash: CardTextHash(text),
		Status: "active", AuthorityClass: "canonical", RepositoryPath: &path,
		ProofRefs: []string{"artifact_fixture"}, GraphNodeIDs: []string{},
		Dense: []float64{0.1, 0.2, 0.3}, Sparse: SparseVector{Indices: []uint32{1, 3}, Values: []float64{0.4, 0.6}},
		Embedding: EmbeddingDescriptor{
			Model: "fixture-dense", Version: "2026-07", Dimensions: 3,
			SparseEncoderVersion: "sparse-fixture-v1", EmbeddedAt: time.Date(2026, 7, 19, 15, 0, 0, 0, time.UTC),
		},
	}
	point.PointID = RetrievalPointID(point.CollectionGeneration, point.EntityID, point.SourceHash)
	return point
}

func validRetrievalQuery() RetrievalQuery {
	return RetrievalQuery{
		RequestID: "retrieval_request_fixture", SchemaVersion: RetrievalSchemaVersion,
		TenantID: retrievalTestTenant, RepositoryID: retrievalTestRepository, Query: "find fixture symbol",
		Scope:   RetrievalScope{SourceCommit: retrievalTestCommit, AllowedPaths: []string{"internal"}},
		Filters: RetrievalFilters{ArtifactFamilies: []string{"symbol"}, AuthorityClasses: []string{"canonical"}},
		Policy:  RetrievalPolicy{Limit: 10, DenseLimit: 20, SparseLimit: 20, DenseWeight: 1, SparseWeight: 1, RRFK: 60},
	}
}

func cloneRetrievalPoint(point RetrievalPoint) RetrievalPoint {
	copy := point
	copy.Dense = append([]float64(nil), point.Dense...)
	copy.Sparse = SparseVector{Indices: append([]uint32(nil), point.Sparse.Indices...), Values: append([]float64(nil), point.Sparse.Values...)}
	copy.ProofRefs = append([]string(nil), point.ProofRefs...)
	copy.GraphNodeIDs = append([]string(nil), point.GraphNodeIDs...)
	return copy
}
