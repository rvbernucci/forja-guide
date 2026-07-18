package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

var terminalAttemptStatuses = []string{
	"succeeded",
	"blocked",
	"failed_retryable",
	"failed_terminal",
	"cancelled",
}

var (
	workerContractOnce sync.Once
	workerContracts    *contracts.Registry
	workerContractErr  error
)

// GetAttempt reads one repository-bound durable attempt.
func (s *Store) GetAttempt(
	ctx context.Context,
	attemptID string,
) (persistence.Attempt, error) {
	if err := validateAttemptID(attemptID); err != nil {
		return persistence.Attempt{}, err
	}
	attempt, err := scanAttempt(s.pool.QueryRow(ctx, `
		SELECT a.attempt_id, a.run_id, a.ordinal, a.status,
		       a.lease_resource_type, a.lease_resource_id,
		       a.worker_id, a.fencing_token, a.started_at, a.finished_at,
		       a.version, a.created_at, a.updated_at
		FROM forja.attempts AS a
		JOIN forja.runs AS r
		  ON r.tenant_id=a.tenant_id AND r.run_id=a.run_id
		WHERE a.tenant_id=$1 AND r.repository_id=$2 AND a.attempt_id=$3`,
		s.tenantID,
		s.repositoryID,
		attemptID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return persistence.Attempt{}, fault.New(
			fault.CodeNotFound,
			"postgres.GetAttempt",
			fmt.Sprintf("attempt %s was not found", attemptID),
		)
	}
	if err != nil {
		return persistence.Attempt{}, databaseError("postgres.GetAttempt", err)
	}
	return attempt, nil
}

// StartAttempt transitions one queued attempt under its exact live scheduler
// fence. A replay returns the immutable response snapshot.
func (s *Store) StartAttempt(
	ctx context.Context,
	attemptID string,
	expectedVersion int,
	metadata runstate.CommandMetadata,
	proof persistence.LeaseProof,
) (persistence.Attempt, error) {
	if err := validateAttemptLifecycleInput(attemptID, expectedVersion, metadata, proof, s); err != nil {
		return persistence.Attempt{}, err
	}
	scope := "start_attempt:" + s.repositoryID + ":" + attemptID
	requestHash := hashCommand(metadata, scope, fmt.Sprint(expectedVersion), proof.OwnerID, fmt.Sprint(proof.FencingToken))
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return persistence.Attempt{}, databaseError("postgres.StartAttempt.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return persistence.Attempt{}, err
	}
	attempt, err := s.lockAttempt(ctx, tx, attemptID)
	if err != nil {
		return persistence.Attempt{}, err
	}
	if err := s.prepareAttemptWrite(ctx, tx, proof); err != nil {
		return persistence.Attempt{}, err
	}
	if replay, found, err := loadAttemptReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return persistence.Attempt{}, err
	} else if found {
		return replay, nil
	}
	if attempt.Status != "queued" || attempt.Version != expectedVersion {
		return persistence.Attempt{}, fault.New(
			fault.CodeConflict,
			"postgres.StartAttempt",
			"attempt state or version changed",
		)
	}
	if !attemptOwnedBy(attempt, proof) {
		return persistence.Attempt{}, fault.New(
			fault.CodeConflict,
			"postgres.StartAttempt",
			"attempt belongs to a different scheduler fence",
		)
	}
	now := postgresTimestamp(s.clock.Now())
	err = tx.QueryRow(ctx, `
		UPDATE forja.attempts
		SET status='running', started_at=$1, version=version+1, updated_at=$1
		WHERE tenant_id=$2 AND attempt_id=$3 AND status='queued' AND version=$4
		RETURNING attempt_id, run_id, ordinal, status,
		          lease_resource_type, lease_resource_id, worker_id, fencing_token,
		          started_at, finished_at, version, created_at, updated_at`,
		now,
		s.tenantID,
		attemptID,
		expectedVersion,
	).Scan(attemptScanTargets(&attempt)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistence.Attempt{}, fault.New(fault.CodeConflict, "postgres.StartAttempt", "attempt changed concurrently")
	}
	if err != nil {
		return persistence.Attempt{}, databaseError("postgres.StartAttempt.update", err)
	}
	normalizeAttemptTimes(&attempt)
	if err := s.appendAttemptEvent(ctx, tx, "attempt.started", attempt, nil, metadata); err != nil {
		return persistence.Attempt{}, err
	}
	if err := saveAttemptReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, attempt); err != nil {
		return persistence.Attempt{}, err
	}
	if err := s.enforceAttemptFence(ctx, tx, proof); err != nil {
		return persistence.Attempt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return persistence.Attempt{}, databaseError("postgres.StartAttempt.commit", err)
	}
	return attempt, nil
}

