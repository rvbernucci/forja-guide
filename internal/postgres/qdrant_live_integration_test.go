package postgres

import (
	"context"
	"crypto/sha256"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	qdrant "github.com/qdrant/go-client/qdrant"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/retrieval"
)

// TestLiveQdrantDeletionResetAndReplay is deliberately opt-in. It combines a
// real PostgreSQL ledger with a real Qdrant process, deletes the derived
// collection, and proves that the canonical reset/replay path restores an
// accepted governed result without trusting leftover vectors.
func TestLiveQdrantDeletionResetAndReplay(t *testing.T) {
	client := livePostgresQdrantClient(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := newIntegrationStore(t, pool)
	configurationHash := sha256.Sum256([]byte("live-qdrant-reset-replay"))
	if err := store.EnsureProjectionConsumer(ctx, retrieval.DefaultQdrantProjectorName, configurationHash); err != nil {
		t.Fatal(err)
	}
	publication := indexPublicationFixture(t, pool, "live-qdrant", strings.Repeat("c", 40))
	snapshot, err := store.PublishIndexSnapshot(ctx, publication, testMetadata("live-qdrant-index"))
	if err != nil {
		t.Fatal(err)
	}
	generation := contracts.RetrievalGenerationID("fixture", "v1", 3, retrieval.SparseEncoderVersion)
	collection := "forja_live_rebuild_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	plan, err := retrieval.BuildQdrantCollectionPlan(collection, 3, generation)
	if err != nil {
		t.Fatal(err)
	}
	if err := retrieval.EnsureQdrantCollection(ctx, client, plan); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = client.DeleteCollection(cleanupContext, collection)
	})
	if err := store.RegisterRetrievalGeneration(ctx, retrievalGenerationConfig(generation, collection)); err != nil {
		t.Fatal(err)
	}
	writer := retrieval.QdrantPointWriter{Client: client, CollectionName: collection}
	worker := retrieval.ProjectionWorker{
		Deliveries: store, Source: store, Recorder: store, Writer: writer,
		Embedder: postgresFixtureEmbedder{}, Sparse: retrieval.HashingSparseEncoder{},
		WorkerID: "live-qdrant-rebuild-worker", Generation: generation, BatchSize: 10,
		ClaimTTL: time.Minute, MaxAttempts: 3, RetryDelay: time.Second,
	}
	if run, err := worker.ProcessOnce(ctx); err != nil || run.Published != 1 {
		t.Fatalf("initial projection run=%#v err=%v", run, err)
	}
	query := livePostgresRetrievalQuery(snapshot.SourceCommit, generation)
	assertLivePostgresResult(t, ctx, client, collection, store, query, publication.Bundle.Symbols[0].SymbolID)
	if err := client.DeleteCollection(ctx, collection); err != nil {
		t.Fatal(err)
	}
	if err := store.ResetRetrievalProjection(ctx, retrieval.DefaultQdrantProjectorName, configurationHash, generation); err != nil {
		t.Fatal(err)
	}
	if err := retrieval.EnsureQdrantCollection(ctx, client, plan); err != nil {
		t.Fatal(err)
	}
	if run, err := worker.ProcessOnce(ctx); err != nil || run.Published != 1 {
		t.Fatalf("rebuild projection run=%#v err=%v", run, err)
	}
	assertLivePostgresResult(t, ctx, client, collection, store, query, publication.Bundle.Symbols[0].SymbolID)
}

func livePostgresQdrantClient(t *testing.T) *qdrant.Client {
	t.Helper()
	if os.Getenv("FORJA_QDRANT_LIVE") != "1" {
		t.Skip("set FORJA_QDRANT_LIVE=1 to run destructive PostgreSQL/Qdrant integration")
	}
	port := 6334
	if value := os.Getenv("FORJA_QDRANT_GRPC_PORT"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("parse FORJA_QDRANT_GRPC_PORT: %v", err)
		}
		port = parsed
	}
	host := os.Getenv("FORJA_QDRANT_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	client, err := retrieval.OpenQdrant(retrieval.QdrantEndpoint{
		Host: host, Port: port, APIKey: os.Getenv("FORJA_QDRANT_API_KEY"),
	})
	if err != nil {
		t.Fatalf("open live Qdrant: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func retrievalGenerationConfig(generation, collection string) persistence.RetrievalGenerationConfig {
	return persistence.RetrievalGenerationConfig{
		GenerationID: generation, CollectionAlias: "forja_live_retrieval", CollectionName: collection,
		EmbeddingModel: "fixture", EmbeddingVersion: "v1", Dimensions: 3, SparseEncoderVersion: retrieval.SparseEncoderVersion,
	}
}

func livePostgresRetrievalQuery(commit, generation string) contracts.RetrievalQuery {
	return contracts.RetrievalQuery{
		RequestID: "retrieval_request_live_rebuild", SchemaVersion: contracts.RetrievalSchemaVersion,
		TenantID: "tenant_" + DefaultTenantID, RepositoryID: "repo_" + DefaultRepositoryID, Query: "find main",
		Scope:              contracts.RetrievalScope{SourceCommit: commit, AllowedPaths: []string{"app/**"}},
		Filters:            contracts.RetrievalFilters{ArtifactFamilies: []string{"symbol"}, AuthorityClasses: []string{"canonical"}},
		Policy:             contracts.RetrievalPolicy{Limit: 2, DenseLimit: 3, SparseLimit: 3, DenseWeight: 1, SparseWeight: 1, RRFK: 60},
		ExpectedGeneration: &generation,
	}
}

func assertLivePostgresResult(t *testing.T, ctx context.Context, client *qdrant.Client, collection string, store *Store, query contracts.RetrievalQuery, symbolID string) {
	t.Helper()
	service := retrieval.QueryService{
		Client: client, CollectionName: collection, Embedder: postgresFixtureEmbedder{}, Sparse: retrieval.HashingSparseEncoder{},
		Resolver: store, QueryTimeout: 10 * time.Second,
	}
	result, err := service.Search(ctx, query)
	if err != nil || result.Status != "complete" || len(result.Accepted) != 1 || result.Accepted[0].EntityID != symbolID {
		t.Fatalf("governed result=%#v err=%v", result, err)
	}
}
