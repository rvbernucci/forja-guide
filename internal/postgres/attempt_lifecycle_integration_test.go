package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/persistence"
)

func TestAttemptLifecycleIsFencedIdempotentAndSecretSafe(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	runID := mustRunID(t)
	if _, err := store.CreateRun(t.Context(), runID, "Execute one durable worker", testMetadata("attempt-life-run")); err != nil {
		t.Fatalf("create run: %v", err)
	}
	lease, err := store.AcquireLease(t.Context(), persistence.LeaseKey{
		TenantID:     DefaultTenantID,
		ResourceType: "scheduler",
		ResourceID:   "attempt-lifecycle",
	}, "scheduler-lifecycle", time.Minute)
	if err != nil {
		t.Fatalf("acquire scheduler lease: %v", err)
	}
	proof := persistence.LeaseProof{LeaseKey: lease.LeaseKey, OwnerID: lease.OwnerID, FencingToken: lease.FencingToken}
	created, err := store.CreateAttempt(t.Context(), runID, "queued", testMetadata("attempt-life-create"), proof)
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	if created.Version != 1 || created.UpdatedAt.IsZero() {
		t.Fatalf("created attempt = %#v", created)
	}
	read, err := store.GetAttempt(t.Context(), created.AttemptID)
	if err != nil || !reflect.DeepEqual(read, created) {
		t.Fatalf("read=%#v err=%v want=%#v", read, err, created)
	}
	startMetadata := testMetadata("attempt-life-start")
	started, err := store.StartAttempt(t.Context(), created.AttemptID, 1, startMetadata, proof)
	if err != nil {
		t.Fatalf("start attempt: %v", err)
	}
	if started.Status != "running" || started.Version != 2 || started.StartedAt == nil {
		t.Fatalf("started attempt = %#v", started)
	}
	replayedStart, err := store.StartAttempt(t.Context(), created.AttemptID, 1, startMetadata, proof)
	if err != nil || !reflect.DeepEqual(replayedStart, started) {
		t.Fatalf("start replay=%#v err=%v", replayedStart, err)
	}
	result := successfulWorkerResult(created.AttemptID, runID.String())
	result.StartedAt = started.StartedAt.Add(time.Microsecond)
	result.FinishedAt = time.Now().UTC()
	result.DurationMS = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
	finishMetadata := testMetadata("attempt-life-finish")
	finished, err := store.FinishAttempt(t.Context(), created.AttemptID, 2, result, finishMetadata, proof)
	if err != nil {
		t.Fatalf("finish attempt: %v", err)
	}
	if finished.Status != "succeeded" || finished.Version != 3 || finished.FinishedAt == nil {
		t.Fatalf("finished attempt = %#v", finished)
	}
	replayedFinish, err := store.FinishAttempt(t.Context(), created.AttemptID, 2, result, finishMetadata, proof)
	if err != nil || !reflect.DeepEqual(replayedFinish, finished) {
		t.Fatalf("finish replay=%#v err=%v", replayedFinish, err)
	}
	var eventCount, outboxCount int
	var finishedPayload string
	if err := pool.QueryRow(t.Context(), `
		SELECT
		  (SELECT count(*) FROM forja.events WHERE aggregate_type='attempt' AND aggregate_id=$1),
		  (SELECT count(*) FROM forja.outbox AS o JOIN forja.events AS e ON e.event_id=o.event_id
		   WHERE e.aggregate_type='attempt' AND e.aggregate_id=$1),
		  (SELECT payload::text FROM forja.events
		   WHERE aggregate_type='attempt' AND aggregate_id=$1 AND event_type='attempt.finished')`,
		created.AttemptID,
	).Scan(&eventCount, &outboxCount, &finishedPayload); err != nil {
		t.Fatalf("inspect attempt events: %v", err)
	}
	if eventCount != 3 || outboxCount != 3 {
		t.Fatalf("events/outbox=%d/%d, want 3/3", eventCount, outboxCount)
	}
	if strings.Contains(finishedPayload, "SECRET_STDOUT") || strings.Contains(finishedPayload, "SECRET_STDERR") {
		t.Fatalf("canonical event leaked raw process output: %s", finishedPayload)
	}
	for _, required := range []string{result.StdoutSHA256, result.StderrSHA256, "termination_reason"} {
		if !strings.Contains(finishedPayload, required) {
			t.Fatalf("finished event lacks %q: %s", required, finishedPayload)
		}
	}
}