// FinishAttempt commits a terminal supervisor result without persisting raw
// stdout or stderr in canonical events.
func (s *Store) FinishAttempt(
	ctx context.Context,
	attemptID string,
	expectedVersion int,
	result contracts.WorkerResult,
	metadata runstate.CommandMetadata,
	proof persistence.LeaseProof,
) (persistence.Attempt, error) {
	if err := validateAttemptLifecycleInput(attemptID, expectedVersion, metadata, proof, s); err != nil {
		return persistence.Attempt{}, err
	}
	result.StartedAt = postgresTimestamp(result.StartedAt)
	result.FinishedAt = postgresTimestamp(result.FinishedAt)
	if err := validateCompletion(attemptID, result); err != nil {
		return persistence.Attempt{}, err
	}
	scope := "finish_attempt:" + s.repositoryID + ":" + attemptID
	hashParts := append([]string{scope, fmt.Sprint(expectedVersion)}, workerResultHashParts(result)...)
	hashParts = append(hashParts, proof.OwnerID, fmt.Sprint(proof.FencingToken))
	requestHash := hashCommand(metadata, hashParts...)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return persistence.Attempt{}, databaseError("postgres.FinishAttempt.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return persistence.Attempt{}, err
	}
	attempt, err := s.lockAttempt(ctx, tx, attemptID)
	if err != nil {
		return persistence.Attempt{}, err
	}
	if err := s.prepareAttemptWrite(ctx, tx, proof); err != nil {
		return persistence.Attempt{}, err
	}
	if replay, found, err := loadAttemptReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return persistence.Attempt{}, err
	} else if found {
		return replay, nil
	}
	if attempt.Status != "running" || attempt.Version != expectedVersion {
		return persistence.Attempt{}, fault.New(fault.CodeConflict, "postgres.FinishAttempt", "attempt state or version changed")
	}
	if !attemptOwnedBy(attempt, proof) {
		return persistence.Attempt{}, fault.New(fault.CodeConflict, "postgres.FinishAttempt", "attempt belongs to a different scheduler fence")
	}
	now := postgresTimestamp(s.clock.Now())
	if result.RunID != attempt.RunID || attempt.StartedAt == nil ||
		result.StartedAt.Before(*attempt.StartedAt) || result.FinishedAt.After(now) ||
		result.DurationMS != result.FinishedAt.Sub(result.StartedAt).Milliseconds() {
		return persistence.Attempt{}, fault.New(
			fault.CodeInvalidArgument,
			"postgres.FinishAttempt",
			"worker result disagrees with the durable attempt timeline",
		)
	}
	err = tx.QueryRow(ctx, `
		UPDATE forja.attempts
		SET status=$1, finished_at=$2, version=version+1, updated_at=$2
		WHERE tenant_id=$3 AND attempt_id=$4 AND status='running' AND version=$5
		RETURNING attempt_id, run_id, ordinal, status,
		          lease_resource_type, lease_resource_id, worker_id, fencing_token,
		          started_at, finished_at, version, created_at, updated_at`,
		result.Status,
		now,
		s.tenantID,
		attemptID,
		expectedVersion,
	).Scan(attemptScanTargets(&attempt)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistence.Attempt{}, fault.New(fault.CodeConflict, "postgres.FinishAttempt", "attempt changed concurrently")
	}
	if err != nil {
		return persistence.Attempt{}, databaseError("postgres.FinishAttempt.update", err)
	}
	normalizeAttemptTimes(&attempt)
	if err := s.appendAttemptEvent(ctx, tx, "attempt.finished", attempt, &result, metadata); err != nil {
		return persistence.Attempt{}, err
	}
	if err := saveAttemptReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, attempt); err != nil {
		return persistence.Attempt{}, err
	}
	if err := s.enforceAttemptFence(ctx, tx, proof); err != nil {
		return persistence.Attempt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return persistence.Attempt{}, databaseError("postgres.FinishAttempt.commit", err)
	}
	return attempt, nil
}

