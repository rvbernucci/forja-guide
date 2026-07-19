package postgres

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/retrieval"
)

func TestResolveRetrievalPointRequiresActiveCanonicalSymbolAndHash(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store := newIntegrationStore(t, pool)
	publication := indexPublicationFixture(t, pool, "resolver", strings.Repeat("a", 40))
	if _, err := store.PublishIndexSnapshot(t.Context(), publication, testMetadata("retrieval-resolver-index")); err != nil {
		t.Fatal(err)
	}
	file := publication.Bundle.Files[0]
	symbol := publication.Bundle.Symbols[0]
	generation := contracts.RetrievalGenerationID("fixture", "v1", 3, "sparse-fixture-v1")
	pointID := contracts.RetrievalPointID(generation, symbol.SymbolID, file.SourceHash)
	sourceHash, err := decodeContentHash(file.SourceHash)
	if err != nil {
		t.Fatal(err)
	}
	cardHash, err := decodeContentHash(indexHash("retrieval-card"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.retrieval_generations (
			tenant_id, repository_id, generation_id, collection_alias, collection_name,
			embedding_model, embedding_version, dimensions, sparse_encoder_version, status
		) VALUES ($1,$2,$3,'retrieval','retrieval_fixture','fixture','v1',3,'sparse-fixture-v1','active')`,
		DefaultTenantID, DefaultRepositoryID, generation); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.retrieval_projection_points (
			tenant_id, repository_id, generation_id, point_id, entity_id, artifact_family,
			source_commit, source_sha256, card_sha256, status, authority_class,
			stale, language, symbol_kind, repository_path, proof_refs
		) VALUES ($1,$2,$3,$4,$5,'symbol',$6,$7,$8,'active','canonical',false,'python','function',$9,'["snapshot"]')`,
		DefaultTenantID, DefaultRepositoryID, generation, pointID, symbol.SymbolID,
		publication.Bundle.Snapshot.SourceCommit, sourceHash, cardHash, file.Path); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveRetrievalPoint(t.Context(), pointID)
	if err != nil || len(resolved) != 1 || resolved[0].EntityID != symbol.SymbolID || resolved[0].SourceHash != file.SourceHash {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	wrongHash := indexHash("wrong-source")
	wrongPoint := contracts.RetrievalPointID(generation, symbol.SymbolID, wrongHash)
	wrongBytes, err := decodeContentHash(wrongHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.retrieval_projection_points (
			tenant_id, repository_id, generation_id, point_id, entity_id, artifact_family,
			source_commit, source_sha256, card_sha256, status, authority_class, stale, proof_refs
		) VALUES ($1,$2,$3,$4,$5,'symbol',$6,$7,$8,'active','canonical',false,'[]')`,
		DefaultTenantID, DefaultRepositoryID, generation, wrongPoint, symbol.SymbolID,
		publication.Bundle.Snapshot.SourceCommit, wrongBytes, cardHash); err != nil {
		t.Fatal(err)
	}
	if resolved, err := store.ResolveRetrievalPoint(t.Context(), wrongPoint); err != nil || len(resolved) != 0 {
		t.Fatalf("hash-mismatched resolved=%#v err=%v", resolved, err)
	}
}

func TestRecordRetrievalProjectionPointMakesOnlyCanonicalPointResolvable(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store := newIntegrationStore(t, pool)
	publication := indexPublicationFixture(t, pool, "record-point", strings.Repeat("a", 40))
	snapshot, err := store.PublishIndexSnapshot(t.Context(), publication, testMetadata("record-retrieval-point-index"))
	if err != nil {
		t.Fatal(err)
	}
	generation := contracts.RetrievalGenerationID("fixture", "v1", 3, retrieval.SparseEncoderVersion)
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.retrieval_generations (
			tenant_id, repository_id, generation_id, collection_alias, collection_name,
			embedding_model, embedding_version, dimensions, sparse_encoder_version, status
		) VALUES ($1,$2,$3,'retrieval','retrieval_fixture','fixture','v1',3,$4,'active')`,
		DefaultTenantID, DefaultRepositoryID, generation, retrieval.SparseEncoderVersion); err != nil {
		t.Fatal(err)
	}
	var outboxID int64
	if err := pool.QueryRow(t.Context(), `
		SELECT outbox.outbox_id
		FROM forja.outbox AS outbox
		JOIN forja.events AS event ON event.event_id=outbox.event_id
		WHERE event.aggregate_type='index_snapshot' AND event.aggregate_id=$1
		  AND event.event_type='index_snapshot.activated'`, snapshot.SnapshotID).Scan(&outboxID); err != nil {
		t.Fatal(err)
	}
	source, err := retrieval.BuildSymbolSource(snapshot, publication.Bundle.Files[0], publication.Bundle.Symbols[0], "canonical", nil)
	if err != nil {
		t.Fatal(err)
	}
	point, err := retrieval.BuildPoint(t.Context(), source, generation, postgresFixtureEmbedder{}, retrieval.HashingSparseEncoder{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRetrievalProjectionPoint(t.Context(), point, outboxID); err != nil {
		t.Fatal(err)
	}
	// An idempotent replay may refresh receipt time but cannot create another
	// canonical identity row for the same stable point.
	if err := store.RecordRetrievalProjectionPoint(t.Context(), point, outboxID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM forja.retrieval_projection_points WHERE point_id=$1`, point.PointID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("point rows=%d, want 1", count)
	}
	resolved, err := store.ResolveRetrievalPoint(t.Context(), point.PointID)
	if err != nil || len(resolved) != 1 || resolved[0].EntityID != point.EntityID {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	tombstoned, err := store.TombstoneRetrievalProjectionPoints(t.Context(), generation, snapshot.SourceCommit, outboxID)
	if err != nil || len(tombstoned) != 1 || tombstoned[0] != point.PointID {
		t.Fatalf("tombstoned=%#v err=%v", tombstoned, err)
	}
	if resolved, err := store.ResolveRetrievalPoint(t.Context(), point.PointID); err != nil || len(resolved) != 0 {
		t.Fatalf("tombstoned point resolved=%#v err=%v", resolved, err)
	}
}

func TestFencedSymbolProjectionPublishesOnlyAfterCanonicalReceipt(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store := newIntegrationStore(t, pool)
	configurationHash := sha256.Sum256([]byte("retrieval-projector-fixture"))
	if err := store.EnsureProjectionConsumer(t.Context(), retrieval.DefaultQdrantProjectorName, configurationHash); err != nil {
		t.Fatal(err)
	}
	publication := indexPublicationFixture(t, pool, "worker-point", strings.Repeat("a", 40))
	snapshot, err := store.PublishIndexSnapshot(t.Context(), publication, testMetadata("fenced-retrieval-worker"))
	if err != nil {
		t.Fatal(err)
	}
	generation := contracts.RetrievalGenerationID("fixture", "v1", 3, retrieval.SparseEncoderVersion)
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.retrieval_generations (
			tenant_id, repository_id, generation_id, collection_alias, collection_name,
			embedding_model, embedding_version, dimensions, sparse_encoder_version, status
		) VALUES ($1,$2,$3,'retrieval','retrieval_fixture','fixture','v1',3,$4,'active')`,
		DefaultTenantID, DefaultRepositoryID, generation, retrieval.SparseEncoderVersion); err != nil {
		t.Fatal(err)
	}
	writer := &postgresRecordingPointWriter{}
	worker := retrieval.ProjectionWorker{
		Deliveries: store, Source: store, Recorder: store, Writer: writer,
		Embedder: postgresFixtureEmbedder{}, Sparse: retrieval.HashingSparseEncoder{},
		WorkerID: "postgres-integration-worker", Generation: generation, BatchSize: 10,
		ClaimTTL: time.Minute, MaxAttempts: 3, RetryDelay: time.Second,
	}
	run, err := worker.ProcessOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if run.Claimed != 1 || run.Published != 1 || run.Retried != 0 || len(writer.points) != 1 {
		t.Fatalf("run=%#v points=%d", run, len(writer.points))
	}
	resolved, err := store.ResolveRetrievalPoint(t.Context(), writer.points[0].PointID)
	if err != nil || len(resolved) != 1 || resolved[0].EntityID != publication.Bundle.Symbols[0].SymbolID {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	var state string
	var checkpoint int64
	if err := pool.QueryRow(t.Context(), `
		SELECT delivery.state, checkpoint.last_outbox_id
		FROM forja.projection_deliveries AS delivery
		JOIN forja.projection_checkpoints AS checkpoint
		  ON checkpoint.tenant_id=delivery.tenant_id AND checkpoint.repository_id=delivery.repository_id
		 AND checkpoint.projector_name=delivery.projector_name
		WHERE delivery.projector_name=$1`, retrieval.DefaultQdrantProjectorName).Scan(&state, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if state != "published" || checkpoint < 1 || snapshot.SnapshotID == "" {
		t.Fatalf("delivery state=%q checkpoint=%d snapshot=%q", state, checkpoint, snapshot.SnapshotID)
	}
}

type postgresFixtureEmbedder struct{}

func (postgresFixtureEmbedder) Descriptor() contracts.EmbeddingDescriptor {
	return contracts.EmbeddingDescriptor{
		Model: "fixture", Version: "v1", Dimensions: 3,
		SparseEncoderVersion: retrieval.SparseEncoderVersion,
		EmbeddedAt:           time.Date(2026, 7, 19, 17, 0, 0, 0, time.UTC),
	}
}

func (postgresFixtureEmbedder) Embed(context.Context, string) ([]float64, error) {
	return []float64{0.1, 0.2, 0.3}, nil
}

type postgresRecordingPointWriter struct {
	points []contracts.RetrievalPoint
}

func (writer *postgresRecordingPointWriter) UpsertPoint(_ context.Context, point contracts.RetrievalPoint) error {
	writer.points = append(writer.points, point)
	return nil
}
