package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

// CreateAttempt allocates a monotonic run-local ordinal exactly once for an
// idempotent command.
func (s *Store) CreateAttempt(
	ctx context.Context,
	runID identity.RunID,
	status string,
	metadata runstate.CommandMetadata,
	proof persistence.LeaseProof,
) (persistence.Attempt, error) {
	if length := utf8.RuneCountInString(status); length < 1 || length > 100 {
		return persistence.Attempt{}, fault.New(
			fault.CodeInvalidArgument,
			"postgres.CreateAttempt",
			"status length must be between 1 and 100 characters",
		)
	}
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return persistence.Attempt{}, err
	}
	if err := validateAttemptFence(proof, s.tenantID, s.repositoryID); err != nil {
		return persistence.Attempt{}, err
	}
	scope := "create_attempt:" + s.repositoryID + ":" + runID.String()
	requestHash := hashCommand(
		metadata,
		scope,
		status,
		proof.ResourceType,
		proof.ResourceID,
		proof.OwnerID,
		fmt.Sprint(proof.FencingToken),
	)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return persistence.Attempt{}, databaseError("postgres.CreateAttempt.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return persistence.Attempt{}, err
	}
	var runExists bool
	if err := tx.QueryRow(ctx, `
		SELECT true FROM forja.runs
		WHERE tenant_id=$1 AND repository_id=$2 AND run_id=$3
		FOR UPDATE`,
		s.tenantID,
		s.repositoryID,
		runID.String(),
	).Scan(&runExists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return persistence.Attempt{}, fault.New(
				fault.CodeNotFound,
				"postgres.CreateAttempt",
				fmt.Sprintf("run %s was not found", runID),
			)
		}
		return persistence.Attempt{}, databaseError("postgres.CreateAttempt.lockRun", err)
	}
	// Acquire every known contended write lock before checking lease liveness.
	// The final fenced UPDATE takes an exclusive lease-row lock through commit,
	// so a replacement owner cannot overlap this protected transaction.
	if _, err := tx.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock($1)",
		advisoryLockKey(outboxWatermarkLock),
	); err != nil {
		return persistence.Attempt{}, databaseError(
			"postgres.CreateAttempt.lockWatermark",
			err,
		)
	}
	if err := s.verifyAttemptFence(ctx, tx, proof); err != nil {
		return persistence.Attempt{}, err
	}
	if replay, found, err := loadAttemptReplay(
		ctx,
		tx,
		s.tenantID,
		scope,
		metadata.IdempotencyKey,
		requestHash,
	); err != nil {
		return persistence.Attempt{}, err
	} else if found {
		return replay, nil
	}
	attemptID, err := newPrefixedID("attempt")
	if err != nil {
		return persistence.Attempt{}, fault.Wrap(
			fault.CodeInternal,
			"postgres.CreateAttempt",
			"generate attempt ID",
			err,
		)
	}
	attempt := persistence.Attempt{
		AttemptID:         attemptID,
		RunID:             runID.String(),
		Status:            status,
		LeaseResourceType: proof.ResourceType,
		LeaseResourceID:   proof.ResourceID,
		WorkerID:          proof.OwnerID,
		FencingToken:      proof.FencingToken,
		Version:           1,
	}
	err = tx.QueryRow(ctx, `
		WITH next_ordinal AS (
			SELECT COALESCE(max(ordinal), 0)+1 AS ordinal
			FROM forja.attempts
			WHERE tenant_id=$2 AND run_id=$3
		), live_fence AS (
			SELECT true
			FROM forja.leases
			WHERE tenant_id=$2 AND repository_id=$7
			  AND resource_type=$8 AND resource_id=$9
			  AND owner_id=$5 AND fencing_token=$6
			  AND expires_at > clock_timestamp()
		)
		INSERT INTO forja.attempts (
			attempt_id, tenant_id, run_id, ordinal, status,
			lease_resource_type, lease_resource_id,
			worker_id, fencing_token, version
		)
		SELECT $1, $2, $3, n.ordinal, $4, $8, $9, $5, $6, 1
		FROM next_ordinal AS n
		CROSS JOIN live_fence
		RETURNING ordinal, created_at`,
		attempt.AttemptID,
		s.tenantID,
		attempt.RunID,
		attempt.Status,
		proof.OwnerID,
		proof.FencingToken,
		s.repositoryID,
		proof.ResourceType,
		proof.ResourceID,
	).Scan(&attempt.Ordinal, &attempt.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistence.Attempt{}, fault.New(
			fault.CodeConflict,
			"postgres.CreateAttempt",
			"scheduler lease expired before the protected write",
		)
	}
	if err != nil {
		return persistence.Attempt{}, databaseError("postgres.CreateAttempt.insert", err)
	}
	attempt.CreatedAt = attempt.CreatedAt.UTC()
	payload, err := json.Marshal(attempt)
	if err != nil {
		return persistence.Attempt{}, fault.Wrap(
			fault.CodeInternal,
			"postgres.CreateAttempt",
			"encode attempt event payload",
			err,
		)
	}
	if err := s.appendEvent(
		ctx,
		tx,
		"attempt",
		attempt.AttemptID,
		attempt.Version,
		"attempt.created",
		attempt.CreatedAt,
		payload,
		metadata,
	); err != nil {
		return persistence.Attempt{}, err
	}
	if err := saveAttemptReplay(
		ctx,
		tx,
		s.tenantID,
		scope,
		metadata.IdempotencyKey,
		requestHash,
		attempt,
	); err != nil {
		return persistence.Attempt{}, err
	}
	if err := s.enforceAttemptFence(ctx, tx, proof); err != nil {
		return persistence.Attempt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return persistence.Attempt{}, databaseError("postgres.CreateAttempt.commit", err)
	}
	return attempt, nil
}

