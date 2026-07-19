// Package postgres implements Forja's canonical PostgreSQL repositories.
package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rvbernucci/forja-guide/internal/clock"
	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

const (
	// DefaultTenantID and DefaultRepositoryID bootstrap the single-user runtime.
	DefaultTenantID     = control.LocalTenantID
	DefaultRepositoryID = control.LocalRepositoryID
	outboxWatermarkLock = "forja:event-outbox-commit-order:v1"
)

// Store implements durable run, lease, outbox, and projection repositories.
type Store struct {
	pool         *pgxpool.Pool
	clock        clock.Clock
	machine      *runstate.Machine
	tenantID     string
	repositoryID string
}

// Open validates a pool configuration and establishes bounded connections.
func Open(
	ctx context.Context,
	databaseURL string,
	maxConnections int32,
	queryTracers ...pgx.QueryTracer,
) (*pgxpool.Pool, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, fault.New(
			fault.CodeInvalidArgument,
			"postgres.Open",
			"database URL is required",
		)
	}
	if maxConnections < 1 || maxConnections > 100 {
		return nil, fault.New(
			fault.CodeInvalidArgument,
			"postgres.Open",
			"max connections must be between 1 and 100",
		)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fault.Wrap(
			fault.CodeInvalidArgument,
			"postgres.Open",
			"parse database URL",
			err,
		)
	}
	config.MaxConns = maxConnections
	if len(queryTracers) > 1 {
		return nil, fault.New(
			fault.CodeInvalidArgument,
			"postgres.Open",
			"at most one PostgreSQL query tracer is supported",
		)
	}
	if len(queryTracers) == 1 {
		config.ConnConfig.Tracer = queryTracers[0]
	}
	config.MinConns = 0
	config.MaxConnIdleTime = 5 * time.Minute
	config.MaxConnLifetime = time.Hour
	config.HealthCheckPeriod = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fault.Wrap(
			fault.CodeUnavailable,
			"postgres.Open",
			"create connection pool",
			err,
		)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fault.Wrap(
			fault.CodeUnavailable,
			"postgres.Open",
			"ping database",
			err,
		)
	}
	return pool, nil
}

// NewStore binds repositories to one tenant and repository authority.
func NewStore(
	pool *pgxpool.Pool,
	source clock.Clock,
	tenantID string,
	repositoryID string,
) (*Store, error) {
	if pool == nil {
		return nil, fault.New(
			fault.CodeInvalidArgument,
			"postgres.NewStore",
			"pool is required",
		)
	}
	if source == nil {
		source = clock.Real{}
	}
	if tenantID == "" || repositoryID == "" {
		return nil, fault.New(
			fault.CodeInvalidArgument,
			"postgres.NewStore",
			"tenant and repository IDs are required",
		)
	}
	return &Store{
		pool:         pool,
		clock:        source,
		machine:      runstate.NewMachine(source),
		tenantID:     tenantID,
		repositoryID: repositoryID,
	}, nil
}

// Ready reports whether the canonical database can accept requests.
func (s *Store) Ready(ctx context.Context) error {
	if err := VerifySchema(
		ctx,
		s.pool,
		s.tenantID,
		s.repositoryID,
	); err != nil {
		return fault.Wrap(
			fault.CodeUnavailable,
			"postgres.Ready",
			"canonical schema is not ready",
			err,
		)
	}
	return nil
}

