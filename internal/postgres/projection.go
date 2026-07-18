package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

type replayedRun struct {
	eventID string
	run     contracts.Run
}

// RebuildRunProjection validates event continuity and semantics, then
// reconstructs the read model entirely from immutable run events.
func (s *Store) RebuildRunProjection(ctx context.Context, projectorName string) error {
	if projectorName == "" {
		return fault.New(
			fault.CodeInvalidArgument,
			"postgres.RebuildRunProjection",
			"projector name is required",
		)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.Serializable,
	})
	if err != nil {
		return databaseError("postgres.RebuildRunProjection.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Event/outbox writers acquire the same lock before allocating an outbox
	// identity. Once this lock is held, every lower visible ID is committed and
	// no higher ID can be allocated until the checkpoint commits.
	if _, err := tx.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock($1)",
		advisoryLockKey(outboxWatermarkLock),
	); err != nil {
		return databaseError("postgres.RebuildRunProjection.lockWatermark", err)
	}
	if _, err := tx.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock($1)",
		advisoryLockKey(
			s.tenantID+"\x00"+s.repositoryID+"\x00projector\x00"+projectorName,
		),
	); err != nil {
		return databaseError("postgres.RebuildRunProjection.lockProjector", err)
	}

	latest, err := replayRunEvents(ctx, tx, s.tenantID, s.repositoryID)
	if err != nil {
		return err
	}
	if err := reconcileCanonicalRuns(
		ctx,
		tx,
		s.tenantID,
		s.repositoryID,
		latest,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM forja.run_projections
		WHERE tenant_id=$1 AND repository_id=$2 AND projector_name=$3`,
		s.tenantID,
		s.repositoryID,
		projectorName,
	); err != nil {
		return databaseError("postgres.RebuildRunProjection.clear", err)
	}
	for runID, projection := range latest {
		run := projection.run
		if _, err := tx.Exec(ctx, `
			INSERT INTO forja.run_projections (
				tenant_id, repository_id, projector_name, run_id, objective, state,
				aggregate_version, source_event_id, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			s.tenantID,
			s.repositoryID,
			projectorName,
			runID,
			run.Objective,
			run.State,
			run.Version,
			projection.eventID,
			run.UpdatedAt,
		); err != nil {
			return databaseError("postgres.RebuildRunProjection.insert", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.projection_checkpoints (
			tenant_id, repository_id, projector_name, last_outbox_id, updated_at
		)
		SELECT $1, $2, $3, COALESCE(max(outbox_id), 0), clock_timestamp()
		FROM forja.outbox
		WHERE tenant_id=$1 AND repository_id=$2
		ON CONFLICT (tenant_id, repository_id, projector_name) DO UPDATE
		SET last_outbox_id=EXCLUDED.last_outbox_id,
		    updated_at=EXCLUDED.updated_at`,
		s.tenantID,
		s.repositoryID,
		projectorName,
	); err != nil {
		return databaseError("postgres.RebuildRunProjection.checkpoint", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return databaseError("postgres.RebuildRunProjection.commit", err)
	}
	return nil
}

func reconcileCanonicalRuns(
	ctx context.Context,
	tx pgx.Tx,
	tenantID string,
	repositoryID string,
	replayed map[string]replayedRun,
) error {
	rows, err := tx.Query(ctx, `
		SELECT run_id, objective, state, version, created_at, updated_at
		FROM forja.runs
		WHERE tenant_id=$1 AND repository_id=$2
		ORDER BY run_id`,
		tenantID,
		repositoryID,
	)
	if err != nil {
		return databaseError("postgres.reconcileCanonicalRuns.query", err)
	}
	defer rows.Close()
	seen := make(map[string]struct{}, len(replayed))
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return databaseError("postgres.reconcileCanonicalRuns.scan", err)
		}
		candidate, ok := replayed[run.RunID]
		if !ok || candidate.run != run {
			return corruptRunStream(
				run.RunID,
				"canonical aggregate differs from immutable event replay",
			)
		}
		seen[run.RunID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return databaseError("postgres.reconcileCanonicalRuns.rows", err)
	}
	if len(seen) != len(replayed) {
		return corruptRunStream(
			"*",
			"immutable event replay contains a run missing from canonical state",
		)
	}
	return nil
}

func replayRunEvents(
	ctx context.Context,
	tx pgx.Tx,
	tenantID string,
	repositoryID string,
) (map[string]replayedRun, error) {
	rows, err := tx.Query(ctx, `
		SELECT aggregate_id, aggregate_version, event_id, event_type,
		       occurred_at, payload
		FROM forja.events
		WHERE tenant_id=$1 AND repository_id=$2 AND aggregate_type='run'
		ORDER BY aggregate_id, aggregate_version`,
		tenantID,
		repositoryID,
	)
	if err != nil {
		return nil, databaseError("postgres.replayRunEvents.query", err)
	}
	defer rows.Close()
	latest := make(map[string]replayedRun)
	for rows.Next() {
		var aggregateID, eventID, eventType string
		var aggregateVersion int
		var occurredAt time.Time
		var payload []byte
		if err := rows.Scan(
			&aggregateID,
			&aggregateVersion,
			&eventID,
			&eventType,
			&occurredAt,
			&payload,
		); err != nil {
			return nil, databaseError("postgres.replayRunEvents.scan", err)
		}
		run, err := decodeStoredRun(payload)
		if err != nil {
			return nil, corruptRunStream(
				aggregateID,
				fmt.Sprintf("event %s has invalid payload: %v", eventID, err),
			)
		}
		previous, exists := latest[aggregateID]
		if err := validateReplayedRunEvent(
			aggregateID,
			aggregateVersion,
			eventID,
			eventType,
			occurredAt.UTC(),
			run,
			previous.run,
			exists,
		); err != nil {
			return nil, err
		}
		latest[aggregateID] = replayedRun{eventID: eventID, run: run}
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError("postgres.replayRunEvents.rows", err)
	}
	return latest, nil
}

func validateReplayedRunEvent(
	aggregateID string,
	aggregateVersion int,
	eventID string,
	eventType string,
	occurredAt time.Time,
	run contracts.Run,
	previous contracts.Run,
	hasPrevious bool,
) error {
	if run.RunID != aggregateID || run.Version != aggregateVersion {
		return corruptRunStream(
			aggregateID,
			fmt.Sprintf("event %s payload identity/version disagrees with its envelope", eventID),
		)
	}
	if !run.UpdatedAt.Equal(occurredAt) {
		return corruptRunStream(
			aggregateID,
			fmt.Sprintf("event %s occurrence time disagrees with payload", eventID),
		)
	}
	if !hasPrevious {
		if aggregateVersion != 1 ||
			eventType != "run.created" ||
			run.State != string(runstate.StateDraft) ||
			!run.CreatedAt.Equal(run.UpdatedAt) {
			return corruptRunStream(aggregateID, "first event is not a valid run creation")
		}
		return nil
	}
	if aggregateVersion != previous.Version+1 {
		return corruptRunStream(
			aggregateID,
			fmt.Sprintf(
				"event stream is not contiguous: got version %d after %d",
				aggregateVersion,
				previous.Version,
			),
		)
	}
	if eventType != "run.transitioned" ||
		run.Objective != previous.Objective ||
		!run.CreatedAt.Equal(previous.CreatedAt) ||
		run.UpdatedAt.Before(previous.UpdatedAt) {
		return corruptRunStream(
			aggregateID,
			fmt.Sprintf("event %s changes immutable fields or time ordering", eventID),
		)
	}
	from, err := runstate.ParseState(previous.State)
	if err != nil {
		return corruptRunStream(aggregateID, "previous event has an invalid state")
	}
	to, err := runstate.ParseState(run.State)
	if err != nil || !canReplayRunTransition(from, to) {
		return corruptRunStream(
			aggregateID,
			fmt.Sprintf("event %s contains an illegal state transition", eventID),
		)
	}
	return nil
}

func canReplayRunTransition(from runstate.State, to runstate.State) bool {
	// Sprint 04 resumed blocked work directly into running. New commands must use
	// a fresh queued scheduling cycle, but immutable pre-upgrade events remain
	// valid replay input.
	if from == runstate.StateAwaitingDecision && to == runstate.StateRunning {
		return true
	}
	return runstate.CanTransition(from, to)
}

func corruptRunStream(runID string, detail string) error {
	return fault.New(
		fault.CodeConflict,
		"postgres.RebuildRunProjection",
		fmt.Sprintf("event stream for run %s is corrupt: %s", runID, detail),
	)
}