func (s *Store) verifyAttemptFence(
	ctx context.Context,
	tx pgx.Tx,
	proof persistence.LeaseProof,
) error {
	var leaseExpiresAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT expires_at
		FROM forja.leases
		WHERE tenant_id=$1 AND repository_id=$2
		  AND resource_type=$3 AND resource_id=$4
		  AND owner_id=$5 AND fencing_token=$6
		  AND expires_at > clock_timestamp()
		FOR SHARE`,
		proof.TenantID,
		s.repositoryID,
		proof.ResourceType,
		proof.ResourceID,
		proof.OwnerID,
		proof.FencingToken,
	).Scan(&leaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return fault.New(
			fault.CodeConflict,
			"postgres.CreateAttempt",
			"scheduler lease is expired, replaced, or owned by another worker",
		)
	}
	if err != nil {
		return databaseError("postgres.CreateAttempt.verifyFence", err)
	}
	return nil
}

func (s *Store) enforceAttemptFence(
	ctx context.Context,
	tx pgx.Tx,
	proof persistence.LeaseProof,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE forja.leases
		SET updated_at=updated_at
		WHERE tenant_id=$1 AND repository_id=$2
		  AND resource_type=$3 AND resource_id=$4
		  AND owner_id=$5 AND fencing_token=$6
		  AND expires_at > clock_timestamp()`,
		proof.TenantID,
		proof.RepositoryID,
		proof.ResourceType,
		proof.ResourceID,
		proof.OwnerID,
		proof.FencingToken,
	)
	if err != nil {
		return databaseError("postgres.CreateAttempt.finalFence", err)
	}
	if tag.RowsAffected() != 1 {
		return fault.New(
			fault.CodeConflict,
			"postgres.CreateAttempt",
			"scheduler lease expired before commit",
		)
	}
	return nil
}

func validateAttemptFence(
	proof persistence.LeaseProof,
	tenantID string,
	repositoryID string,
) error {
	if proof.TenantID != tenantID ||
		proof.RepositoryID != repositoryID ||
		proof.ResourceType != "scheduler" ||
		proof.ResourceID == "" ||
		proof.OwnerID == "" ||
		proof.FencingToken < 1 {
		return fault.New(
			fault.CodeInvalidArgument,
			"postgres.CreateAttempt",
			"a valid scheduler lease proof is required",
		)
	}
	return nil
}