// CreateRun atomically commits an aggregate, immutable event, outbox row, and
// replay receipt.
func (s *Store) CreateRun(
	ctx context.Context,
	id identity.RunID,
	objective string,
	metadata runstate.CommandMetadata,
) (contracts.Run, error) {
	if err := validateObjective(objective); err != nil {
		return contracts.Run{}, err
	}
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return contracts.Run{}, err
	}
	// The generated aggregate ID is an outcome, not part of the caller's
	// command identity. Retries may arrive after the daemon generates a new
	// candidate ID and must still replay the first committed response.
	scope := "create_run:" + s.repositoryID
	requestHash := hashCommand(
		metadata,
		"create_run",
		s.repositoryID,
		objective,
	)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contracts.Run{}, databaseError("postgres.CreateRun.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return contracts.Run{}, err
	}
	if replay, found, err := loadRunReplay(
		ctx,
		tx,
		s.tenantID,
		scope,
		metadata.IdempotencyKey,
		requestHash,
	); err != nil {
		return contracts.Run{}, err
	} else if found {
		return replay, nil
	}

	now := postgresTimestamp(s.clock.Now())
	run := contracts.Run{
		RunID:         id.String(),
		SchemaVersion: "1.0",
		Objective:     objective,
		State:         string(runstate.StateDraft),
		Version:       1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.runs (
			run_id, tenant_id, repository_id, objective, state, version,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		run.RunID,
		s.tenantID,
		s.repositoryID,
		run.Objective,
		run.State,
		run.Version,
		run.CreatedAt,
		run.UpdatedAt,
	); err != nil {
		return contracts.Run{}, databaseError("postgres.CreateRun.insert", err)
	}
	if err := s.appendRunEvent(
		ctx,
		tx,
		"run.created",
		run,
		metadata,
	); err != nil {
		return contracts.Run{}, err
	}
	if err := saveRunReplay(
		ctx,
		tx,
		s.tenantID,
		scope,
		metadata.IdempotencyKey,
		requestHash,
		201,
		run,
	); err != nil {
		return contracts.Run{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.Run{}, databaseError("postgres.CreateRun.commit", err)
	}
	return run, nil
}

// GetRun reads the canonical aggregate.
func (s *Store) GetRun(ctx context.Context, id identity.RunID) (contracts.Run, error) {
	run, err := scanRun(s.pool.QueryRow(ctx, `
		SELECT run_id::text, objective, state, version, created_at, updated_at
		FROM forja.runs
		WHERE tenant_id=$1 AND repository_id=$2 AND run_id=$3`,
		s.tenantID,
		s.repositoryID,
		id.String(),
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.Run{}, fault.New(
				fault.CodeNotFound,
				"postgres.GetRun",
				fmt.Sprintf("run %s was not found", id),
			)
		}
		return contracts.Run{}, databaseError("postgres.GetRun", err)
	}
	return run, nil
}

// TransitionRun applies optimistic concurrency and event persistence in one
// transaction.
func (s *Store) TransitionRun(
	ctx context.Context,
	id identity.RunID,
	expectedVersion int,
	target runstate.State,
	metadata runstate.CommandMetadata,
) (contracts.Run, error) {
	if expectedVersion < 1 {
		return contracts.Run{}, fault.New(
			fault.CodeInvalidArgument,
			"postgres.TransitionRun",
			"expected version must be at least 1",
		)
	}
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return contracts.Run{}, err
	}
	requestHash := hashCommand(
		metadata,
		"transition_run",
		id.String(),
		fmt.Sprint(expectedVersion),
		string(target),
	)
	scope := "transition_run:" + s.repositoryID + ":" + id.String()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contracts.Run{}, databaseError("postgres.TransitionRun.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return contracts.Run{}, err
	}
	if replay, found, err := loadRunReplay(
		ctx,
		tx,
		s.tenantID,
		scope,
		metadata.IdempotencyKey,
		requestHash,
	); err != nil {
		return contracts.Run{}, err
	} else if found {
		if err := s.appendSuccessToolAudit(ctx, tx, metadata, scope, postgresTimestamp(s.clock.Now()), true); err != nil {
			return contracts.Run{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return contracts.Run{}, databaseError("postgres.TransitionRun.commitReplayAudit", err)
		}
		return replay, nil
	}

	// Decision resolution locks Sprint before Run. Generic transitions use the
	// same order so the two command paths cannot form a lock cycle.
	sprint, sprintErr := scanSprint(tx.QueryRow(ctx, `
			SELECT 'sprint_' || sp.sprint_id::text, sp.sequence_number, sp.title,
			       sp.objective, sp.status, sp.version, sp.run_id,
			       (
			         SELECT d.decision_id
			         FROM forja.decisions AS d
			         WHERE d.tenant_id=sp.tenant_id
			           AND d.repository_id=sp.repository_id
			           AND d.sprint_id=sp.sprint_id
			           AND d.status='pending'
			         LIMIT 1
			       ),
			       sp.created_at, sp.updated_at
			FROM forja.sprints AS sp
			WHERE sp.tenant_id=$1 AND sp.repository_id=$2 AND sp.run_id=$3
			FOR UPDATE OF sp`,
		s.tenantID,
		s.repositoryID,
		id.String(),
	))
	run, err := scanRun(tx.QueryRow(ctx, `
		SELECT run_id::text, objective, state, version, created_at, updated_at
		FROM forja.runs
		WHERE tenant_id=$1 AND repository_id=$2 AND run_id=$3
		FOR UPDATE`,
		s.tenantID,
		s.repositoryID,
		id.String(),
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.Run{}, fault.New(
				fault.CodeNotFound,
				"postgres.TransitionRun",
				fmt.Sprintf("run %s was not found", id),
			)
		}
		return contracts.Run{}, databaseError("postgres.TransitionRun.select", err)
	}
	if run.Version != expectedVersion {
		return contracts.Run{}, fault.New(
			fault.CodeConflict,
			"postgres.TransitionRun",
			fmt.Sprintf(
				"run %s version mismatch: expected %d, current %d",
				id,
				expectedVersion,
				run.Version,
			),
		)
	}
	if isPrivilegedResumeTransition(run.State, target) {
		return contracts.Run{}, fault.New(
			fault.CodePermissionDenied,
			"postgres.TransitionRun",
			"resume transitions require the governed ResumeRun command",
		)
	}
	var cancellingSprint *control.Sprint
	switch {
	case errors.Is(sprintErr, pgx.ErrNoRows):
	case sprintErr != nil:
		return contracts.Run{}, databaseError("postgres.TransitionRun.selectSprint", sprintErr)
	case sprint.Status == string(control.SprintAwaitingApproval) || sprint.PendingDecisionID != nil:
		return contracts.Run{}, fault.New(
			fault.CodeConflict,
			"postgres.TransitionRun",
			"resolve the pending decision before transitioning its run",
		)
	case sprint.Status == string(control.SprintProposed) && target != runstate.StateCancelling:
		return contracts.Run{}, fault.New(
			fault.CodeConflict,
			"postgres.TransitionRun",
			"submit the proposed Sprint before transitioning its run",
		)
	case target == runstate.StateCancelling:
		cancellingSprint = &sprint
	}
	if err := s.requireRunPublicationTransition(
		ctx, tx, id.String(), target, "postgres.TransitionRun",
	); err != nil {
		return contracts.Run{}, err
	}
	updated, err := s.machine.Transition(run, target)
	if err != nil {
		return contracts.Run{}, err
	}
	updated.UpdatedAt = postgresTimestamp(updated.UpdatedAt)
	tag, err := tx.Exec(ctx, `
		UPDATE forja.runs
		SET state=$1, version=$2, updated_at=$3
		WHERE tenant_id=$4 AND repository_id=$5 AND run_id=$6 AND version=$7`,
		updated.State,
		updated.Version,
		updated.UpdatedAt,
		s.tenantID,
		s.repositoryID,
		updated.RunID,
		expectedVersion,
	)
	if err != nil {
		return contracts.Run{}, databaseError("postgres.TransitionRun.update", err)
	}
	if tag.RowsAffected() != 1 {
		return contracts.Run{}, fault.New(
			fault.CodeConflict,
			"postgres.TransitionRun",
			"run version changed concurrently",
		)
	}
	if cancellingSprint != nil &&
		(cancellingSprint.Status == string(control.SprintProposed) ||
			cancellingSprint.Status == string(control.SprintApproved)) {
		previousVersion := cancellingSprint.Version
		cancellingSprint.Status = string(control.SprintCancelling)
		cancellingSprint.Version++
		cancellingSprint.UpdatedAt = updated.UpdatedAt
		tag, err = tx.Exec(ctx, `
			UPDATE forja.sprints
			SET status=$1, version=$2, updated_at=$3
			WHERE tenant_id=$4 AND repository_id=$5
			  AND sprint_id=$6::uuid AND version=$7`,
			cancellingSprint.Status,
			cancellingSprint.Version,
			cancellingSprint.UpdatedAt,
			s.tenantID,
			s.repositoryID,
			strings.TrimPrefix(cancellingSprint.SprintID, "sprint_"),
			previousVersion,
		)
		if err != nil {
			return contracts.Run{}, databaseError("postgres.TransitionRun.updateSprint", err)
		}
		if tag.RowsAffected() != 1 {
			return contracts.Run{}, fault.New(
				fault.CodeConflict,
				"postgres.TransitionRun.updateSprint",
				"sprint version changed concurrently",
			)
		}
		if err := s.appendControlEvent(
			ctx,
			tx,
			"sprint",
			cancellingSprint.SprintID,
			cancellingSprint.Version,
			"sprint.cancellation_requested",
			cancellingSprint.UpdatedAt,
			*cancellingSprint,
			metadata,
		); err != nil {
			return contracts.Run{}, err
		}
	}
	if err := s.appendRunEvent(
		ctx,
		tx,
		"run.transitioned",
		updated,
		metadata,
	); err != nil {
		return contracts.Run{}, err
	}
	if err := s.appendSuccessToolAudit(ctx, tx, metadata, scope, updated.UpdatedAt, false); err != nil {
		return contracts.Run{}, err
	}
	if err := saveRunReplay(
		ctx,
		tx,
		s.tenantID,
		scope,
		metadata.IdempotencyKey,
		requestHash,
		200,
		updated,
	); err != nil {
		return contracts.Run{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.Run{}, databaseError("postgres.TransitionRun.commit", err)
	}
	return updated, nil
}

// ResumeRun atomically derives the permitted resume target and stores a
// command-specific receipt before another controller can advance the Run.
func (s *Store) ResumeRun(
	ctx context.Context,
	id identity.RunID,
	expectedVersion int,
	metadata runstate.CommandMetadata,
) (contracts.Run, error) {
	if expectedVersion < 1 {
		return contracts.Run{}, fault.New(
			fault.CodeInvalidArgument,
			"postgres.ResumeRun",
			"expected version must be at least 1",
		)
	}
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return contracts.Run{}, err
	}
	requestHash := hashCommand(
		metadata,
		"resume_run",
		id.String(),
		fmt.Sprint(expectedVersion),
	)
	scope := "resume_run:" + s.repositoryID + ":" + id.String()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contracts.Run{}, databaseError("postgres.ResumeRun.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return contracts.Run{}, err
	}
	if replay, found, err := loadRunReplay(
		ctx,
		tx,
		s.tenantID,
		scope,
		metadata.IdempotencyKey,
		requestHash,
	); err != nil {
		return contracts.Run{}, err
	} else if found {
		if err := s.appendSuccessToolAudit(ctx, tx, metadata, scope, postgresTimestamp(s.clock.Now()), true); err != nil {
			return contracts.Run{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return contracts.Run{}, databaseError("postgres.ResumeRun.commitReplayAudit", err)
		}
		return replay, nil
	}
	run, err := scanRun(tx.QueryRow(ctx, `
		SELECT run_id::text, objective, state, version, created_at, updated_at
		FROM forja.runs
		WHERE tenant_id=$1 AND repository_id=$2 AND run_id=$3
		FOR UPDATE`,
		s.tenantID,
		s.repositoryID,
		id.String(),
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.Run{}, fault.New(fault.CodeNotFound, "postgres.ResumeRun", fmt.Sprintf("run %s was not found", id))
		}
		return contracts.Run{}, databaseError("postgres.ResumeRun.select", err)
	}
	if run.Version != expectedVersion {
		return contracts.Run{}, fault.New(
			fault.CodeConflict,
			"postgres.ResumeRun",
			fmt.Sprintf("run %s version mismatch: expected %d, current %d", id, expectedVersion, run.Version),
		)
	}
	var pending bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM forja.decisions
			WHERE tenant_id=$1 AND repository_id=$2 AND run_id=$3 AND status='pending'
		)`, s.tenantID, s.repositoryID, id.String()).Scan(&pending); err != nil {
		return contracts.Run{}, databaseError("postgres.ResumeRun.pendingDecision", err)
	}
	if pending {
		return contracts.Run{}, fault.New(
			fault.CodeConflict,
			"postgres.ResumeRun",
			"resolve the pending decision before resuming its run",
		)
	}
	target, err := resumeTarget(run)
	if err != nil {
		return contracts.Run{}, err
	}
	if err := s.requireRunPublicationTransition(
		ctx, tx, id.String(), target, "postgres.ResumeRun",
	); err != nil {
		return contracts.Run{}, err
	}
	updated, err := s.machine.Transition(run, target)
	if err != nil {
		return contracts.Run{}, err
	}
	updated.UpdatedAt = postgresTimestamp(updated.UpdatedAt)
	tag, err := tx.Exec(ctx, `
		UPDATE forja.runs
		SET state=$1, version=$2, updated_at=$3
		WHERE tenant_id=$4 AND repository_id=$5 AND run_id=$6 AND version=$7`,
		updated.State,
		updated.Version,
		updated.UpdatedAt,
		s.tenantID,
		s.repositoryID,
		updated.RunID,
		expectedVersion,
	)
	if err != nil {
		return contracts.Run{}, databaseError("postgres.ResumeRun.update", err)
	}
	if tag.RowsAffected() != 1 {
		return contracts.Run{}, fault.New(fault.CodeConflict, "postgres.ResumeRun", "run version changed concurrently")
	}
	if err := s.appendRunEvent(ctx, tx, "run.transitioned", updated, metadata); err != nil {
		return contracts.Run{}, err
	}
	if err := s.appendSuccessToolAudit(ctx, tx, metadata, scope, updated.UpdatedAt, false); err != nil {
		return contracts.Run{}, err
	}
	if err := saveRunReplay(
		ctx,
		tx,
		s.tenantID,
		scope,
		metadata.IdempotencyKey,
		requestHash,
		200,
		updated,
	); err != nil {
		return contracts.Run{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.Run{}, databaseError("postgres.ResumeRun.commit", err)
	}
	return updated, nil
}

func (s *Store) requireRunPublicationTransition(
	ctx context.Context,
	tx pgx.Tx,
	runID string,
	target runstate.State,
	operation string,
) error {
	var publicationTableExists bool
	if err := tx.QueryRow(
		ctx, "SELECT to_regclass('forja.delivery_publications') IS NOT NULL",
	).Scan(&publicationTableExists); err != nil {
		return databaseError(operation+".publicationSchema", err)
	}
	if !publicationTableExists {
		return nil
	}
	var publicationPrepared, publicationPublished bool
	if err := tx.QueryRow(ctx, `
		SELECT
		  EXISTS (
		    SELECT 1
		    FROM forja.delivery_publications AS dp
		    JOIN forja.attempts AS a
		      ON a.tenant_id=dp.tenant_id AND a.attempt_id=dp.attempt_id
		    WHERE dp.tenant_id=$1 AND dp.repository_id=$2
		      AND a.run_id=$3 AND dp.state='prepared'
		  ),
		  EXISTS (
		    SELECT 1
		    FROM forja.delivery_publications AS dp
		    JOIN forja.attempts AS a
		      ON a.tenant_id=dp.tenant_id AND a.attempt_id=dp.attempt_id
		    WHERE dp.tenant_id=$1 AND dp.repository_id=$2
		      AND a.run_id=$3 AND dp.state='published'
		  )`,
		s.tenantID, s.repositoryID, runID,
	).Scan(&publicationPrepared, &publicationPublished); err != nil {
		return databaseError(operation+".publicationFence", err)
	}
	if publicationPrepared ||
		(publicationPublished && target != runstate.StateCompleted) {
		return fault.New(
			fault.CodeConflict,
			operation,
			"a prepared or published delivery has committed this Run to publication",
		)
	}
	return nil
}

func (s *Store) appendRunEvent(
	ctx context.Context,
	tx pgx.Tx,
	eventType string,
	run contracts.Run,
	metadata runstate.CommandMetadata,
) error {
	payload, err := json.Marshal(run)
	if err != nil {
		return fault.Wrap(
			fault.CodeInternal,
			"postgres.appendRunEvent",
			"encode event payload",
			err,
		)
	}
	return s.appendEvent(
		ctx,
		tx,
		"run",
		run.RunID,
		run.Version,
		eventType,
		run.UpdatedAt,
		payload,
		metadata,
	)
}

func (s *Store) appendEvent(
	ctx context.Context,
	tx pgx.Tx,
	aggregateType string,
	aggregateID string,
	aggregateVersion int,
	eventType string,
	occurredAt time.Time,
	payload []byte,
	metadata runstate.CommandMetadata,
) error {
	// All canonical event/outbox writers and checkpoint rebuilds share this
	// transaction lock. Identity values can be allocated out of commit order;
	// serializing this boundary makes max(outbox_id) a safe committed watermark.
	if _, err := tx.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock($1)",
		advisoryLockKey(outboxWatermarkLock),
	); err != nil {
		return databaseError("postgres.appendEvent.lockWatermark", err)
	}
	eventID, err := newPrefixedID("event")
	if err != nil {
		return fault.Wrap(
			fault.CodeInternal,
			"postgres.appendEvent",
			"generate event ID",
			err,
		)
	}
	tag, err := tx.Exec(ctx, `
		WITH inserted AS (
			INSERT INTO forja.events (
				event_id, tenant_id, repository_id, aggregate_type, aggregate_id,
				aggregate_version, event_type, schema_version, occurred_at,
				actor_type, actor_id, correlation_id, causation_id,
				idempotency_key, payload
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, '1.0', $8,
				$9, $10, $11, $12, $13, $14
			)
			RETURNING event_id, tenant_id, repository_id
		)
		INSERT INTO forja.outbox (event_id, tenant_id, repository_id)
		SELECT event_id, tenant_id, repository_id FROM inserted`,
		eventID,
		s.tenantID,
		s.repositoryID,
		aggregateType,
		aggregateID,
		aggregateVersion,
		eventType,
		occurredAt,
		metadata.ActorType,
		metadata.ActorID,
		metadata.CorrelationID,
		metadata.CausationID,
		metadata.IdempotencyKey,
		payload,
	)
	if err != nil {
		return databaseError("postgres.appendEvent", err)
	}
	if tag.RowsAffected() != 1 {
		return fault.New(
			fault.CodeInternal,
			"postgres.appendEvent",
			"event/outbox insert did not create exactly one message",
		)
	}
	return nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanRun(row rowScanner) (contracts.Run, error) {
	var run contracts.Run
	err := row.Scan(
		&run.RunID,
		&run.Objective,
		&run.State,
		&run.Version,
		&run.CreatedAt,
		&run.UpdatedAt,
	)
	run.SchemaVersion = "1.0"
	run.CreatedAt = run.CreatedAt.UTC()
	run.UpdatedAt = run.UpdatedAt.UTC()
	return run, err
}

func validateObjective(objective string) error {
	length := utf8.RuneCountInString(objective)
	if length < 3 || length > 8000 {
		return fault.New(
			fault.CodeInvalidArgument,
			"postgres.validateObjective",
			"objective length must be between 3 and 8000 characters",
		)
	}
	return nil
}

func hashRequest(parts ...string) []byte {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return digest[:]
}

func hashCommand(metadata runstate.CommandMetadata, parts ...string) []byte {
	causation := ""
	if metadata.CausationID != nil {
		causation = *metadata.CausationID
	}
	parts = append(parts, metadata.ActorType, metadata.ActorID, causation)
	if metadata.AuditToolName != "" {
		parts = append(parts, metadata.AuditToolName)
	}
	return hashRequest(parts...)
}

func postgresTimestamp(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func lockIdempotency(
	ctx context.Context,
	tx pgx.Tx,
	tenantID string,
	scope string,
	key string,
) error {
	// Incremental migrations acquire this relation exclusively before touching
	// aggregate tables. Take the compatible command-side barrier before any
	// command can lock an aggregate, including CreateAttempt's delayed replay
	// lookup after lease validation.
	if _, err := tx.Exec(
		ctx,
		"LOCK TABLE forja.idempotency_keys IN ACCESS SHARE MODE",
	); err != nil {
		return databaseError("postgres.lockIdempotency.lockMigrationBarrier", err)
	}
	_, err := tx.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock($1)",
		advisoryLockKey(tenantID+"\x00"+scope+"\x00"+key),
	)
	if err != nil {
		return databaseError("postgres.lockIdempotency", err)
	}
	return nil
}

func newPrefixedID(prefix string) (string, error) {
	id, err := identity.NewRunID()
	if err != nil {
		return "", err
	}
	return prefix + "_" + strings.TrimPrefix(id.String(), "run_"), nil
}

func advisoryLockKey(value string) int64 {
	digest := sha256.Sum256([]byte(value))
	return int64(binary.BigEndian.Uint64(digest[:8]))
}

func loadRunReplay(
	ctx context.Context,
	tx pgx.Tx,
	tenantID string,
	scope string,
	key string,
	requestHash []byte,
) (contracts.Run, bool, error) {
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
		return contracts.Run{}, false, nil
	}
	if err != nil {
		return contracts.Run{}, false, databaseError("postgres.loadRunReplay", err)
	}
	if !bytes.Equal(storedHash, requestHash) {
		return contracts.Run{}, false, fault.New(
			fault.CodeConflict,
			"postgres.loadRunReplay",
			"idempotency key was already used for a different command",
		)
	}
	run, err := decodeStoredRun(response)
	if err != nil {
		return contracts.Run{}, false, fault.Wrap(
			fault.CodeInternal,
			"postgres.loadRunReplay",
			"decode stored command response",
			err,
		)
	}
	return run, true, nil
}

func decodeStoredRun(data []byte) (contracts.Run, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var run contracts.Run
	if err := decoder.Decode(&run); err != nil {
		return contracts.Run{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON documents")
		}
		return contracts.Run{}, err
	}
	id, err := identity.ParseRunID(run.RunID)
	if err != nil || id.String() != run.RunID {
		return contracts.Run{}, fmt.Errorf("invalid stored run ID")
	}
	if run.SchemaVersion != "1.0" {
		return contracts.Run{}, fmt.Errorf("invalid stored schema version")
	}
	if err := validateObjective(run.Objective); err != nil {
		return contracts.Run{}, err
	}
	if _, err := runstate.ParseState(run.State); err != nil {
		return contracts.Run{}, err
	}
	if run.Version < 1 ||
		!runstate.ValidTimestamp(run.CreatedAt) ||
		!runstate.ValidTimestamp(run.UpdatedAt) ||
		run.UpdatedAt.Before(run.CreatedAt) {
		return contracts.Run{}, fmt.Errorf("invalid stored run version or timestamps")
	}
	return run, nil
}

func saveRunReplay(
	ctx context.Context,
	tx pgx.Tx,
	tenantID string,
	scope string,
	key string,
	requestHash []byte,
	status int,
	run contracts.Run,
) error {
	response, err := json.Marshal(run)
	if err != nil {
		return fault.Wrap(
			fault.CodeInternal,
			"postgres.saveRunReplay",
			"encode command response",
			err,
		)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO forja.idempotency_keys (
			tenant_id, scope, idempotency_key, request_hash,
			response_status, response_body
		) VALUES ($1, $2, $3, $4, $5, $6)`,
		tenantID,
		scope,
		key,
		requestHash,
		status,
		response,
	)
	if err != nil {
		return databaseError("postgres.saveRunReplay", err)
	}
	return nil
}

func databaseError(operation string, err error) error {
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch pgError.Code {
		case "23505", "23503", "40001", "40P01":
			return fault.Wrap(fault.CodeConflict, operation, "database conflict", err)
		case "23514", "22P02":
			return fault.Wrap(fault.CodeInvalidArgument, operation, "database rejected input", err)
		}
	}
	return fault.Wrap(fault.CodeUnavailable, operation, "database operation failed", err)
}

func isPrivilegedResumeTransition(source string, target runstate.State) bool {
	return (source == string(runstate.StateFailedRetryable) && target == runstate.StateQueued) ||
		(source == string(runstate.StateAwaitingDecision) && target == runstate.StateQueued)
}
