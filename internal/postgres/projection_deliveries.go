package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/retrieval"
)

// EnsureProjectionConsumer atomically creates one independent consumer and
// seeds it from every committed event already visible in this repository.
func (s *Store) EnsureProjectionConsumer(ctx context.Context, projectorName string, configurationHash [32]byte) error {
	if !validProjectorName(projectorName) {
		return fault.New(fault.CodeInvalidArgument, "postgres.EnsureProjectionConsumer", "projector name is invalid")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return databaseError("postgres.EnsureProjectionConsumer.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", advisoryLockKey(outboxWatermarkLock)); err != nil {
		return databaseError("postgres.EnsureProjectionConsumer.lockWatermark", err)
	}
	var existing []byte
	err = tx.QueryRow(ctx, `
		SELECT configuration_sha256
		FROM forja.projection_consumers
		WHERE tenant_id=$1 AND repository_id=$2 AND projector_name=$3
		FOR UPDATE`, s.tenantID, s.repositoryID, projectorName).Scan(&existing)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO forja.projection_consumers (
				tenant_id, repository_id, projector_name, status, configuration_sha256
			) VALUES ($1, $2, $3, 'active', $4)`,
			s.tenantID, s.repositoryID, projectorName, configurationHash[:]); err != nil {
			return databaseError("postgres.EnsureProjectionConsumer.insert", err)
		}
	} else if err != nil {
		return databaseError("postgres.EnsureProjectionConsumer.load", err)
	} else if string(existing) != string(configurationHash[:]) {
		return fault.New(fault.CodeConflict, "postgres.EnsureProjectionConsumer", "projector configuration hash differs from registered consumer")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.projection_deliveries (
			tenant_id, repository_id, projector_name, outbox_id, state, available_at
		)
		SELECT o.tenant_id, o.repository_id, $3, o.outbox_id, 'pending', o.available_at
		FROM forja.outbox AS o
		WHERE o.tenant_id=$1 AND o.repository_id=$2
		ON CONFLICT (tenant_id, repository_id, projector_name, outbox_id) DO NOTHING`,
		s.tenantID, s.repositoryID, projectorName); err != nil {
		return databaseError("postgres.EnsureProjectionConsumer.backfill", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.projection_checkpoints (
			tenant_id, repository_id, projector_name, last_outbox_id, updated_at
		) VALUES ($1, $2, $3, 0, clock_timestamp())
		ON CONFLICT (tenant_id, repository_id, projector_name) DO NOTHING`,
		s.tenantID, s.repositoryID, projectorName); err != nil {
		return databaseError("postgres.EnsureProjectionConsumer.checkpoint", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return databaseError("postgres.EnsureProjectionConsumer.commit", err)
	}
	return nil
}

// ClaimProjectionDeliveries leases only this projector's pending work.
func (s *Store) ClaimProjectionDeliveries(ctx context.Context, projectorName, workerID string, limit int, ttl time.Duration) ([]persistence.ProjectionDelivery, error) {
	if !validProjectorName(projectorName) || utf8.RuneCountInString(workerID) < 1 || utf8.RuneCountInString(workerID) > 500 || limit < 1 || limit > 1000 {
		return nil, fault.New(fault.CodeInvalidArgument, "postgres.ClaimProjectionDeliveries", "projector, worker ID, and limit are invalid")
	}
	if ttl < time.Millisecond || ttl > time.Hour {
		return nil, fault.New(fault.CodeInvalidArgument, "postgres.ClaimProjectionDeliveries", "claim TTL must be between one millisecond and one hour")
	}
	rows, err := s.pool.Query(ctx, `
		WITH candidates AS (
			SELECT delivery.outbox_id
			FROM forja.projection_deliveries AS delivery
			WHERE delivery.tenant_id=$1 AND delivery.repository_id=$2
			  AND delivery.projector_name=$3 AND delivery.available_at <= clock_timestamp()
			  AND (delivery.state='pending' OR (delivery.state='inflight' AND delivery.locked_until <= clock_timestamp()))
			ORDER BY delivery.outbox_id
			FOR UPDATE SKIP LOCKED
			LIMIT $4
		), claimed AS (
			UPDATE forja.projection_deliveries AS delivery
			SET state='inflight', locked_by=$5,
				locked_until=clock_timestamp() + $6::interval,
				fencing_token=delivery.fencing_token+1, attempts=delivery.attempts+1
			FROM candidates
			WHERE delivery.tenant_id=$1 AND delivery.repository_id=$2 AND delivery.projector_name=$3
			  AND delivery.outbox_id=candidates.outbox_id
			RETURNING delivery.outbox_id, delivery.attempts, delivery.fencing_token
		)
		SELECT claimed.outbox_id, outbox.event_id::text, event.aggregate_type,
		       event.aggregate_id::text, event.aggregate_version, event.event_type,
		       event.payload, claimed.attempts, claimed.fencing_token
		FROM claimed
		JOIN forja.outbox AS outbox
		  ON outbox.tenant_id=$1 AND outbox.repository_id=$2 AND outbox.outbox_id=claimed.outbox_id
		JOIN forja.events AS event ON event.event_id=outbox.event_id
		ORDER BY claimed.outbox_id`,
		s.tenantID, s.repositoryID, projectorName, limit, workerID, intervalString(ttl))
	if err != nil {
		return nil, databaseError("postgres.ClaimProjectionDeliveries", err)
	}
	defer rows.Close()
	deliveries := make([]persistence.ProjectionDelivery, 0, limit)
	for rows.Next() {
		var delivery persistence.ProjectionDelivery
		delivery.ProjectorName = projectorName
		if err := rows.Scan(
			&delivery.OutboxID, &delivery.EventID, &delivery.AggregateType, &delivery.AggregateID,
			&delivery.AggregateVersion, &delivery.EventType, &delivery.Payload,
			&delivery.Attempts, &delivery.FencingToken,
		); err != nil {
			return nil, databaseError("postgres.ClaimProjectionDeliveries.scan", err)
		}
		deliveries = append(deliveries, delivery)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError("postgres.ClaimProjectionDeliveries.rows", err)
	}
	return deliveries, nil
}

// CompleteProjectionDelivery publishes only the exact fenced lease, then
// advances its checkpoint through the contiguous published prefix only.
func (s *Store) CompleteProjectionDelivery(ctx context.Context, projectorName string, outboxID int64, workerID string, fencingToken int64) error {
	if !validProjectorName(projectorName) || outboxID < 1 || workerID == "" || fencingToken < 1 {
		return fault.New(fault.CodeInvalidArgument, "postgres.CompleteProjectionDelivery", "projection completion arguments are invalid")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return databaseError("postgres.CompleteProjectionDelivery.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE forja.projection_deliveries
		SET state='published', published_at=clock_timestamp(), locked_by=NULL,
			locked_until=NULL, last_error=NULL
		WHERE tenant_id=$1 AND repository_id=$2 AND projector_name=$3 AND outbox_id=$4
		  AND state='inflight' AND locked_by=$5 AND fencing_token=$6
		  AND locked_until > clock_timestamp()`,
		s.tenantID, s.repositoryID, projectorName, outboxID, workerID, fencingToken)
	if err != nil {
		return databaseError("postgres.CompleteProjectionDelivery.update", err)
	}
	if tag.RowsAffected() != 1 {
		return fault.New(fault.CodeConflict, "postgres.CompleteProjectionDelivery", "projection delivery claim is stale or owned by another worker")
	}
	if err := advanceProjectionCheckpoint(ctx, tx, s.tenantID, s.repositoryID, projectorName); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return databaseError("postgres.CompleteProjectionDelivery.commit", err)
	}
	return nil
}

