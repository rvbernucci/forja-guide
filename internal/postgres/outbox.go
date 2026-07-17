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
)

// ClaimOutbox atomically leases queue rows to one competing dispatcher worker.
func (s *Store) ClaimOutbox(
	ctx context.Context,
	workerID string,
	limit int,
	ttl time.Duration,
) ([]persistence.OutboxMessage, error) {
	if utf8.RuneCountInString(workerID) < 1 ||
		utf8.RuneCountInString(workerID) > 500 ||
		limit < 1 ||
		limit > 1000 {
		return nil, fault.New(
			fault.CodeInvalidArgument,
			"postgres.ClaimOutbox",
			"worker ID of at most 500 characters and a limit between 1 and 1000 are required",
		)
	}
	if ttl < time.Millisecond || ttl > time.Hour {
		return nil, fault.New(
			fault.CodeInvalidArgument,
			"postgres.ClaimOutbox",
			"claim TTL must be between one millisecond and one hour",
		)
	}
	rows, err := s.pool.Query(ctx, `
		WITH candidates AS (
			SELECT outbox_id
			FROM forja.outbox
				WHERE tenant_id=$1 AND repository_id=$2
				  AND available_at <= clock_timestamp()
			  AND (
			      state='pending'
			      OR (state='inflight' AND locked_until <= clock_timestamp())
			  )
			ORDER BY outbox_id
			FOR UPDATE SKIP LOCKED
				LIMIT $3
		), claimed AS (
			UPDATE forja.outbox AS o
			SET state='inflight',
				    locked_by=$4,
				    locked_until=clock_timestamp() + $5::interval,
			    fencing_token=o.fencing_token+1,
			    attempts=o.attempts+1
			FROM candidates AS c
			WHERE o.outbox_id=c.outbox_id
			RETURNING o.outbox_id, o.event_id, o.attempts, o.fencing_token
		)
		SELECT c.outbox_id, c.event_id::text, e.aggregate_type,
		       e.aggregate_id::text, e.aggregate_version, e.event_type,
		       e.payload, c.attempts, c.fencing_token
		FROM claimed AS c
		JOIN forja.events AS e ON e.event_id=c.event_id
		ORDER BY c.outbox_id`,
		s.tenantID,
		s.repositoryID,
		limit,
		workerID,
		intervalString(ttl),
	)
	if err != nil {
		return nil, databaseError("postgres.ClaimOutbox", err)
	}
	defer rows.Close()
	messages := make([]persistence.OutboxMessage, 0, limit)
	for rows.Next() {
		var message persistence.OutboxMessage
		if err := rows.Scan(
			&message.OutboxID,
			&message.EventID,
			&message.AggregateType,
			&message.AggregateID,
			&message.AggregateVersion,
			&message.EventType,
			&message.Payload,
			&message.Attempts,
			&message.FencingToken,
		); err != nil {
			return nil, databaseError("postgres.ClaimOutbox.scan", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError("postgres.ClaimOutbox.rows", err)
	}
	return messages, nil
}

// CompleteOutbox publishes only the exact fenced claim.
func (s *Store) CompleteOutbox(
	ctx context.Context,
	outboxID int64,
	workerID string,
	fencingToken int64,
) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE forja.outbox
		SET state='published', published_at=clock_timestamp(),
		    locked_by=NULL, locked_until=NULL, last_error=NULL
		WHERE tenant_id=$1 AND repository_id=$2 AND outbox_id=$3 AND state='inflight'
		  AND locked_by=$4 AND fencing_token=$5
		  AND locked_until > clock_timestamp()`,
		s.tenantID,
		s.repositoryID,
		outboxID,
		workerID,
		fencingToken,
	)
	if err != nil {
		return databaseError("postgres.CompleteOutbox", err)
	}
	if tag.RowsAffected() != 1 {
		return fault.New(
			fault.CodeConflict,
			"postgres.CompleteOutbox",
			"outbox claim is stale or owned by another worker",
		)
	}
	return nil
}

// FailOutbox reschedules a claim or records a terminal dead letter.
func (s *Store) FailOutbox(
	ctx context.Context,
	outboxID int64,
	workerID string,
	fencingToken int64,
	cause error,
	retryAt time.Time,
	maxAttempts int,
) error {
	if cause == nil || maxAttempts < 1 || retryAt.IsZero() {
		return fault.New(
			fault.CodeInvalidArgument,
			"postgres.FailOutbox",
			"cause, retry time, and positive max attempts are required",
		)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return databaseError("postgres.FailOutbox.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var attempts int
	var eventID string
	var payload []byte
	err = tx.QueryRow(ctx, `
		SELECT o.attempts, o.event_id::text, e.payload
		FROM forja.outbox AS o
		JOIN forja.events AS e ON e.event_id=o.event_id
		WHERE o.tenant_id=$1 AND o.repository_id=$2
		  AND o.outbox_id=$3 AND o.state='inflight'
		  AND o.locked_by=$4 AND o.fencing_token=$5
		  AND o.locked_until > clock_timestamp()
		FOR UPDATE OF o`,
		s.tenantID,
		s.repositoryID,
		outboxID,
		workerID,
		fencingToken,
	).Scan(&attempts, &eventID, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return fault.New(
			fault.CodeConflict,
			"postgres.FailOutbox",
			"outbox claim is stale or owned by another worker",
		)
	}
	if err != nil {
		return databaseError("postgres.FailOutbox.select", err)
	}
	nextState := "pending"
	if attempts >= maxAttempts {
		nextState = "dead"
	}
	if nextState == "dead" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO forja.projection_dead_letters (
				tenant_id, repository_id, projector_name, outbox_id, event_id,
				error_class, error_message, payload
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			s.tenantID,
			s.repositoryID,
			workerID,
			outboxID,
			eventID,
			fmt.Sprintf("%T", cause),
			truncateError(cause),
			payload,
		); err != nil {
			return databaseError("postgres.FailOutbox.deadLetter", err)
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE forja.outbox
		SET state=$1, available_at=$2, locked_by=NULL, locked_until=NULL,
		    last_error=$3
		WHERE tenant_id=$4 AND repository_id=$5 AND outbox_id=$6
		  AND state='inflight' AND locked_by=$7 AND fencing_token=$8
		  AND locked_until > clock_timestamp()`,
		nextState,
		retryAt,
		truncateError(cause),
		s.tenantID,
		s.repositoryID,
		outboxID,
		workerID,
		fencingToken,
	)
	if err != nil {
		return databaseError("postgres.FailOutbox.update", err)
	}
	if tag.RowsAffected() != 1 {
		return fault.New(
			fault.CodeConflict,
			"postgres.FailOutbox",
			"outbox claim expired before the final write",
		)
	}
	if err := tx.Commit(ctx); err != nil {
		return databaseError("postgres.FailOutbox.commit", err)
	}
	return nil
}

func truncateError(err error) string {
	const limit = 4000
	message := err.Error()
	if utf8.RuneCountInString(message) <= limit {
		return message
	}
	return string([]rune(message)[:limit])
}
