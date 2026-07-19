package retrieval

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

func TestResolveRankedCandidatesReauthorizesEveryVectorResult(t *testing.T) {
	query := governedQuery()
	first := canonicalCandidate("retrieval_"+strings.Repeat("1", 64), "symbol_login", "sha256:"+strings.Repeat("a", 64), "internal/http/login.go")
	second := canonicalCandidate("retrieval_"+strings.Repeat("2", 64), "symbol_private", "sha256:"+strings.Repeat("b", 64), "internal/private/token.go")
	ranked := []contracts.RetrievalCandidate{
		rankedCandidate(first), rankedCandidate(second),
	}
	accepted, rejected, ambiguities, err := ResolveRankedCandidates(context.Background(), query, ranked, staticResolver{
		first.PointID: {first}, second.PointID: {second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted) != 1 || accepted[0].EntityID != first.EntityID {
		t.Fatalf("accepted=%#v", accepted)
	}
	if len(rejected) != 1 || rejected[0].Reason != "unauthorized_scope" || len(ambiguities) != 0 {
		t.Fatalf("rejected=%#v ambiguities=%#v", rejected, ambiguities)
	}
}

func TestResolveRankedCandidatesRejectsPayloadAndIdentityDrift(t *testing.T) {
	query := governedQuery()
	canonical := canonicalCandidate("retrieval_"+strings.Repeat("3", 64), "symbol_login", "sha256:"+strings.Repeat("c", 64), "internal/http/login.go")
	drifted := rankedCandidate(canonical)
	drifted.SourceHash = "sha256:" + strings.Repeat("d", 64)
	accepted, rejected, ambiguities, err := ResolveRankedCandidates(context.Background(), query, []contracts.RetrievalCandidate{drifted}, staticResolver{
		canonical.PointID: {canonical},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted) != 0 || len(rejected) != 1 || rejected[0].Reason != "source_hash_mismatch" || len(ambiguities) != 0 {
		t.Fatalf("accepted=%#v rejected=%#v ambiguities=%#v", accepted, rejected, ambiguities)
	}
}

func TestResolveRankedCandidatesExposesOnlyAuthorizedAmbiguousAlternatives(t *testing.T) {
	query := governedQuery()
	first := canonicalCandidate("retrieval_"+strings.Repeat("4", 64), "symbol_login", "sha256:"+strings.Repeat("e", 64), "internal/http/login.go")
	second := first
	second.EntityID = "symbol_login_v2"
	private := first
	private.EntityID = "symbol_private"
	private.RepositoryPath = stringPointer("internal/private/token.go")
	accepted, rejected, ambiguities, err := ResolveRankedCandidates(context.Background(), query, []contracts.RetrievalCandidate{rankedCandidate(first)}, staticResolver{
		first.PointID: {first, second, private},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted) != 0 || len(rejected) != 1 || rejected[0].Reason != "ambiguous_identity" || len(ambiguities) != 1 || !slices.Equal(ambiguities[0].AlternativeEntityIDs, []string{"symbol_login", "symbol_login_v2"}) {
		t.Fatalf("accepted=%#v rejected=%#v ambiguities=%#v", accepted, rejected, ambiguities)
	}
}

func stringPointer(value string) *string { return &value }

type staticResolver map[string][]CanonicalCandidate

func (resolver staticResolver) ResolveRetrievalPoint(_ context.Context, pointID string) ([]CanonicalCandidate, error) {
	return resolver[pointID], nil
}

func governedQuery() contracts.RetrievalQuery {
	return contracts.RetrievalQuery{
		RequestID: "retrieval_request_governed", SchemaVersion: contracts.RetrievalSchemaVersion,
		TenantID: retrievalTenantID, RepositoryID: retrievalRepositoryID, Query: "locate LoginHandler",
		Scope:   contracts.RetrievalScope{SourceCommit: retrievalCommit, AllowedPaths: []string{"internal/**"}, DeniedPaths: []string{"internal/private/**"}},
		Filters: contracts.RetrievalFilters{ArtifactFamilies: []string{"symbol"}, AuthorityClasses: []string{"canonical"}},
		Policy:  contracts.RetrievalPolicy{Limit: 2, DenseLimit: 3, SparseLimit: 3, DenseWeight: 1, SparseWeight: 1, RRFK: 60},
	}
}

func canonicalCandidate(pointID, entityID, sourceHash, repositoryPath string) CanonicalCandidate {
	commit := retrievalCommit
	return CanonicalCandidate{
		PointID: pointID, CollectionGeneration: contracts.RetrievalGenerationID("fixture", "v1", 3, SparseEncoderVersion),
		TenantID: retrievalTenantID, RepositoryID: retrievalRepositoryID, EntityID: entityID,
		ArtifactFamily: "symbol", SourceCommit: &commit, SourceHash: sourceHash, Status: "active",
		AuthorityClass: "canonical", RepositoryPath: &repositoryPath, ProofRefs: []string{"proof_a"},
	}
}

func rankedCandidate(candidate CanonicalCandidate) contracts.RetrievalCandidate {
	rank := 1
	return contracts.RetrievalCandidate{
		PointID: candidate.PointID, EntityID: candidate.EntityID, ArtifactFamily: candidate.ArtifactFamily,
		SourceCommit: candidate.SourceCommit, SourceHash: candidate.SourceHash, AuthorityClass: candidate.AuthorityClass,
		RepositoryPath: candidate.RepositoryPath, DenseRank: &rank, FusedScore: 0.5, ProofRefs: candidate.ProofRefs,
	}
}
