package retrieval

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

// CanonicalCandidate is the authority-checked record returned by PostgreSQL.
// It intentionally excludes card text, embeddings, and Qdrant payload data.
type CanonicalCandidate struct {
	PointID              string
	CollectionGeneration string
	TenantID             string
	RepositoryID         string
	EntityID             string
	ArtifactFamily       string
	SourceCommit         *string
	SourceHash           string
	Status               string
	AuthorityClass       string
	Stale                bool
	Language             *string
	SymbolKind           *string
	RepositoryPath       *string
	ProofRefs            []string
}

// CanonicalResolver performs a database lookup for the candidate point ID.
// A result may be ambiguous when independent canonical rows disagree; that is
// fail-closed rather than resolved by vector score or payload ordering.
type CanonicalResolver interface {
	ResolveRetrievalPoint(context.Context, string) ([]CanonicalCandidate, error)
}

// ResolveRankedCandidates reauthorizes each ranked vector candidate against
// PostgreSQL before it can enter a context pack. It is deliberately usable for
// both fused Qdrant rankings and a canonical exact-lookup fallback.
func ResolveRankedCandidates(
	ctx context.Context,
	query contracts.RetrievalQuery,
	ranked []contracts.RetrievalCandidate,
	resolver CanonicalResolver,
) ([]contracts.RetrievalCandidate, []contracts.RetrievalRejection, []contracts.RetrievalAmbiguity, error) {
	if err := contracts.ValidateRetrievalQuery(query); err != nil {
		return nil, nil, nil, err
	}
	if resolver == nil {
		return nil, nil, nil, fmt.Errorf("canonical retrieval resolver is required")
	}
	if len(ranked) > query.Policy.DenseLimit+query.Policy.SparseLimit {
		return nil, nil, nil, fmt.Errorf("ranked candidate list exceeds retrieval policy")
	}
	accepted := make([]contracts.RetrievalCandidate, 0, min(len(ranked), query.Policy.Limit))
	rejections := make([]contracts.RetrievalRejection, 0, len(ranked))
	ambiguities := make([]contracts.RetrievalAmbiguity, 0, len(ranked))
	seenEntity := make(map[string]struct{}, len(ranked))
	for _, candidate := range ranked {
		if err := validateRankedCandidate(candidate); err != nil {
			rejections = append(rejections, contracts.RetrievalRejection{PointID: candidate.PointID, Reason: "malformed_candidate"})
			continue
		}
		resolved, err := resolver.ResolveRetrievalPoint(ctx, candidate.PointID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("resolve retrieval point %s: %w", candidate.PointID, err)
		}
		if len(resolved) == 0 {
			rejections = append(rejections, contracts.RetrievalRejection{PointID: candidate.PointID, Reason: "missing_canonical_entity"})
			continue
		}
		if len(resolved) != 1 {
			if alternatives := authorizedAlternatives(query, candidate.PointID, resolved); len(alternatives) > 1 {
				ambiguities = append(ambiguities, contracts.RetrievalAmbiguity{PointID: candidate.PointID, AlternativeEntityIDs: alternatives})
			}
			rejections = append(rejections, contracts.RetrievalRejection{PointID: candidate.PointID, Reason: "ambiguous_identity"})
			continue
		}
		canonical := resolved[0]
		if reason := authorizeCanonicalCandidate(query, candidate, canonical); reason != "" {
			rejections = append(rejections, contracts.RetrievalRejection{PointID: candidate.PointID, Reason: reason})
			continue
		}
		if _, exists := seenEntity[canonical.EntityID]; exists {
			rejections = append(rejections, contracts.RetrievalRejection{PointID: candidate.PointID, Reason: "duplicate_entity"})
			continue
		}
		seenEntity[canonical.EntityID] = struct{}{}
		accepted = append(accepted, contracts.RetrievalCandidate{
			PointID: candidate.PointID, EntityID: canonical.EntityID, ArtifactFamily: canonical.ArtifactFamily,
			SourceCommit: canonical.SourceCommit, SourceHash: canonical.SourceHash,
			AuthorityClass: canonical.AuthorityClass, RepositoryPath: canonical.RepositoryPath,
			DenseRank: candidate.DenseRank, SparseRank: candidate.SparseRank, FusedScore: candidate.FusedScore,
			ProofRefs: append([]string(nil), canonical.ProofRefs...),
		})
		if len(accepted) == query.Policy.Limit {
			break
		}
	}
	return accepted, rejections, ambiguities, nil
}