// ReconcileAbandonedAttempts marks active attempts from dead scheduler fences
// retryable. It never inspects or signals a stale operating-system PID.
func (s *Store) ReconcileAbandonedAttempts(
	ctx context.Context,
	metadata runstate.CommandMetadata,
	proof persistence.LeaseProof,
) ([]persistence.Attempt, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return nil, err
	}
	if err := validateAttemptFence(proof, s.tenantID, s.repositoryID); err != nil {
		return nil, err
	}
	scope := "reconcile_attempts:" + s.repositoryID + ":" + proof.ResourceID
	requestHash := hashCommand(metadata, scope, proof.OwnerID, fmt.Sprint(proof.FencingToken))
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, databaseError("postgres.ReconcileAbandonedAttempts.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT a.attempt_id, a.run_id, a.ordinal, a.status,
		       a.lease_resource_type, a.lease_resource_id,
		       a.worker_id, a.fencing_token, a.started_at, a.finished_at,
		       a.version, a.created_at, a.updated_at
		FROM forja.attempts AS a
		JOIN forja.runs AS r
		  ON r.tenant_id=a.tenant_id AND r.run_id=a.run_id
		WHERE a.tenant_id=$1 AND r.repository_id=$2
		  AND a.status IN ('queued', 'running')
		  AND a.lease_resource_type=$3 AND a.lease_resource_id=$4
		  AND NOT (a.worker_id=$5 AND a.fencing_token=$6)
		  AND NOT EXISTS (
		    SELECT 1 FROM forja.leases AS l
		    WHERE l.tenant_id=a.tenant_id AND l.repository_id=r.repository_id
		      AND l.resource_type=a.lease_resource_type
		      AND l.resource_id=a.lease_resource_id
		      AND l.owner_id=a.worker_id AND l.fencing_token=a.fencing_token
		      AND l.expires_at > clock_timestamp()
		  )
		ORDER BY a.run_id, a.ordinal
		FOR UPDATE OF a`,
		s.tenantID,
		s.repositoryID,
		proof.ResourceType,
		proof.ResourceID,
		proof.OwnerID,
		proof.FencingToken,
	)
	if err != nil {
		return nil, databaseError("postgres.ReconcileAbandonedAttempts.query", err)
	}
	defer rows.Close()
	var abandoned []persistence.Attempt
	for rows.Next() {
		var attempt persistence.Attempt
		if err := rows.Scan(attemptScanTargets(&attempt)...); err != nil {
			return nil, databaseError("postgres.ReconcileAbandonedAttempts.scan", err)
		}
		normalizeAttemptTimes(&attempt)
		abandoned = append(abandoned, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError("postgres.ReconcileAbandonedAttempts.rows", err)
	}
	rows.Close()
	// Match StartAttempt and FinishAttempt: attempt rows first, then the
	// event/outbox watermark, then the live scheduler lease through commit.
	if err := s.prepareAttemptWrite(ctx, tx, proof); err != nil {
		return nil, err
	}
	if replay, found, err := loadAttemptReconciliationReplay(
		ctx, tx, s.tenantID, s.repositoryID, scope, metadata, proof, requestHash,
	); err != nil {
		return nil, err
	} else if found {
		return replay, nil
	}
	now := postgresTimestamp(s.clock.Now())
	reconciled := make([]persistence.Attempt, 0, len(abandoned))
	for _, attempt := range abandoned {
		err := tx.QueryRow(ctx, `
			UPDATE forja.attempts
			SET status='failed_retryable', finished_at=$1,
			    version=version+1, updated_at=$1
			WHERE tenant_id=$2 AND attempt_id=$3
			  AND status IN ('queued', 'running') AND version=$4
			RETURNING attempt_id, run_id, ordinal, status,
			          lease_resource_type, lease_resource_id, worker_id, fencing_token,
			          started_at, finished_at, version, created_at, updated_at`,
			now,
			s.tenantID,
			attempt.AttemptID,
			attempt.Version,
		).Scan(attemptScanTargets(&attempt)...)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fault.New(fault.CodeConflict, "postgres.ReconcileAbandonedAttempts", "attempt changed concurrently")
		}
		if err != nil {
			return nil, databaseError("postgres.ReconcileAbandonedAttempts.update", err)
		}
		normalizeAttemptTimes(&attempt)
		if err := s.appendReconciledAttemptEvent(ctx, tx, attempt, proof, metadata); err != nil {
			return nil, err
		}
		reconciled = append(reconciled, attempt)
	}
	if err := saveAttemptReconciliationReplay(
		ctx, tx, s.tenantID, s.repositoryID, scope, metadata, proof, requestHash, reconciled,
	); err != nil {
		return nil, err
	}
	if err := s.enforceAttemptFence(ctx, tx, proof); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, databaseError("postgres.ReconcileAbandonedAttempts.commit", err)
	}
	return reconciled, nil
}

func (s *Store) lockAttempt(
	ctx context.Context,
	tx pgx.Tx,
	attemptID string,
) (persistence.Attempt, error) {
	attempt, err := scanAttempt(tx.QueryRow(ctx, `
		SELECT a.attempt_id, a.run_id, a.ordinal, a.status,
		       a.lease_resource_type, a.lease_resource_id,
		       a.worker_id, a.fencing_token, a.started_at, a.finished_at,
		       a.version, a.created_at, a.updated_at
		FROM forja.attempts AS a
		JOIN forja.runs AS r
		  ON r.tenant_id=a.tenant_id AND r.run_id=a.run_id
		WHERE a.tenant_id=$1 AND r.repository_id=$2 AND a.attempt_id=$3
		FOR UPDATE OF a`,
		s.tenantID,
		s.repositoryID,
		attemptID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return persistence.Attempt{}, fault.New(fault.CodeNotFound, "postgres.lockAttempt", "attempt was not found")
	}
	if err != nil {
		return persistence.Attempt{}, databaseError("postgres.lockAttempt", err)
	}
	return attempt, nil
}

func (s *Store) prepareAttemptWrite(ctx context.Context, tx pgx.Tx, proof persistence.LeaseProof) error {
	if err := s.lockAttemptWritePrerequisites(ctx, tx); err != nil {
		return err
	}
	return s.verifyAttemptFence(ctx, tx, proof)
}

func (s *Store) lockAttemptWritePrerequisites(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", advisoryLockKey(outboxWatermarkLock)); err != nil {
		return databaseError("postgres.lockAttemptWritePrerequisites", err)
	}
	return nil
}

func (s *Store) appendAttemptEvent(
	ctx context.Context,
	tx pgx.Tx,
	eventType string,
	attempt persistence.Attempt,
	result *contracts.WorkerResult,
	metadata runstate.CommandMetadata,
) error {
	payload := map[string]any{"attempt": attempt}
	if result != nil {
		payload["result"] = map[string]any{
			"task_id":            result.TaskID,
			"adapter":            result.Adapter,
			"status":             result.Status,
			"retryable":          result.Retryable,
			"termination_reason": result.TerminationReason,
			"started_at":         result.StartedAt,
			"finished_at":        result.FinishedAt,
			"duration_ms":        result.DurationMS,
			"exit_code":          result.ExitCode,
			"stdout_sha256":      result.StdoutSHA256,
			"stderr_sha256":      result.StderrSHA256,
			"usage":              result.Usage,
			"evidence_refs":      result.EvidenceRefs,
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fault.Wrap(fault.CodeInternal, "postgres.appendAttemptEvent", "encode attempt event", err)
	}
	return s.appendEvent(ctx, tx, "attempt", attempt.AttemptID, attempt.Version, eventType, attempt.UpdatedAt, encoded, metadata)
}

func (s *Store) appendReconciledAttemptEvent(
	ctx context.Context,
	tx pgx.Tx,
	attempt persistence.Attempt,
	proof persistence.LeaseProof,
	metadata runstate.CommandMetadata,
) error {
	payload, err := json.Marshal(map[string]any{
		"attempt": attempt,
		"reconciled_by": map[string]any{
			"tenant_id":     proof.TenantID,
			"repository_id": proof.RepositoryID,
			"resource_type": proof.ResourceType,
			"resource_id":   proof.ResourceID,
			"owner_id":      proof.OwnerID,
			"fencing_token": proof.FencingToken,
		},
	})
	if err != nil {
		return fault.Wrap(fault.CodeInternal, "postgres.appendReconciledAttemptEvent", "encode event", err)
	}
	return s.appendEvent(
		ctx,
		tx,
		"attempt",
		attempt.AttemptID,
		attempt.Version,
		"attempt.reconciled",
		attempt.UpdatedAt,
		payload,
		metadata,
	)
}

func scanAttempt(row rowScanner) (persistence.Attempt, error) {
	var attempt persistence.Attempt
	err := row.Scan(attemptScanTargets(&attempt)...)
	if err == nil {
		normalizeAttemptTimes(&attempt)
	}
	return attempt, err
}

func attemptScanTargets(attempt *persistence.Attempt) []any {
	return []any{
		&attempt.AttemptID,
		&attempt.RunID,
		&attempt.Ordinal,
		&attempt.Status,
		&attempt.LeaseResourceType,
		&attempt.LeaseResourceID,
		&attempt.WorkerID,
		&attempt.FencingToken,
		&attempt.StartedAt,
		&attempt.FinishedAt,
		&attempt.Version,
		&attempt.CreatedAt,
		&attempt.UpdatedAt,
	}
}

func normalizeAttemptTimes(attempt *persistence.Attempt) {
	attempt.CreatedAt = attempt.CreatedAt.UTC()
	attempt.UpdatedAt = attempt.UpdatedAt.UTC()
	if attempt.StartedAt != nil {
		value := attempt.StartedAt.UTC()
		attempt.StartedAt = &value
	}
	if attempt.FinishedAt != nil {
		value := attempt.FinishedAt.UTC()
		attempt.FinishedAt = &value
	}
}

func validateAttemptLifecycleInput(
	attemptID string,
	expectedVersion int,
	metadata runstate.CommandMetadata,
	proof persistence.LeaseProof,
	store *Store,
) error {
	if err := validateAttemptID(attemptID); err != nil {
		return err
	}
	if expectedVersion < 1 {
		return fault.New(fault.CodeInvalidArgument, "postgres.validateAttemptLifecycleInput", "expected version must be positive")
	}
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return err
	}
	return validateAttemptFence(proof, store.tenantID, store.repositoryID)
}

func validateAttemptID(attemptID string) error {
	if !strings.HasPrefix(attemptID, "attempt_") {
		return fault.New(fault.CodeInvalidArgument, "postgres.validateAttemptID", "invalid attempt ID")
	}
	runID := "run_" + strings.TrimPrefix(attemptID, "attempt_")
	parsed, err := identity.ParseRunID(runID)
	if err != nil || "attempt_"+strings.TrimPrefix(parsed.String(), "run_") != attemptID {
		return fault.New(fault.CodeInvalidArgument, "postgres.validateAttemptID", "invalid attempt ID")
	}
	return nil
}

func attemptOwnedBy(attempt persistence.Attempt, proof persistence.LeaseProof) bool {
	return attempt.LeaseResourceType == proof.ResourceType &&
		attempt.LeaseResourceID == proof.ResourceID &&
		attempt.WorkerID == proof.OwnerID &&
		attempt.FencingToken == proof.FencingToken
}

func validateCompletion(attemptID string, result contracts.WorkerResult) error {
	workerContractOnce.Do(func() {
		workerContracts, workerContractErr = contracts.NewRegistry()
	})
	if workerContractErr != nil {
		return fault.Wrap(fault.CodeInternal, "postgres.FinishAttempt", "compile worker contracts", workerContractErr)
	}
	if !utf8.ValidString(result.Stdout) || !utf8.ValidString(result.Stderr) {
		return fault.New(fault.CodeInvalidArgument, "postgres.FinishAttempt", "worker output must be valid UTF-8")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return fault.Wrap(fault.CodeInvalidArgument, "postgres.FinishAttempt", "encode worker result", err)
	}
	if err := workerContracts.ValidateJSON("worker-result.schema.json", encoded); err != nil {
		return fault.Wrap(fault.CodeInvalidArgument, "postgres.FinishAttempt", "worker result violates its contract", err)
	}
	if result.AttemptID != attemptID || result.SchemaVersion != "1.0" ||
		!slices.Contains(terminalAttemptStatuses, result.Status) ||
		result.StartedAt.IsZero() || result.FinishedAt.IsZero() ||
		result.FinishedAt.Before(result.StartedAt) || result.DurationMS < 0 {
		return fault.New(fault.CodeInvalidArgument, "postgres.FinishAttempt", "invalid terminal worker result")
	}
	for _, digest := range []string{result.StdoutSHA256, result.StderrSHA256} {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != 32 {
			return fault.New(fault.CodeInvalidArgument, "postgres.FinishAttempt", "invalid output digest")
		}
	}
	stdoutDigest := sha256.Sum256([]byte(result.Stdout))
	stderrDigest := sha256.Sum256([]byte(result.Stderr))
	if result.StdoutSHA256 != hex.EncodeToString(stdoutDigest[:]) ||
		result.StderrSHA256 != hex.EncodeToString(stderrDigest[:]) {
		return fault.New(fault.CodeInvalidArgument, "postgres.FinishAttempt", "output digest does not match captured output")
	}
	if result.Usage.InputTokens < 0 || result.Usage.CachedInputTokens < 0 ||
		result.Usage.OutputTokens < 0 || result.Usage.ToolCalls < 0 {
		return fault.New(fault.CodeInvalidArgument, "postgres.FinishAttempt", "invalid worker usage")
	}
	return nil
}

func workerResultHashParts(result contracts.WorkerResult) []string {
	exitCode := "null"
	if result.ExitCode != nil {
		exitCode = strconv.Itoa(*result.ExitCode)
	}
	evidence, _ := json.Marshal(result.EvidenceRefs)
	return []string{
		result.TaskID,
		result.Adapter,
		result.Status,
		strconv.FormatBool(result.Retryable),
		result.TerminationReason,
		result.StartedAt.UTC().Format(time.RFC3339Nano),
		result.FinishedAt.UTC().Format(time.RFC3339Nano),
		strconv.FormatInt(result.DurationMS, 10),
		exitCode,
		result.StdoutSHA256,
		result.StderrSHA256,
		strconv.Itoa(result.Usage.InputTokens),
		strconv.Itoa(result.Usage.CachedInputTokens),
		strconv.Itoa(result.Usage.OutputTokens),
		strconv.Itoa(result.Usage.ToolCalls),
		string(evidence),
	}
}

type attemptReconciliationReplay struct {
	Attempts  []persistence.Attempt          `json:"attempts"`
	Authority attemptReconciliationAuthority `json:"authority"`
	Command   attemptReconciliationCommand   `json:"command"`
}

type attemptReconciliationAuthority struct {
	TenantID     string `json:"tenant_id"`
	RepositoryID string `json:"repository_id"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	OwnerID      string `json:"owner_id"`
	FencingToken int64  `json:"fencing_token"`
}