func TestAttemptReconciliationUsesDeadFencesNotPIDs(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	runID := mustRunID(t)
	if _, err := store.CreateRun(t.Context(), runID, "Recover an abandoned attempt", testMetadata("reconcile-run")); err != nil {
		t.Fatalf("create run: %v", err)
	}
	key := persistence.LeaseKey{TenantID: DefaultTenantID, ResourceType: "scheduler", ResourceID: "restart-reconcile"}
	oldLease, err := store.AcquireLease(t.Context(), key, "scheduler-before-restart", time.Minute)
	if err != nil {
		t.Fatalf("acquire old lease: %v", err)
	}
	oldProof := persistence.LeaseProof{LeaseKey: oldLease.LeaseKey, OwnerID: oldLease.OwnerID, FencingToken: oldLease.FencingToken}
	abandoned, err := store.CreateAttempt(t.Context(), runID, "queued", testMetadata("reconcile-create-old"), oldProof)
	if err != nil {
		t.Fatalf("create abandoned attempt: %v", err)
	}
	abandoned, err = store.StartAttempt(t.Context(), abandoned.AttemptID, abandoned.Version, testMetadata("reconcile-start-old"), oldProof)
	if err != nil {
		t.Fatalf("start abandoned attempt: %v", err)
	}
	if err := store.ReleaseLease(t.Context(), key, oldLease.OwnerID, oldLease.FencingToken); err != nil {
		t.Fatalf("release old lease: %v", err)
	}
	newLease, err := store.AcquireLease(t.Context(), key, "scheduler-after-restart", time.Minute)
	if err != nil {
		t.Fatalf("acquire replacement lease: %v", err)
	}
	newProof := persistence.LeaseProof{LeaseKey: newLease.LeaseKey, OwnerID: newLease.OwnerID, FencingToken: newLease.FencingToken}
	current, err := store.CreateAttempt(t.Context(), runID, "queued", testMetadata("reconcile-create-new"), newProof)
	if err != nil {
		t.Fatalf("create current attempt: %v", err)
	}
	metadata := testMetadata("reconcile-abandoned")
	reconciled, err := store.ReconcileAbandonedAttempts(t.Context(), metadata, newProof)
	if err != nil {
		t.Fatalf("reconcile attempts: %v", err)
	}
	if len(reconciled) != 1 || reconciled[0].AttemptID != abandoned.AttemptID ||
		reconciled[0].Status != "failed_retryable" || reconciled[0].Version != 3 {
		t.Fatalf("reconciled = %#v", reconciled)
	}
	replay, err := store.ReconcileAbandonedAttempts(t.Context(), metadata, newProof)
	if err != nil || len(replay) != 1 || !reflect.DeepEqual(replay[0], reconciled[0]) {
		t.Fatalf("reconcile replay=%#v err=%v", replay, err)
	}
	currentRead, err := store.GetAttempt(t.Context(), current.AttemptID)
	if err != nil || currentRead.Status != "queued" || currentRead.Version != 1 {
		t.Fatalf("current attempt changed: %#v err=%v", currentRead, err)
	}
	if _, err := store.FinishAttempt(
		t.Context(),
		abandoned.AttemptID,
		abandoned.Version,
		successfulWorkerResult(abandoned.AttemptID, runID.String()),
		testMetadata("stale-finish-after-restart"),
		oldProof,
	); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("stale scheduler finish error=%v, want conflict", err)
	}
}

