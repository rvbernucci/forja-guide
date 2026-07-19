package retrieval

import (
	"context"
	"encoding/json"
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

func TestProjectionWorkerProjectsCanonicalTestsAsTestFamily(t *testing.T) {
	t.Parallel()
	bundle := projectionFixture(t)
	symbol := bundle.Symbols[0]
	symbol.Test = true
	symbol.SymbolID = contracts.ComputeSymbolID(symbol)
	symbol.LineageID = contracts.ComputeSymbolLineageID(symbol)
	bundle.Symbols[0] = symbol
	bundle.Files[0].SymbolIDs = []string{symbol.SymbolID}
	delivery := persistence.ProjectionDelivery{ProjectorName: DefaultQdrantProjectorName, OutboxMessage: persistence.OutboxMessage{
		OutboxID: 71, AggregateType: "index_snapshot", AggregateID: bundle.Snapshot.SnapshotID,
		EventType: "index_snapshot.activated", Attempts: 1, FencingToken: 3,
	}}
	store := &recordingDeliveries{claimed: []persistence.ProjectionDelivery{delivery}}
	writer := &recordingPointWriter{}
	worker := projectionWorker(store, staticIndexSource{bundle: bundle, found: true}, &recordingPointRecorder{}, writer)
	if _, err := worker.ProcessOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(writer.points) != 1 || writer.points[0].ArtifactFamily != "test" || writer.points[0].EntityID != symbol.SymbolID {
		t.Fatalf("projected points=%#v", writer.points)
	}
}

func TestProjectionWorkerProjectsResolvedDecisionFromCanonicalLookup(t *testing.T) {
	t.Parallel()
	decidedAt := time.Date(2026, 7, 19, 16, 0, 0, 0, time.UTC)
	decidedBy, reason := "operator", "Bounded approval is ready."
	decision := contracts.Decision{
		DecisionID: "decision_11111111-2222-4333-8444-555555555555", SchemaVersion: "1.0",
		SprintID: "sprint_11111111-2222-4333-8444-555555555555", RunID: "run_fixture",
		Action: "submit_sprint", RiskClass: "low", Status: "approved", Version: 2,
		RequestedBy: "planner", DecidedBy: &decidedBy, Reason: &reason, DecidedAt: &decidedAt,
	}
	store := &recordingDeliveries{claimed: []persistence.ProjectionDelivery{{ProjectorName: DefaultQdrantProjectorName, OutboxMessage: persistence.OutboxMessage{
		OutboxID: 72, AggregateType: "decision", AggregateID: decision.DecisionID, EventType: "decision.approved", Attempts: 1, FencingToken: 3,
	}}}}
	writer := &recordingPointWriter{}
	worker := projectionWorker(store, staticIndexSource{}, &recordingPointRecorder{}, writer)
	worker.Decisions = staticDecisionSource{decision: decision, found: true}
	worker.DecisionTenantID = retrievalTenantID
	worker.DecisionRepositoryID = retrievalRepositoryID
	if _, err := worker.ProcessOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(writer.points) != 1 || writer.points[0].ArtifactFamily != "decision" || writer.points[0].EntityID != decision.DecisionID {
		t.Fatalf("points=%#v", writer.points)
	}
}

func TestProjectionWorkerProjectsActiveMemoryFromCanonicalLookup(t *testing.T) {
	t.Parallel()
	body := []byte("Bounded durable memory")
	record := validMemoryRetrievalRecord(body)
	store := &recordingDeliveries{claimed: []persistence.ProjectionDelivery{{ProjectorName: DefaultQdrantProjectorName, OutboxMessage: persistence.OutboxMessage{
		OutboxID: 73, AggregateType: "memory", AggregateID: record.Memory.MemoryID, EventType: "memory.promoted", Attempts: 1, FencingToken: 3,
	}}}}
	writer := &recordingPointWriter{}
	worker := projectionWorker(store, staticIndexSource{}, &recordingPointRecorder{}, writer)
	worker.Memories = staticMemorySource{record: record, found: true}
	worker.MemoryBodies = staticMemoryBodyReader{body: body}
	if _, err := worker.ProcessOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(writer.points) != 1 || writer.points[0].ArtifactFamily != "memory" || writer.points[0].EntityID != record.Memory.MemoryID {
		t.Fatalf("points=%#v", writer.points)
	}
}

func TestProjectionWorkerProjectsOnlyCanonicalFailureIncidents(t *testing.T) {
	t.Parallel()
	incident := projectionIncident(t)
	store := &recordingDeliveries{claimed: []persistence.ProjectionDelivery{{ProjectorName: DefaultQdrantProjectorName, OutboxMessage: persistence.OutboxMessage{
		OutboxID: 74, AggregateType: "attempt", AggregateID: incident.AttemptID, EventType: "attempt.finished", Attempts: 1, FencingToken: 3,
	}}}}
	writer := &recordingPointWriter{}
	worker := projectionWorker(store, staticIndexSource{}, &recordingPointRecorder{}, writer)
	worker.Incidents = staticIncidentSource{incident: incident, found: true}
	if _, err := worker.ProcessOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(writer.points) != 1 || writer.points[0].ArtifactFamily != "incident" || writer.points[0].EntityID != incident.IncidentID {
		t.Fatalf("points=%#v", writer.points)
	}

	store = &recordingDeliveries{claimed: []persistence.ProjectionDelivery{{ProjectorName: DefaultQdrantProjectorName, OutboxMessage: persistence.OutboxMessage{
		OutboxID: 75, AggregateType: "attempt", AggregateID: incident.AttemptID, EventType: "attempt.started", Attempts: 1, FencingToken: 3,
	}}}}
	writer = &recordingPointWriter{}
	worker = projectionWorker(store, staticIndexSource{}, &recordingPointRecorder{}, writer)
	// The source deliberately has a current incident. A delayed non-terminal
	// delivery must still not project it under the wrong outbox receipt.
	worker.Incidents = staticIncidentSource{incident: incident, found: true}
	run, err := worker.ProcessOnce(t.Context())
	if err != nil || run != (ProjectionRun{Claimed: 1, Published: 1, Skipped: 1}) || len(writer.points) != 0 {
		t.Fatalf("run=%#v points=%#v err=%v", run, writer.points, err)
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

func TestProjectionWorkerBoundsStalledDeliveryAndLeavesItForRetry(t *testing.T) {
	t.Parallel()
	bundle := projectionFixture(t)
	delivery := persistence.ProjectionDelivery{ProjectorName: DefaultQdrantProjectorName, OutboxMessage: persistence.OutboxMessage{
		OutboxID: 81, AggregateType: "index_snapshot", AggregateID: bundle.Snapshot.SnapshotID,
		EventType: "index_snapshot.activated", Attempts: 1, FencingToken: 1,
	}}
	store := &recordingDeliveries{claimed: []persistence.ProjectionDelivery{delivery}}
	worker := projectionWorker(store, staticIndexSource{bundle: bundle, found: true}, &recordingPointRecorder{}, blockingPointWriter{})
	worker.DeliveryTimeout = time.Millisecond
	started := time.Now()
	run, err := worker.ProcessOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if run != (ProjectionRun{Claimed: 1, Retried: 1}) || len(store.completed) != 0 || len(store.failed) != 1 || !errors.Is(store.cause, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("run=%#v completed=%v failed=%v cause=%v elapsed=%s", run, store.completed, store.failed, store.cause, time.Since(started))
	}
}

func TestProjectionWorkerShutdownLeavesInFlightDeliveryUnacknowledged(t *testing.T) {
	t.Parallel()
	bundle := projectionFixture(t)
	delivery := persistence.ProjectionDelivery{ProjectorName: DefaultQdrantProjectorName, OutboxMessage: persistence.OutboxMessage{
		OutboxID: 82, AggregateType: "index_snapshot", AggregateID: bundle.Snapshot.SnapshotID,
		EventType: "index_snapshot.activated", Attempts: 1, FencingToken: 1,
	}}
	store := &recordingDeliveries{claimed: []persistence.ProjectionDelivery{delivery}}
	writer := &cancellationPointWriter{started: make(chan struct{})}
	worker := projectionWorker(store, staticIndexSource{bundle: bundle, found: true}, &recordingPointRecorder{}, writer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		run ProjectionRun
		err error
	}
	finished := make(chan result, 1)
	go func() {
		run, err := worker.ProcessOnce(ctx)
		finished <- result{run: run, err: err}
	}()
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("projection did not reach its cancellable derived write")
	}
	cancel()
	select {
	case result := <-finished:
		if !errors.Is(result.err, context.Canceled) || result.run != (ProjectionRun{Claimed: 1}) || len(store.completed) != 0 || len(store.failed) != 0 {
			t.Fatalf("run=%#v err=%v completed=%v failed=%v", result.run, result.err, store.completed, store.failed)
		}
	case <-time.After(time.Second):
		t.Fatal("projection did not stop after cancellation")
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

func TestProjectionWorkerTombstonesCanonicallyBeforeDerivedDelete(t *testing.T) {
	t.Parallel()
	bundle := projectionFixture(t)
	snapshot := bundle.Snapshot
	artifactID := "artifact_projection_fixture"
	artifactHash := testHash("artifact")
	snapshot.Status = "superseded"
	snapshot.ArtifactID = &artifactID
	snapshot.ArtifactContentHash = &artifactHash
	validatedAt := snapshot.CreatedAt
	snapshot.ValidatedAt = &validatedAt
	payload, err := json.Marshal(map[string]any{"snapshot": snapshot})
	if err != nil {
		t.Fatal(err)
	}
	pointID := "retrieval_" + strings.Repeat("a", 64)
	store := &recordingDeliveries{claimed: []persistence.ProjectionDelivery{{ProjectorName: DefaultQdrantProjectorName, OutboxMessage: persistence.OutboxMessage{
		OutboxID: 10, AggregateType: "index_snapshot", AggregateID: snapshot.SnapshotID,
		EventType: "index_snapshot.superseded", Payload: payload, Attempts: 1, FencingToken: 1,
	}}}}
	recorder := &recordingPointRecorder{tombstoneIDs: []string{pointID}}
	deleter := &recordingPointDeleter{}
	worker := projectionWorker(store, staticIndexSource{}, recorder, &recordingPointWriter{})
	worker.Deleter = deleter
	run, err := worker.ProcessOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if run != (ProjectionRun{Claimed: 1, Published: 1}) || recorder.tombstoneCommit != snapshot.SourceCommit || len(deleter.pointIDs) != 1 || deleter.pointIDs[0] != pointID || len(store.completed) != 1 {
		t.Fatalf("run=%#v recorder=%#v deleter=%#v completed=%v", run, recorder, deleter, store.completed)
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

type staticDecisionSource struct {
	decision contracts.Decision
	found    bool
	err      error
}

type staticMemorySource struct {
	record MemoryRetrievalRecord
	found  bool
	err    error
}

type staticIncidentSource struct {
	incident contracts.Incident
	found    bool
	err      error
}

func (source staticMemorySource) GetActiveMemory(context.Context, string) (MemoryRetrievalRecord, bool, error) {
	return source.record, source.found, source.err
}

func (source staticIncidentSource) GetIncidentForAttempt(context.Context, string) (contracts.Incident, bool, error) {
	return source.incident, source.found, source.err
}

func (source staticDecisionSource) GetDecision(context.Context, string) (contracts.Decision, bool, error) {
	return source.decision, source.found, source.err
}

func (source staticIndexSource) GetActiveIndexBundle(context.Context) (indexing.IndexBundle, bool, error) {
	return source.bundle, source.found, source.err
}

func projectionIncident(t *testing.T) contracts.Incident {
	t.Helper()
	incident := contracts.Incident{
		IncidentID: "incident_attempt_11111111-2222-4333-8444-555555555555", SchemaVersion: contracts.IncidentSchemaVersion,
		TenantID: retrievalTenantID, RepositoryID: retrievalRepositoryID,
		RunID: "run_11111111-2222-4333-8444-555555555555", AttemptID: "attempt_11111111-2222-4333-8444-555555555555",
		AttemptVersion: 3, EventID: "event_projection_incident", EventType: "attempt.finished",
		Status: "open", Severity: "critical", Classification: "process_failure", Retryable: false,
		OccurredAt: time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC), EvidenceRefs: []string{},
	}
	var err error
	incident.SourceHash, err = contracts.IncidentSourceHash(incident)
	if err != nil {
		t.Fatal(err)
	}
	return incident
}

type recordingPointWriter struct {
	points []contracts.RetrievalPoint
	err    error
}

type blockingPointWriter struct{}

func (blockingPointWriter) UpsertPoint(ctx context.Context, _ contracts.RetrievalPoint) error {
	<-ctx.Done()
	return ctx.Err()
}

type cancellationPointWriter struct {
	started chan struct{}
}

func (writer *cancellationPointWriter) UpsertPoint(ctx context.Context, _ contracts.RetrievalPoint) error {
	close(writer.started)
	<-ctx.Done()
	return ctx.Err()
}

func (writer *recordingPointWriter) UpsertPoint(_ context.Context, point contracts.RetrievalPoint) error {
	writer.points = append(writer.points, point)
	return writer.err
}

type recordingPointRecorder struct {
	points          []contracts.RetrievalPoint
	outboxIDs       []int64
	tombstoneIDs    []string
	tombstoneCommit string
	err             error
}

type recordingPointDeleter struct {
	pointIDs []string
	err      error
}

func (deleter *recordingPointDeleter) DeletePoints(_ context.Context, pointIDs []string) error {
	deleter.pointIDs = append([]string(nil), pointIDs...)
	return deleter.err
}

func (recorder *recordingPointRecorder) RecordRetrievalProjectionPoint(_ context.Context, point contracts.RetrievalPoint, outboxID int64) error {
	recorder.points = append(recorder.points, point)
	recorder.outboxIDs = append(recorder.outboxIDs, outboxID)
	return recorder.err
}

func (recorder *recordingPointRecorder) TombstoneRetrievalProjectionPoints(_ context.Context, _ string, sourceCommit string, _ int64) ([]string, error) {
	recorder.tombstoneCommit = sourceCommit
	return append([]string(nil), recorder.tombstoneIDs...), recorder.err
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
func (store *recordingDeliveries) RequeueProjectionDelivery(context.Context, string, int64) error {
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
