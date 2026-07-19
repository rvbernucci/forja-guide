package retrieval

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/indexing"
	"github.com/rvbernucci/forja-guide/internal/persistence"
)

func TestProjectionWorkerOnlyCompletesAfterVectorAndProvenanceWrites(t *testing.T) {
	t.Parallel()
	bundle := projectionFixture(t)
	delivery := persistence.ProjectionDelivery{ProjectorName: DefaultQdrantProjectorName, OutboxMessage: persistence.OutboxMessage{
		OutboxID: 7, AggregateType: "index_snapshot", AggregateID: bundle.Snapshot.SnapshotID,
		EventType: "index_snapshot.activated", Attempts: 1, FencingToken: 3,
	}}
	store := &recordingDeliveries{claimed: []persistence.ProjectionDelivery{delivery}}
	writer := &recordingPointWriter{}
	recorder := &recordingPointRecorder{}
	worker := projectionWorker(store, staticIndexSource{bundle: bundle, found: true}, recorder, writer)
	run, err := worker.ProcessOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if run != (ProjectionRun{Claimed: 1, Published: 1}) || len(writer.points) != 1 || len(recorder.points) != 1 || len(store.completed) != 1 || len(store.failed) != 0 {
		t.Fatalf("run=%#v writes=%d receipts=%d complete=%d failed=%d cause=%v", run, len(writer.points), len(recorder.points), len(store.completed), len(store.failed), store.cause)
	}
	if writer.points[0].PointID != recorder.points[0].PointID || recorder.outboxIDs[0] != delivery.OutboxID {
		t.Fatalf("point receipt mismatch: %#v %#v", writer.points, recorder)
	}
}

func TestProjectionWorkerRetriesAndDoesNotCheckpointFailedWrite(t *testing.T) {
	t.Parallel()
	bundle := projectionFixture(t)
	delivery := persistence.ProjectionDelivery{ProjectorName: DefaultQdrantProjectorName, OutboxMessage: persistence.OutboxMessage{
		OutboxID: 8, AggregateType: "index_snapshot", AggregateID: bundle.Snapshot.SnapshotID,
		EventType: "index_snapshot.activated", Attempts: 1, FencingToken: 1,
	}}
	store := &recordingDeliveries{claimed: []persistence.ProjectionDelivery{delivery}}
	writer := &recordingPointWriter{err: errors.New("Qdrant unavailable")}
	worker := projectionWorker(store, staticIndexSource{bundle: bundle, found: true}, &recordingPointRecorder{}, writer)
	run, err := worker.ProcessOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if run != (ProjectionRun{Claimed: 1, Retried: 1}) || len(store.completed) != 0 || len(store.failed) != 1 {
		t.Fatalf("run=%#v completed=%d failed=%d", run, len(store.completed), len(store.failed))
	}
}

func TestProjectionWorkerSkipsSupersededActivationWithoutWriting(t *testing.T) {
	t.Parallel()
	bundle := projectionFixture(t)
	store := &recordingDeliveries{claimed: []persistence.ProjectionDelivery{{ProjectorName: DefaultQdrantProjectorName, OutboxMessage: persistence.OutboxMessage{
		OutboxID: 9, AggregateType: "index_snapshot", AggregateID: "snapshot_" + strings.Repeat("f", 64),
		EventType: "index_snapshot.activated", Attempts: 1, FencingToken: 1,
	}}}}
	writer := &recordingPointWriter{}
	worker := projectionWorker(store, staticIndexSource{bundle: bundle, found: true}, &recordingPointRecorder{}, writer)
	run, err := worker.ProcessOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if run != (ProjectionRun{Claimed: 1, Published: 1, Skipped: 1}) || len(writer.points) != 0 {
		t.Fatalf("run=%#v points=%d", run, len(writer.points))
	}
}

func projectionWorker(deliveries persistence.ProjectionDeliveryRepository, source ActiveIndexSource, recorder persistence.RetrievalPointRepository, writer PointWriter) ProjectionWorker {
	return ProjectionWorker{
		Deliveries: deliveries, Source: source, Recorder: recorder, Writer: writer,
		Embedder: fixtureEmbedder{}, Sparse: HashingSparseEncoder{}, WorkerID: "test-worker",
		Generation: contracts.RetrievalGenerationID("fixture", "v1", 3, SparseEncoderVersion),
		BatchSize:  10, ClaimTTL: time.Minute, MaxAttempts: 3, RetryDelay: time.Second,
		Now: func() time.Time { return time.Date(2026, 7, 19, 16, 0, 0, 0, time.UTC) },
	}
}