// FailProjectionDelivery reschedules an independent projector lease or records
// its terminal error without changing other consumers' delivery state.
func (s *Store) FailProjectionDelivery(ctx context.Context, projectorName string, outboxID int64, workerID string, fencingToken int64, cause error, retryAt time.Time, maxAttempts int) error {
	if !validProjectorName(projectorName) || outboxID < 1 || workerID == "" || fencingToken < 1 || cause == nil || retryAt.IsZero() || maxAttempts < 1 {
		return fault.New(fault.CodeInvalidArgument, "postgres.FailProjectionDelivery", "projection failure arguments are invalid")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return databaseError("postgres.FailProjectionDelivery.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var attempts int
	var eventID string
	var payload []byte
	err = tx.QueryRow(ctx, `
		SELECT delivery.attempts, outbox.event_id::text, event.payload
		FROM forja.projection_deliveries AS delivery
		JOIN forja.outbox AS outbox
		  ON outbox.tenant_id=delivery.tenant_id AND outbox.repository_id=delivery.repository_id
		 AND outbox.outbox_id=delivery.outbox_id
		JOIN forja.events AS event ON event.event_id=outbox.event_id
		WHERE delivery.tenant_id=$1 AND delivery.repository_id=$2 AND delivery.projector_name=$3
		  AND delivery.outbox_id=$4 AND delivery.state='inflight' AND delivery.locked_by=$5
		  AND delivery.fencing_token=$6 AND delivery.locked_until > clock_timestamp()
		FOR UPDATE OF delivery`,
		s.tenantID, s.repositoryID, projectorName, outboxID, workerID, fencingToken).Scan(&attempts, &eventID, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return fault.New(fault.CodeConflict, "postgres.FailProjectionDelivery", "projection delivery claim is stale or owned by another worker")
	}
	if err != nil {
		return databaseError("postgres.FailProjectionDelivery.select", err)
	}
	nextState := "pending"
	if attempts >= maxAttempts {
		nextState = "dead"
		if _, err := tx.Exec(ctx, `
			INSERT INTO forja.projection_dead_letters (
				tenant_id, repository_id, projector_name, outbox_id, event_id,
				error_class, error_message, payload
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			s.tenantID, s.repositoryID, projectorName, outboxID, eventID,
			fmt.Sprintf("%T", cause), truncateError(cause), payload); err != nil {
			return databaseError("postgres.FailProjectionDelivery.deadLetter", err)
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE forja.projection_deliveries
		SET state=$1, available_at=$2, locked_by=NULL, locked_until=NULL, last_error=$3
		WHERE tenant_id=$4 AND repository_id=$5 AND projector_name=$6 AND outbox_id=$7
		  AND state='inflight' AND locked_by=$8 AND fencing_token=$9
		  AND locked_until > clock_timestamp()`,
		nextState, retryAt, truncateError(cause), s.tenantID, s.repositoryID,
		projectorName, outboxID, workerID, fencingToken)
	if err != nil {
		return databaseError("postgres.FailProjectionDelivery.update", err)
	}
	if tag.RowsAffected() != 1 {
		return fault.New(fault.CodeConflict, "postgres.FailProjectionDelivery", "projection delivery claim expired before final write")
	}
	if err := tx.Commit(ctx); err != nil {
		return databaseError("postgres.FailProjectionDelivery.commit", err)
	}
	return nil
}

// RequeueProjectionDelivery is the explicit operator repair path for a dead
// derived-store delivery. It preserves the immutable dead-letter record and
// checkpoint barrier while giving a repaired dependency a new fenced attempt.
func (s *Store) RequeueProjectionDelivery(ctx context.Context, projectorName string, outboxID int64) error {
	if !validProjectorName(projectorName) || outboxID < 1 {
		return fault.New(fault.CodeInvalidArgument, "postgres.RequeueProjectionDelivery", "projector name or outbox ID is invalid")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE forja.projection_deliveries
		SET state='pending', available_at=clock_timestamp(), locked_by=NULL,
			locked_until=NULL, attempts=0, last_error=NULL
		WHERE tenant_id=$1 AND repository_id=$2 AND projector_name=$3
		  AND outbox_id=$4 AND state='dead'`,
		s.tenantID, s.repositoryID, projectorName, outboxID,
	)
	if err != nil {
		return databaseError("postgres.RequeueProjectionDelivery", err)
	}
	if tag.RowsAffected() != 1 {
		return fault.New(fault.CodeConflict, "postgres.RequeueProjectionDelivery", "projection delivery is not dead")
	}
	return nil
}

// RetrievalProjectionLag returns the number of Qdrant retrieval deliveries
// that have not reached a durable published state. It is aggregate-only and
// intentionally fails when the retrieval consumer is not active, preventing a
// query from treating an unregistered derived plane as fresh.
func (s *Store) RetrievalProjectionLag(ctx context.Context) (int64, error) {
	var active bool
	var lag int64
	err := s.pool.QueryRow(ctx, `
		SELECT
			EXISTS(
				SELECT 1 FROM forja.projection_consumers
				WHERE tenant_id=$1 AND repository_id=$2 AND projector_name=$3 AND status='active'
			),
			count(*) FILTER (WHERE state <> 'published')
		FROM forja.projection_deliveries
		WHERE tenant_id=$1 AND repository_id=$2 AND projector_name=$3`,
		s.tenantID, s.repositoryID, retrieval.DefaultQdrantProjectorName,
	).Scan(&active, &lag)
	if err != nil {
		return 0, databaseError("postgres.RetrievalProjectionLag", err)
	}
	if !active {
		return 0, fault.New(fault.CodeUnavailable, "postgres.RetrievalProjectionLag", "retrieval projection consumer is not active")
	}
	return lag, nil
}

func advanceProjectionCheckpoint(ctx context.Context, tx pgx.Tx, tenantID, repositoryID, projectorName string) error {
	tag, err := tx.Exec(ctx, `
		WITH checkpoint AS (
			SELECT last_outbox_id
			FROM forja.projection_checkpoints
			WHERE tenant_id=$1 AND repository_id=$2 AND projector_name=$3
			FOR UPDATE
		), next_pending AS (
			SELECT min(delivery.outbox_id) AS outbox_id
			FROM forja.projection_deliveries AS delivery, checkpoint
			WHERE delivery.tenant_id=$1 AND delivery.repository_id=$2 AND delivery.projector_name=$3
			  AND delivery.outbox_id > checkpoint.last_outbox_id AND delivery.state <> 'published'
		), latest_delivery AS (
			SELECT max(outbox_id) AS outbox_id
			FROM forja.projection_deliveries
			WHERE tenant_id=$1 AND repository_id=$2 AND projector_name=$3
		)
		UPDATE forja.projection_checkpoints AS checkpoint
		SET last_outbox_id=GREATEST(
			checkpoint.last_outbox_id,
			COALESCE((SELECT outbox_id-1 FROM next_pending), (SELECT outbox_id FROM latest_delivery), checkpoint.last_outbox_id)
		), updated_at=clock_timestamp()
		WHERE checkpoint.tenant_id=$1 AND checkpoint.repository_id=$2 AND checkpoint.projector_name=$3`,
		tenantID, repositoryID, projectorName)
	if err != nil {
		return databaseError("postgres.advanceProjectionCheckpoint", err)
	}
	if tag.RowsAffected() != 1 {
		return fault.New(fault.CodeConflict, "postgres.advanceProjectionCheckpoint", "projection checkpoint is missing")
	}
	return nil
}

func validProjectorName(value string) bool {
	if utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 120 {
		return false
	}
	for index, runeValue := range value {
		if (runeValue >= 'a' && runeValue <= 'z') || (index > 0 && ((runeValue >= '0' && runeValue <= '9') || runeValue == '_' || runeValue == '.' || runeValue == '-')) {
			continue
		}
		return false
	}
	return true
}