func TestAttemptReconciliationPersistsEmptyReplay(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	key := persistence.LeaseKey{
		TenantID: DefaultTenantID, ResourceType: "scheduler", ResourceID: "empty-reconcile",
	}
	lease, err := store.AcquireLease(t.Context(), key, "empty-reconcile-scheduler", time.Minute)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	proof := persistence.LeaseProof{
		LeaseKey: lease.LeaseKey, OwnerID: lease.OwnerID, FencingToken: lease.FencingToken,
	}
	metadata := testMetadata("empty-reconcile")
	reconciled, err := store.ReconcileAbandonedAttempts(t.Context(), metadata, proof)
	if err != nil || len(reconciled) != 0 {
		t.Fatalf("empty reconciliation=%#v err=%v", reconciled, err)
	}

	scope := "reconcile_attempts:" + store.repositoryID + ":" + proof.ResourceID
	var response []byte
	if err := pool.QueryRow(t.Context(), `
		SELECT convert_to(response_body::text, 'UTF8')
		FROM forja.idempotency_keys
		WHERE tenant_id=$1 AND scope=$2 AND idempotency_key=$3`,
		DefaultTenantID,
		scope,
		metadata.IdempotencyKey,
	).Scan(&response); err != nil {
		t.Fatalf("read empty reconciliation receipt: %v", err)
	}
	var receipt attemptReconciliationReplay
	if err := json.Unmarshal(response, &receipt); err != nil {
		t.Fatalf("decode empty reconciliation receipt: %v", err)
	}
	if len(receipt.Attempts) != 0 || receipt.Authority.OwnerID != proof.OwnerID ||
		receipt.Command.IdempotencyKey != metadata.IdempotencyKey {
		t.Fatalf("empty reconciliation receipt=%s", response)
	}
	retryMetadata := metadata
	retryMetadata.CorrelationID = "empty-reconcile-retry-correlation"
	replay, err := store.ReconcileAbandonedAttempts(t.Context(), retryMetadata, proof)
	if err != nil || len(replay) != 0 {
		t.Fatalf("empty reconciliation replay=%#v err=%v", replay, err)
	}
}

func TestFinishAttemptRejectsInvalidSupervisorResult(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	runID := mustRunID(t)
	if _, err := store.CreateRun(t.Context(), runID, "Reject invalid worker completion", testMetadata("invalid-result-run")); err != nil {
		t.Fatalf("create run: %v", err)
	}
	lease, err := store.AcquireLease(t.Context(), persistence.LeaseKey{
		TenantID: DefaultTenantID, ResourceType: "scheduler", ResourceID: "invalid-result",
	}, "invalid-result-scheduler", time.Minute)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	proof := persistence.LeaseProof{LeaseKey: lease.LeaseKey, OwnerID: lease.OwnerID, FencingToken: lease.FencingToken}
	attempt, err := store.CreateAttempt(t.Context(), runID, "queued", testMetadata("invalid-result-create"), proof)
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	attempt, err = store.StartAttempt(t.Context(), attempt.AttemptID, 1, testMetadata("invalid-result-start"), proof)
	if err != nil {
		t.Fatalf("start attempt: %v", err)
	}
	for _, test := range []struct {
		name   string
		key    string
		mutate func(*contracts.WorkerResult)
	}{
		{"status", "status", func(result *contracts.WorkerResult) { result.Status = "running" }},
		{"future finish", "future-finish", func(result *contracts.WorkerResult) {
			result.FinishedAt = time.Now().UTC().Add(time.Minute)
			result.DurationMS = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
		}},
		{"duration mismatch", "duration-mismatch", func(result *contracts.WorkerResult) { result.DurationMS++ }},
		{"forged output digest", "forged-digest", func(result *contracts.WorkerResult) {
			result.StdoutSHA256 = strings.Repeat("d", 64)
		}},
		{"invalid UTF-8 output", "invalid-utf8", func(result *contracts.WorkerResult) {
			result.Stdout = string([]byte{'x', 0xff})
			digest := sha256.Sum256([]byte(result.Stdout))
			result.StdoutSHA256 = hex.EncodeToString(digest[:])
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := successfulWorkerResult(attempt.AttemptID, runID.String())
			result.StartedAt = attempt.StartedAt.Add(time.Microsecond)
			result.FinishedAt = time.Now().UTC()
			result.DurationMS = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
			test.mutate(&result)
			if _, err := store.FinishAttempt(
				t.Context(), attempt.AttemptID, attempt.Version, result,
				testMetadata("invalid-result-finish-"+test.key), proof,
			); !fault.IsCode(err, fault.CodeInvalidArgument) {
				t.Fatalf("invalid result error=%v, want invalid argument", err)
			}
		})
	}
	read, err := store.GetAttempt(t.Context(), attempt.AttemptID)
	if err != nil || read.Status != "running" || read.Version != 2 {
		t.Fatalf("invalid result mutated attempt: %#v err=%v", read, err)
	}
}