type attemptReconciliationCommand struct {
	TenantID       string `json:"tenant_id"`
	RepositoryID   string `json:"repository_id"`
	IdempotencyKey string `json:"idempotency_key"`
	ActorType      string `json:"actor_type"`
	ActorID        string `json:"actor_id"`
	CorrelationID  string `json:"correlation_id"`
	CausationID    string `json:"causation_id"`
}

func loadAttemptReconciliationReplay(
	ctx context.Context,
	tx pgx.Tx,
	tenantID string,
	repositoryID string,
	scope string,
	metadata runstate.CommandMetadata,
	proof persistence.LeaseProof,
	requestHash []byte,
) ([]persistence.Attempt, bool, error) {
	var storedHash []byte
	var response []byte
	err := tx.QueryRow(ctx, `
		SELECT request_hash, response_body
		FROM forja.idempotency_keys
		WHERE tenant_id=$1 AND scope=$2 AND idempotency_key=$3`,
		tenantID,
		scope,
		metadata.IdempotencyKey,
	).Scan(&storedHash, &response)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, databaseError("postgres.loadAttemptReconciliationReplay", err)
	}
	if !bytes.Equal(storedHash, requestHash) {
		return nil, false, fault.New(fault.CodeConflict, "postgres.loadAttemptReconciliationReplay", "idempotency key was reused")
	}
	decoder := json.NewDecoder(bytes.NewReader(response))
	decoder.DisallowUnknownFields()
	var replay attemptReconciliationReplay
	if err := decoder.Decode(&replay); err != nil {
		return nil, false, fault.Wrap(fault.CodeInternal, "postgres.loadAttemptReconciliationReplay", "decode response", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, false, fault.New(fault.CodeInternal, "postgres.loadAttemptReconciliationReplay", "stored response has extra JSON")
	}
	expected := newAttemptReconciliationReplay(
		tenantID, repositoryID, metadata, proof, replay.Attempts,
	)
	if replay.Attempts == nil || replay.Authority != expected.Authority ||
		!sameStableReconciliationCommand(replay.Command, expected.Command) {
		return nil, false, fault.New(
			fault.CodeInternal,
			"postgres.loadAttemptReconciliationReplay",
			"stored reconciliation provenance is invalid",
		)
	}
	for index := range replay.Attempts {
		encoded, _ := json.Marshal(replay.Attempts[index])
		validated, err := decodeStoredAttempt(encoded)
		if err != nil {
			return nil, false, fault.Wrap(fault.CodeInternal, "postgres.loadAttemptReconciliationReplay", "validate stored attempt", err)
		}
		replay.Attempts[index] = validated
	}
	return replay.Attempts, true, nil
}

