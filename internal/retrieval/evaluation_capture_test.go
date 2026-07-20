package retrieval

import (
	"strings"
	"testing"

	qdrant "github.com/qdrant/go-client/qdrant"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

func TestCaptureRequiredRankingsRunsEveryBaselineWithoutLabels(t *testing.T) {
	t.Parallel()
	point, err := BuildPoint(t.Context(), validCardSource(), contracts.RetrievalGenerationID("fixture", "v1", 3, SparseEncoderVersion), fixtureEmbedder{}, HashingSparseEncoder{})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := QdrantPoint(point)
	if err != nil {
		t.Fatal(err)
	}
	canonical := CanonicalCandidate{
		PointID: point.PointID, CollectionGeneration: point.CollectionGeneration,
		TenantID: point.TenantID, RepositoryID: point.RepositoryID, EntityID: point.EntityID,
		ArtifactFamily: point.ArtifactFamily, SourceCommit: point.SourceCommit, SourceHash: point.SourceHash,
		Status: point.Status, AuthorityClass: point.AuthorityClass, RepositoryPath: point.RepositoryPath,
		ProofRefs: point.ProofRefs,
	}
	client := &recordingQdrantQueryClient{
		dense:  []*qdrant.ScoredPoint{{Payload: wire.Payload}},
		sparse: []*qdrant.ScoredPoint{{Payload: wire.Payload}},
	}
	query := governedQuery()
	generation := point.CollectionGeneration
	query.ExpectedGeneration = &generation
	service := QueryService{
		Client: client, CollectionName: "forja_retrieval_v1", Embedder: fixtureEmbedder{}, Sparse: HashingSparseEncoder{},
		Resolver: staticResolver{point.PointID: {canonical}}, Freshness: staticProjectionFreshness{},
	}
	variants, err := CaptureRequiredRankings(t.Context(), service,
		[]EvaluationQueryCase{{CaseID: "private_case", Query: query}}, capturePolicies(query.Policy))
	if err != nil {
		t.Fatal(err)
	}
	if len(variants) != 4 || variants[0].Name != "lexical_only" || variants[1].Name != "dense_only" ||
		variants[2].Name != "rrf_unweighted" || variants[3].Name != "rrf_weighted" {
		t.Fatalf("variants=%#v", variants)
	}
	for _, variant := range variants {
		if !strings.HasPrefix(variant.PolicyHash, "sha256:") || len(variant.Outcomes) != 1 ||
			variant.Outcomes[0].CaseID != "private_case" || len(variant.Outcomes[0].AcceptedEntityIDs) != 1 ||
			variant.Outcomes[0].AcceptedEntityIDs[0] != point.EntityID || variant.Outcomes[0].ProjectionLagEvents != 0 {
			t.Fatalf("variant=%#v", variant)
		}
	}
	// lexical + dense use one rank path each; both RRF variants use two.
	if len(client.requests) != 6 {
		t.Fatalf("query count=%d requests=%#v", len(client.requests), client.requests)
	}
}

func TestCaptureRequiredRankingsRejectsIncompleteOrMisnamedBaselines(t *testing.T) {
	t.Parallel()
	query := governedQuery()
	service := QueryService{}
	cases := []EvaluationQueryCase{{CaseID: "case", Query: query}}
	if _, err := CaptureRequiredRankings(t.Context(), service, cases, capturePolicies(query.Policy)[:3]); err == nil {
		t.Fatal("incomplete baseline set accepted")
	}
	policies := capturePolicies(query.Policy)
	policies[3].Policy.DenseWeight = 1
	policies[3].Policy.SparseWeight = 1
	if _, err := CaptureRequiredRankings(t.Context(), service, cases, policies); err == nil {
		t.Fatal("weighted baseline with equal weights accepted")
	}
}

func capturePolicies(base contracts.RetrievalPolicy) []EvaluationCapturePolicy {
	lexical := base
	lexical.DenseWeight = 0
	lexical.SparseWeight = 1
	dense := base
	dense.DenseWeight = 1
	dense.SparseWeight = 0
	unweighted := base
	unweighted.DenseWeight = 1
	unweighted.SparseWeight = 1
	weighted := base
	weighted.DenseWeight = 2
	weighted.SparseWeight = 1
	return []EvaluationCapturePolicy{
		{Name: "lexical_only", Policy: lexical},
		{Name: "dense_only", Policy: dense},
		{Name: "rrf_unweighted", Policy: unweighted},
		{Name: "rrf_weighted", Policy: weighted},
	}
}