type staticIndexSource struct {
	bundle indexing.IndexBundle
	found  bool
	err    error
}

func (source staticIndexSource) GetActiveIndexBundle(context.Context) (indexing.IndexBundle, bool, error) {
	return source.bundle, source.found, source.err
}

type recordingPointWriter struct {
	points []contracts.RetrievalPoint
	err    error
}

func (writer *recordingPointWriter) UpsertPoint(_ context.Context, point contracts.RetrievalPoint) error {
	writer.points = append(writer.points, point)
	return writer.err
}

type recordingPointRecorder struct {
	points    []contracts.RetrievalPoint
	outboxIDs []int64
	err       error
}

func (recorder *recordingPointRecorder) RecordRetrievalProjectionPoint(_ context.Context, point contracts.RetrievalPoint, outboxID int64) error {
	recorder.points = append(recorder.points, point)
	recorder.outboxIDs = append(recorder.outboxIDs, outboxID)
	return recorder.err
}

type recordingDeliveries struct {
	claimed   []persistence.ProjectionDelivery
	completed []int64
	failed    []int64
	cause     error
}

func (store *recordingDeliveries) EnsureProjectionConsumer(context.Context, string, [32]byte) error {
	return nil
}
func (store *recordingDeliveries) ClaimProjectionDeliveries(context.Context, string, string, int, time.Duration) ([]persistence.ProjectionDelivery, error) {
	return store.claimed, nil
}
func (store *recordingDeliveries) CompleteProjectionDelivery(_ context.Context, _ string, outboxID int64, _ string, _ int64) error {
	store.completed = append(store.completed, outboxID)
	return nil
}
func (store *recordingDeliveries) FailProjectionDelivery(_ context.Context, _ string, outboxID int64, _ string, _ int64, cause error, _ time.Time, _ int) error {
	store.failed = append(store.failed, outboxID)
	store.cause = cause
	return nil
}

func projectionFixture(t *testing.T) indexing.IndexBundle {
	t.Helper()
	adapter := contracts.AdapterDescriptor{Name: "python", Version: "fixture", ConfigurationHash: testHash("configuration"), CapabilityHash: testHash("capability")}
	adapterHash, err := contracts.ComputeAdapterSetHash([]contracts.AdapterDescriptor{adapter})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := contracts.RepositorySnapshot{
		SchemaVersion: contracts.IndexSchemaVersion, TenantID: retrievalTenantID, RepositoryID: retrievalRepositoryID,
		SourceCommit: retrievalCommit, SourceTree: strings.Repeat("b", 40), ConfigurationHash: testHash("configuration"),
		AdapterSetHash: adapterHash, Adapters: []contracts.AdapterDescriptor{adapter}, Status: "proposed", Version: 1,
		Counts: contracts.SnapshotCounts{Files: 1, Symbols: 1}, CreatedBy: "retrieval-test",
		CreatedAt: time.Date(2026, 7, 19, 16, 0, 0, 0, time.UTC),
	}
	snapshot.SnapshotID = contracts.ComputeSnapshotID(snapshot)
	file := contracts.FileCard{SchemaVersion: contracts.IndexSchemaVersion, SnapshotID: snapshot.SnapshotID, RepositoryID: snapshot.RepositoryID, SourceCommit: snapshot.SourceCommit, Path: "app/main.py", GitBlobID: strings.Repeat("c", 40), SourceHash: testHash("source"), SizeBytes: 42, Language: "python", SymbolIDs: []string{}, Diagnostics: []contracts.DiagnosticSummary{}}
	file.FileID = contracts.ComputeFileID(file)
	file.LineageID = contracts.ComputeFileLineageID(file)
	symbol := contracts.SymbolCard{SchemaVersion: contracts.IndexSchemaVersion, SnapshotID: snapshot.SnapshotID, FileID: file.FileID, FileLineageID: file.LineageID, Language: "python", Kind: "function", Name: "main", QualifiedName: "main", Signature: "def main() -> None", Declaration: contracts.SourceRange{Start: contracts.SourcePosition{Line: 1, Column: 1}, End: contracts.SourcePosition{Line: 2, Column: 1, Offset: 10}}}
	symbol.SymbolID = contracts.ComputeSymbolID(symbol)
	symbol.LineageID = contracts.ComputeSymbolLineageID(symbol)
	file.SymbolIDs = []string{symbol.SymbolID}
	return indexing.IndexBundle{Snapshot: snapshot, Files: []contracts.FileCard{file}, Symbols: []contracts.SymbolCard{symbol}, Relations: []contracts.RelationEvidence{}}
}

func testHash(_ string) string { return "sha256:" + strings.Repeat("a", 64) }
