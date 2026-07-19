package retrieval

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	qdrant "github.com/qdrant/go-client/qdrant"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

func TestQueryServiceFusesUntrustedRanksThenResolvesCanonicalIdentity(t *testing.T) {
	t.Parallel()
	point, err := BuildPoint(t.Context(), validCardSource(), contracts.RetrievalGenerationID("fixture", "v1", 3, SparseEncoderVersion), fixtureEmbedder{}, HashingSparseEncoder{})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := QdrantPoint(point)
	if err != nil {
		t.Fatal(err)
	}
	client := &recordingQdrantQueryClient{dense: []*qdrant.ScoredPoint{{Id: wire.Id, Payload: wire.Payload, Score: 0.9}}, sparse: []*qdrant.ScoredPoint{{Id: wire.Id, Payload: wire.Payload, Score: 0.8}}}
	canonical := CanonicalCandidate{
		PointID: point.PointID, CollectionGeneration: point.CollectionGeneration, TenantID: point.TenantID,
		RepositoryID: point.RepositoryID, EntityID: point.EntityID, ArtifactFamily: point.ArtifactFamily,
		SourceCommit: point.SourceCommit, SourceHash: point.SourceHash, Status: "active",
		AuthorityClass: point.AuthorityClass, RepositoryPath: point.RepositoryPath, ProofRefs: point.ProofRefs,
	}
	query := governedQuery()
	generation := point.CollectionGeneration
	query.ExpectedGeneration = &generation
	service := QueryService{Client: client, CollectionName: "forja_retrieval_v1", Embedder: fixtureEmbedder{}, Sparse: HashingSparseEncoder{}, Resolver: staticResolver{point.PointID: {canonical}}}
	result, err := service.Search(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "complete" || len(result.Accepted) != 1 || result.Accepted[0].PointID != point.PointID || result.Receipt.DenseCandidates != 1 || result.Receipt.SparseCandidates != 1 {
		t.Fatalf("result=%#v", result)
	}
	if len(client.requests) != 2 || client.requests[0].GetUsing() != DenseVectorName || client.requests[1].GetUsing() != SparseVectorName || client.requests[0].GetFilter() == nil || client.requests[1].GetFilter() == nil {
		t.Fatalf("requests=%#v", client.requests)
	}
}

func TestQueryServiceDegradesWhenQdrantIsUnavailable(t *testing.T) {
	t.Parallel()
	query := governedQuery()
	generation := contracts.RetrievalGenerationID("fixture", "v1", 3, SparseEncoderVersion)
	query.ExpectedGeneration = &generation
	service := QueryService{Client: &recordingQdrantQueryClient{err: errors.New("unavailable")}, CollectionName: "forja_retrieval_v1", Embedder: fixtureEmbedder{}, Sparse: HashingSparseEncoder{}, Resolver: staticResolver{}}
	result, err := service.Search(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "degraded" || result.ProjectionFreshness != "unknown" || len(result.Accepted) != 0 || len(result.Gaps) != 1 || result.Gaps[0] != "qdrant_dense_unavailable" {
		t.Fatalf("result=%#v", result)
	}
	if err := contracts.ValidateRetrievalResult(query, result); err != nil {
		t.Fatalf("degraded result is invalid: %v", err)
	}
}

func TestQueryServiceBoundsAStalledQdrantCall(t *testing.T) {
	t.Parallel()
	query := governedQuery()
	generation := contracts.RetrievalGenerationID("fixture", "v1", 3, SparseEncoderVersion)
	query.ExpectedGeneration = &generation
	service := QueryService{Client: blockingQdrantQueryClient{}, CollectionName: "forja_retrieval_v1", Embedder: fixtureEmbedder{}, Sparse: HashingSparseEncoder{}, Resolver: staticResolver{}, QueryTimeout: time.Millisecond}
	started := time.Now()
	result, err := service.Search(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "degraded" || len(result.Gaps) != 1 || result.Gaps[0] != "qdrant_dense_unavailable" || time.Since(started) > time.Second {
		t.Fatalf("result=%#v elapsed=%s", result, time.Since(started))
	}
}

func TestQueryServiceRejectsPayloadIdentityDrift(t *testing.T) {
	t.Parallel()
	point, err := BuildPoint(t.Context(), validCardSource(), contracts.RetrievalGenerationID("fixture", "v1", 3, SparseEncoderVersion), fixtureEmbedder{}, HashingSparseEncoder{})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := QdrantPoint(point)
	if err != nil {
		t.Fatal(err)
	}
	// A syntactically valid but drifted hash must be rejected by canonical
	// resolution rather than used as retrieval context.
	wire.Payload["source_hash"] = qdrant.NewValueString("sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	canonical := CanonicalCandidate{PointID: point.PointID, CollectionGeneration: point.CollectionGeneration, TenantID: point.TenantID, RepositoryID: point.RepositoryID, EntityID: point.EntityID, ArtifactFamily: point.ArtifactFamily, SourceCommit: point.SourceCommit, SourceHash: point.SourceHash, Status: "active", AuthorityClass: point.AuthorityClass, RepositoryPath: point.RepositoryPath, ProofRefs: point.ProofRefs}
	query := governedQuery()
	generation := point.CollectionGeneration
	query.ExpectedGeneration = &generation
	service := QueryService{Client: &recordingQdrantQueryClient{dense: []*qdrant.ScoredPoint{{Payload: wire.Payload}}, sparse: []*qdrant.ScoredPoint{}}, CollectionName: "forja_retrieval_v1", Embedder: fixtureEmbedder{}, Sparse: HashingSparseEncoder{}, Resolver: staticResolver{point.PointID: {canonical}}}
	result, err := service.Search(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accepted) != 0 || len(result.Rejections) != 1 || result.Rejections[0].Reason != "source_hash_mismatch" {
		t.Fatalf("result=%#v", result)
	}
}

func TestQueryServiceAcceptsRepositoryGlobalDecisionOnlyWithRepositoryScope(t *testing.T) {
	t.Parallel()
	source := CardSource{
		TenantID: retrievalTenantID, RepositoryID: retrievalRepositoryID,
		EntityID: "decision_global", ArtifactFamily: "decision",
		SourceHash: "sha256:" + strings.Repeat("c", 64), AuthorityClass: "canonical", Status: "active",
		Title: "decision: publish", Body: "status: approved", ProofRefs: []string{"decision:decision_global"}, GraphNodeIDs: []string{},
	}
	point, err := BuildPoint(t.Context(), source, contracts.RetrievalGenerationID("fixture", "v1", 3, SparseEncoderVersion), fixtureEmbedder{}, HashingSparseEncoder{})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := QdrantPoint(point)
	if err != nil {
		t.Fatal(err)
	}
	canonical := CanonicalCandidate{
		PointID: point.PointID, CollectionGeneration: point.CollectionGeneration, TenantID: point.TenantID, RepositoryID: point.RepositoryID,
		EntityID: point.EntityID, ArtifactFamily: point.ArtifactFamily, SourceHash: point.SourceHash, Status: "active", AuthorityClass: point.AuthorityClass,
		ProofRefs: point.ProofRefs,
	}
	query := governedQuery()
	query.Scope.AllowedPaths = []string{"**"}
	query.Scope.DeniedPaths = nil
	query.Filters.ArtifactFamilies = []string{"decision"}
	generation := point.CollectionGeneration
	query.ExpectedGeneration = &generation
	service := QueryService{
		Client:         &recordingQdrantQueryClient{dense: []*qdrant.ScoredPoint{{Payload: wire.Payload}}, sparse: []*qdrant.ScoredPoint{{Payload: wire.Payload}}},
		CollectionName: "forja_retrieval_v1", Embedder: fixtureEmbedder{}, Sparse: HashingSparseEncoder{}, Resolver: staticResolver{point.PointID: {canonical}},
	}
	result, err := service.Search(t.Context(), query)
	if err != nil || result.Status != "complete" || len(result.Accepted) != 1 || result.Accepted[0].EntityID != point.EntityID || result.Accepted[0].SourceCommit != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

type recordingQdrantQueryClient struct {
	dense    []*qdrant.ScoredPoint
	sparse   []*qdrant.ScoredPoint
	err      error
	requests []*qdrant.QueryPoints
}

type blockingQdrantQueryClient struct{}

func (blockingQdrantQueryClient) Query(ctx context.Context, _ *qdrant.QueryPoints) ([]*qdrant.ScoredPoint, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (client *recordingQdrantQueryClient) Query(_ context.Context, request *qdrant.QueryPoints) ([]*qdrant.ScoredPoint, error) {
	client.requests = append(client.requests, request)
	if client.err != nil {
		return nil, client.err
	}
	if request.GetUsing() == DenseVectorName {
		return client.dense, nil
	}
	return client.sparse, nil
}