func TestFinishAttemptAcceptsCanonicalUTF8OutputDigest(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	runID := mustRunID(t)
	if _, err := store.CreateRun(t.Context(), runID, "Persist canonical UTF-8 output", testMetadata("utf8-run")); err != nil {
		t.Fatalf("create run: %v", err)
	}
	lease, err := store.AcquireLease(t.Context(), persistence.LeaseKey{
		TenantID: DefaultTenantID, ResourceType: "scheduler", ResourceID: "utf8-output",
	}, "utf8-output-scheduler", time.Minute)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	proof := persistence.LeaseProof{LeaseKey: lease.LeaseKey, OwnerID: lease.OwnerID, FencingToken: lease.FencingToken}
	attempt, err := store.CreateAttempt(t.Context(), runID, "queued", testMetadata("utf8-create"), proof)
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	attempt, err = store.StartAttempt(t.Context(), attempt.AttemptID, 1, testMetadata("utf8-start"), proof)
	if err != nil {
		t.Fatalf("start attempt: %v", err)
	}
	result := successfulWorkerResult(attempt.AttemptID, runID.String())
	result.Stdout = "ok:\ufffd\n"
	digest := sha256.Sum256([]byte(result.Stdout))
	result.StdoutSHA256 = hex.EncodeToString(digest[:])
	result.StartedAt = attempt.StartedAt.Add(time.Microsecond)
	result.FinishedAt = time.Now().UTC()
	result.DurationMS = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
	finished, err := store.FinishAttempt(
		t.Context(), attempt.AttemptID, attempt.Version, result,
		testMetadata("utf8-finish"), proof,
	)
	if err != nil || finished.Status != "succeeded" {
		t.Fatalf("finish canonical UTF-8 result=%#v err=%v", finished, err)
	}
}

func successfulWorkerResult(attemptID string, runID string) contracts.WorkerResult {
	started := time.Now().UTC().Add(-time.Second)
	finished := started.Add(time.Second)
	exitCode := 0
	return contracts.WorkerResult{
		TaskID:            "task_00000000-0000-4000-8000-000000000001",
		AttemptID:         attemptID,
		RunID:             runID,
		SchemaVersion:     "1.0",
		Adapter:           "integration-worker",
		Status:            "succeeded",
		Retryable:         false,
		TerminationReason: "completed",
		StartedAt:         started,
		FinishedAt:        finished,
		DurationMS:        1000,
		ExitCode:          &exitCode,
		Stdout:            "SECRET_STDOUT",
		Stderr:            "SECRET_STDERR",
		StdoutSHA256:      "6347873326cd5a8ca4b0b65aa098fdb12ac1ca6c1c432862e05e0402404d4d1b",
		StderrSHA256:      "2076d050811c8fdeedda0bca24aa982d7d09fb70c45f4ccbf47940f8c2ae47cf",
		Usage:             contracts.WorkerUsage{InputTokens: 10, OutputTokens: 2, ToolCalls: 1},
		Report: &contracts.WorkerReport{
			Status: "completed", Summary: "integration completion",
			ChangedPaths: []string{}, EvidenceRefs: []string{}, Risks: []string{},
		},
		EvidenceRefs: []string{"evidence/result.json#sha256=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
	}
}