// authorizedAlternatives exposes only identities that independently satisfy
// every scope and lifecycle constraint. An ambiguity without two such choices
// remains a rejection only, avoiding disclosure from a malformed resolver.
func authorizedAlternatives(query contracts.RetrievalQuery, pointID string, candidates []CanonicalCandidate) []string {
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.PointID != pointID || candidate.TenantID != query.TenantID || candidate.RepositoryID != query.RepositoryID ||
			(query.ExpectedGeneration != nil && candidate.CollectionGeneration != *query.ExpectedGeneration) ||
			candidate.Status != "active" || candidate.Stale || candidate.SourceCommit == nil || *candidate.SourceCommit != query.Scope.SourceCommit ||
			!contains(query.Filters.ArtifactFamilies, candidate.ArtifactFamily) || !contains(query.Filters.AuthorityClasses, candidate.AuthorityClass) ||
			(len(query.Filters.Languages) > 0 && (candidate.Language == nil || !contains(query.Filters.Languages, *candidate.Language))) ||
			(len(query.Filters.SymbolKinds) > 0 && (candidate.SymbolKind == nil || !contains(query.Filters.SymbolKinds, *candidate.SymbolKind))) ||
			!pathInScope(candidate.RepositoryPath, query.Scope) || !contracts.IsRetrievalEntityID(candidate.EntityID) {
			continue
		}
		seen[candidate.EntityID] = struct{}{}
	}
	result := make([]string, 0, min(len(seen), 16))
	for entityID := range seen {
		result = append(result, entityID)
	}
	sort.Strings(result)
	if len(result) > 16 {
		return result[:16]
	}
	return result
}

func validateRankedCandidate(candidate contracts.RetrievalCandidate) error {
	// The contract has no exported candidate validator. A one-candidate result
	// invokes the same semantic checks without exposing unrelated result state.
	policy := contracts.RetrievalPolicy{Limit: 1, DenseLimit: 1, SparseLimit: 1, DenseWeight: 1, SparseWeight: 1, RRFK: 1}
	query := contracts.RetrievalQuery{
		RequestID: "retrieval_request_validation", SchemaVersion: contracts.RetrievalSchemaVersion,
		TenantID: "tenant_00000000-0000-4000-8000-000000000001", RepositoryID: "repo_00000000-0000-4000-8000-000000000002",
		Query: "validation", Scope: contracts.RetrievalScope{SourceCommit: strings.Repeat("a", 40), AllowedPaths: []string{"**"}},
		Filters: contracts.RetrievalFilters{ArtifactFamilies: []string{"symbol"}, AuthorityClasses: []string{"canonical"}}, Policy: policy,
	}
	policyHash, err := contracts.RetrievalPolicyHash(policy)
	if err != nil {
		return err
	}
	return contracts.ValidateRetrievalResult(query, contracts.RetrievalResult{
		RequestID: query.RequestID, SchemaVersion: contracts.RetrievalSchemaVersion, Status: "complete", ProjectionFreshness: "fresh",
		Accepted: []contracts.RetrievalCandidate{candidate}, Rejections: []contracts.RetrievalRejection{},
		Receipt: contracts.RetrievalReceipt{ResolvedCandidates: 1, PolicyHash: policyHash},
	})
}

func authorizeCanonicalCandidate(query contracts.RetrievalQuery, ranked contracts.RetrievalCandidate, canonical CanonicalCandidate) string {
	if canonical.PointID != ranked.PointID || canonical.EntityID != ranked.EntityID || canonical.SourceHash != ranked.SourceHash || canonical.ArtifactFamily != ranked.ArtifactFamily || canonical.AuthorityClass != ranked.AuthorityClass {
		return "source_hash_mismatch"
	}
	if canonical.TenantID != query.TenantID || canonical.RepositoryID != query.RepositoryID {
		return "unauthorized_scope"
	}
	if query.ExpectedGeneration != nil && canonical.CollectionGeneration != *query.ExpectedGeneration {
		return "stale_projection"
	}
	if canonical.Status != "active" {
		return "inactive_source"
	}
	if canonical.Stale {
		return "stale_projection"
	}
	if canonical.SourceCommit == nil || *canonical.SourceCommit != query.Scope.SourceCommit {
		return "source_commit_mismatch"
	}
	if !contains(query.Filters.ArtifactFamilies, canonical.ArtifactFamily) || !contains(query.Filters.AuthorityClasses, canonical.AuthorityClass) {
		return "unauthorized_scope"
	}
	if len(query.Filters.Languages) > 0 && (canonical.Language == nil || !contains(query.Filters.Languages, *canonical.Language)) {
		return "unauthorized_scope"
	}
	if len(query.Filters.SymbolKinds) > 0 && (canonical.SymbolKind == nil || !contains(query.Filters.SymbolKinds, *canonical.SymbolKind)) {
		return "unauthorized_scope"
	}
	if !pathInScope(canonical.RepositoryPath, query.Scope) {
		return "unauthorized_scope"
	}
	return ""
}

func pathInScope(repositoryPath *string, scope contracts.RetrievalScope) bool {
	if repositoryPath == nil {
		return contains(scope.AllowedPaths, "**") && !contains(scope.DeniedPaths, "**")
	}
	value := path.Clean(*repositoryPath)
	allowed := false
	for _, candidate := range scope.AllowedPaths {
		if pathMatchesScope(value, candidate) {
			allowed = true
			break
		}
	}
	if !allowed {
		return false
	}
	for _, candidate := range scope.DeniedPaths {
		if pathMatchesScope(value, candidate) {
			return false
		}
	}
	return true
}

func pathMatchesScope(repositoryPath, scope string) bool {
	if scope == "**" {
		return true
	}
	prefix := strings.TrimSuffix(scope, "/**")
	return repositoryPath == prefix || strings.HasPrefix(repositoryPath, prefix+"/")
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