func loadAttemptReplay(
	ctx context.Context,
	tx pgx.Tx,
	tenantID string,
	scope string,
	key string,
	requestHash []byte,
) (persistence.Attempt, bool, error) {
	var storedHash []byte
	var response []byte
	err := tx.QueryRow(ctx, `
		SELECT request_hash, response_body
		FROM forja.idempotency_keys
		WHERE tenant_id=$1 AND scope=$2 AND idempotency_key=$3`,
		tenantID,
		scope,
		key,
	).Scan(&storedHash, &response)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistence.Attempt{}, false, nil
	}
	if err != nil {
		return persistence.Attempt{}, false, databaseError("postgres.loadAttemptReplay", err)
	}
	if !bytes.Equal(storedHash, requestHash) {
		return persistence.Attempt{}, false, fault.New(
			fault.CodeConflict,
			"postgres.loadAttemptReplay",
			"idempotency key was already used for a different command",
		)
	}
	attempt, err := decodeStoredAttempt(response)
	if err != nil {
		return persistence.Attempt{}, false, fault.Wrap(
			fault.CodeInternal,
			"postgres.loadAttemptReplay",
			"decode stored command response",
			err,
		)
	}
	return attempt, true, nil
}

func decodeStoredAttempt(data []byte) (persistence.Attempt, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var attempt persistence.Attempt
	if err := decoder.Decode(&attempt); err != nil {
		return persistence.Attempt{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON documents")
		}
		return persistence.Attempt{}, err
	}
	runID, err := identity.ParseRunID(attempt.RunID)
	if err != nil || runID.String() != attempt.RunID {
		return persistence.Attempt{}, fmt.Errorf("invalid stored attempt run ID")
	}
	if !strings.HasPrefix(attempt.AttemptID, "attempt_") {
		return persistence.Attempt{}, fmt.Errorf("invalid stored attempt ID")
	}
	attemptUUID := "run_" + strings.TrimPrefix(attempt.AttemptID, "attempt_")
	if parsed, err := identity.ParseRunID(attemptUUID); err != nil ||
		"attempt_"+strings.TrimPrefix(parsed.String(), "run_") != attempt.AttemptID {
		return persistence.Attempt{}, fmt.Errorf("invalid stored attempt ID")
	}
	if length := utf8.RuneCountInString(attempt.Status); length < 1 || length > 100 {
		return persistence.Attempt{}, fmt.Errorf("invalid stored attempt status")
	}
	if attempt.Ordinal < 1 ||
		attempt.LeaseResourceType != "scheduler" ||
		attempt.LeaseResourceID == "" ||
		attempt.WorkerID == "" ||
		attempt.FencingToken < 1 ||
		attempt.Version != 1 ||
		!runstate.ValidTimestamp(attempt.CreatedAt) {
		return persistence.Attempt{}, fmt.Errorf(
			"invalid stored attempt ordinal, version, or timestamp",
		)
	}
	return attempt, nil
}

func saveAttemptReplay(
	ctx context.Context,
	tx pgx.Tx,
	tenantID string,
	scope string,
	key string,
	requestHash []byte,
	attempt persistence.Attempt,
) error {
	response, err := json.Marshal(attempt)
	if err != nil {
		return fault.Wrap(
			fault.CodeInternal,
			"postgres.saveAttemptReplay",
			"encode command response",
			err,
		)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.idempotency_keys (
			tenant_id, scope, idempotency_key, request_hash,
			response_status, response_body
		) VALUES ($1, $2, $3, $4, 201, $5)`,
		tenantID,
		scope,
		key,
		requestHash,
		response,
	); err != nil {
		return databaseError("postgres.saveAttemptReplay", err)
	}
	return nil
}
