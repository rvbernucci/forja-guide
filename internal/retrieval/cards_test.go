package retrieval

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

const (
	retrievalTenantID     = "tenant_00010203-0405-4607-8809-0a0b0c0d0e0f"
	retrievalRepositoryID = "repo_11121314-1516-4718-891a-1b1c1d1e1f20"
	retrievalCommit       = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestBuildCardTextIsStableAndSorted(t *testing.T) {
	t.Parallel()
	first := validCardSource()
	first.ProofRefs = []string{"proof_b", "proof_a"}
	first.GraphNodeIDs = []string{"graph_b", "graph_a"}
	second := validCardSource()
	second.ProofRefs = []string{"proof_a", "proof_b"}
	second.GraphNodeIDs = []string{"graph_a", "graph_b"}
	left, err := BuildCardText(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := BuildCardText(second)
	if err != nil {
		t.Fatal(err)
	}
	if left != right || !strings.Contains(left, "proof_refs: proof_a,proof_b") || !strings.Contains(left, "content: line one line two") {
		t.Fatalf("card text is not canonical: %q", left)
	}
}

func TestBuildPointBindsEveryDerivedComponent(t *testing.T) {
	t.Parallel()
	embedder := fixtureEmbedder{}
	generation := contracts.RetrievalGenerationID("fixture", "v1", 3, SparseEncoderVersion)
	point, err := BuildPoint(context.Background(), validCardSource(), generation, embedder, HashingSparseEncoder{})
	if err != nil {
		t.Fatal(err)
	}
	if err := contracts.ValidateRetrievalPoint(point); err != nil {
		t.Fatal(err)
	}
	if point.PointID != contracts.RetrievalPointID(generation, point.EntityID, point.SourceHash) {
		t.Fatal("point ID is not deterministic")
	}
	if point.Embedding.SparseEncoderVersion != SparseEncoderVersion || len(point.Sparse.Indices) == 0 {
		t.Fatalf("point does not bind sparse encoding: %#v", point)
	}

	bad := fixtureEmbedder{descriptor: contracts.EmbeddingDescriptor{
		Model: "fixture", Version: "v1", Dimensions: 3, SparseEncoderVersion: "wrong", EmbeddedAt: time.Date(2026, 7, 19, 15, 0, 0, 0, time.UTC),
	}}
	if _, err := BuildPoint(context.Background(), validCardSource(), generation, bad, HashingSparseEncoder{}); err == nil {
		t.Fatal("sparse descriptor mismatch accepted")
	}
}

func TestBuildTestSourceRequiresCanonicalTestAndUsesTestFamily(t *testing.T) {
	t.Parallel()
	bundle := projectionFixture(t)
	file := bundle.Files[0]
	symbol := bundle.Symbols[0]
	if _, err := BuildTestSource(bundle.Snapshot, file, symbol, "canonical", nil); err == nil {
		t.Fatal("non-test symbol was accepted as a test card")
	}
	symbol.Test = true
	symbol.SymbolID = contracts.ComputeSymbolID(symbol)
	symbol.LineageID = contracts.ComputeSymbolLineageID(symbol)
	source, err := BuildTestSource(bundle.Snapshot, file, symbol, "canonical", nil)
	if err != nil || source.ArtifactFamily != "test" || source.EntityID != symbol.SymbolID {
		t.Fatalf("source=%#v err=%v", source, err)
	}
}

func TestBuildDecisionSourceRequiresResolvedCanonicalDecisionAndIsStable(t *testing.T) {
	t.Parallel()
	decidedAt := time.Date(2026, 7, 19, 17, 0, 0, 0, time.UTC)
	decidedBy := "operator"
	reason := "The bounded change is ready."
	decision := contracts.Decision{
		DecisionID: "decision_11111111-2222-4333-8444-555555555555", SchemaVersion: "1.0",
		SprintID: "sprint_11111111-2222-4333-8444-555555555555", RunID: "run_fixture",
		Action: "submit_sprint", RiskClass: "medium", Status: "approved", Version: 2,
		RequestedBy: "planner", DecidedBy: &decidedBy, Reason: &reason, DecidedAt: &decidedAt,
	}
	first, err := BuildDecisionSource(retrievalTenantID, retrievalRepositoryID, decision)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildDecisionSource(retrievalTenantID, retrievalRepositoryID, decision)
	if err != nil || first.SourceHash != second.SourceHash || first.ArtifactFamily != "decision" || first.SourceCommit != nil {
		t.Fatalf("first=%#v second=%#v err=%v", first, second, err)
	}
	if _, err := BuildCardText(first); err != nil {
		t.Fatal(err)
	}
	decision.Status = "pending"
	if _, err := BuildDecisionSource(retrievalTenantID, retrievalRepositoryID, decision); err == nil {
		t.Fatal("pending decision was accepted for retrieval")
	}
}

func TestHashingSparseEncoderIsStableAndNormalized(t *testing.T) {
	t.Parallel()
	encoder := HashingSparseEncoder{}
	left, err := encoder.Encode("forjaIndex handles retrieval-point and retrieval_point")
	if err != nil {
		t.Fatal(err)
	}
	right, err := encoder.Encode("forjaIndex handles retrieval-point and retrieval_point")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("sparse encoder is not deterministic: %#v / %#v", left, right)
	}
	var norm float64
	for index, value := range left.Values {
		norm += value * value
		if index > 0 && left.Indices[index-1] >= left.Indices[index] {
			t.Fatal("sparse indices are not strictly ascending")
		}
	}
	if norm < 0.999999 || norm > 1.000001 {
		t.Fatalf("sparse L2 norm = %f, want 1", norm)
	}
	if _, err := encoder.Encode("_"); err == nil {
		t.Fatal("content-free sparse source accepted")
	}
}

func validCardSource() CardSource {
	commit := retrievalCommit
	path := "internal/retrieval/cards.go"
	language := "go"
	kind := "function"
	return CardSource{
		TenantID: retrievalTenantID, RepositoryID: retrievalRepositoryID,
		EntityID: "symbol_fixture", ArtifactFamily: "symbol", SourceCommit: &commit,
		SourceHash: "sha256:" + strings.Repeat("a", 64), AuthorityClass: "canonical", Status: "active",
		Language: &language, SymbolKind: &kind, RepositoryPath: &path,
		Title: "Forja Index", Body: "line one\n line two", ProofRefs: []string{"proof_a"}, GraphNodeIDs: []string{},
	}
}

type fixtureEmbedder struct {
	descriptor contracts.EmbeddingDescriptor
}

func (f fixtureEmbedder) Descriptor() contracts.EmbeddingDescriptor {
	if f.descriptor.Model == "" {
		return contracts.EmbeddingDescriptor{
			Model: "fixture", Version: "v1", Dimensions: 3, SparseEncoderVersion: SparseEncoderVersion,
			EmbeddedAt: time.Date(2026, 7, 19, 15, 0, 0, 0, time.UTC),
		}
	}
	return f.descriptor
}

func (fixtureEmbedder) Embed(_ context.Context, _ string) ([]float64, error) {
	return []float64{0.1, 0.2, 0.3}, nil
}