func sameStableReconciliationCommand(
	stored attemptReconciliationCommand,
	current attemptReconciliationCommand,
) bool {
	return stored.TenantID == current.TenantID &&
		stored.RepositoryID == current.RepositoryID &&
		stored.IdempotencyKey == current.IdempotencyKey &&
		stored.ActorType == current.ActorType &&
		stored.ActorID == current.ActorID &&
		stored.CausationID == current.CausationID &&
		strings.TrimSpace(stored.CorrelationID) != ""
}

func saveAttemptReconciliationReplay(
	ctx context.Context,
	tx pgx.Tx,
	tenantID string,
	repositoryID string,
	scope string,
	metadata runstate.CommandMetadata,
	proof persistence.LeaseProof,
	requestHash []byte,
	attempts []persistence.Attempt,
) error {
	replay := newAttemptReconciliationReplay(
		tenantID, repositoryID, metadata, proof, attempts,
	)
	response, err := json.Marshal(replay)
	if err != nil {
		return fault.Wrap(fault.CodeInternal, "postgres.saveAttemptReconciliationReplay", "encode response", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.idempotency_keys (
			tenant_id, scope, idempotency_key, request_hash,
			response_status, response_body
		) VALUES ($1, $2, $3, $4, 200, $5)`,
		tenantID,
		scope,
		metadata.IdempotencyKey,
		requestHash,
		response,
	); err != nil {
		return databaseError("postgres.saveAttemptReconciliationReplay", err)
	}
	return nil
}

func newAttemptReconciliationReplay(
	tenantID string,
	repositoryID string,
	metadata runstate.CommandMetadata,
	proof persistence.LeaseProof,
	attempts []persistence.Attempt,
) attemptReconciliationReplay {
	causationID := ""
	if metadata.CausationID != nil {
		causationID = *metadata.CausationID
	}
	if attempts == nil {
		attempts = []persistence.Attempt{}
	}
	return attemptReconciliationReplay{
		Attempts: attempts,
		Authority: attemptReconciliationAuthority{
			TenantID: tenantID, RepositoryID: repositoryID,
			ResourceType: proof.ResourceType, ResourceID: proof.ResourceID,
			OwnerID: proof.OwnerID, FencingToken: proof.FencingToken,
		},
		Command: attemptReconciliationCommand{
			TenantID: tenantID, RepositoryID: repositoryID,
			IdempotencyKey: metadata.IdempotencyKey,
			ActorType:      metadata.ActorType, ActorID: metadata.ActorID,
			CorrelationID: metadata.CorrelationID, CausationID: causationID,
		},
	}
}

var _ persistence.AttemptLifecycleRepository = (*Store)(nil)
