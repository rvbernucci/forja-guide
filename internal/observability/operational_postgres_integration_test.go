package observability_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rvbernucci/forja-guide/internal/observability"
	"github.com/rvbernucci/forja-guide/internal/postgres"
)

func TestOperationalSnapshotCompilesAgainstCanonicalSchema(t *testing.T) {
	databaseURL := os.Getenv("FORJA_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("FORJA_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	reader, err := observability.NewPostgresOperationalReader(
		pool,
		postgres.DefaultTenantID,
		postgres.DefaultRepositoryID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.OperationalSnapshot(ctx, observability.DefaultOperationalThresholds()); err != nil {
		t.Fatalf("collect canonical operational snapshot: %v", err)
	}
}

func TestOperationalSnapshotRequiresFreshLeaseAndCountsActualProjectionRows(t *testing.T) {
	databaseURL := os.Getenv("FORJA_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("FORJA_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const (
		repositoryA = "10000000-0000-4000-8000-000000000006"
		repositoryB = "10000000-0000-4000-8000-000000000007"
	)
	for _, repository := range []struct {
		id   string
		name string
	}{{repositoryA, "observability/repository-a"}, {repositoryB, "observability/repository-b"}} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO forja.repositories (
				repository_id, tenant_id, canonical_name
			) VALUES ($1, $2, $3)`, repository.id, postgres.DefaultTenantID, repository.name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.leases (
			tenant_id, repository_id, resource_type, resource_id, owner_id,
			fencing_token, acquired_at, expires_at, updated_at
		) VALUES
			($1, $2, 'scheduler', 'released', 'worker-a', 1,
			 clock_timestamp() - interval '2 minutes',
			 clock_timestamp() - interval '1 minute',
			 clock_timestamp() - interval '1 minute'),
			($1, $2, 'scheduler', 'naturally-expired', 'worker-b', 1,
			 clock_timestamp() - interval '3 minutes',
			 clock_timestamp() - interval '1 minute',
			 clock_timestamp() - interval '3 minutes'),
			($1, $2, 'file', 'retired-set-member', 'worker-c', 1,
			 clock_timestamp() - interval '3 minutes',
			 clock_timestamp() - interval '1 minute',
			 clock_timestamp() - interval '3 minutes')`, postgres.DefaultTenantID, repositoryA); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.lease_sets (
			tenant_id, repository_id, lease_set_id, owner_id, member_digest,
			state, acquired_at, expires_at, updated_at, authorized_ttl_us
		) VALUES (
			$1, $2, 'retired-observability-set', 'worker-c', decode(repeat('00', 32), 'hex'),
			'released', clock_timestamp() - interval '3 minutes',
			clock_timestamp() - interval '1 minute', clock_timestamp(), 60000000
		)`, postgres.DefaultTenantID, repositoryA); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.lease_set_members (
			tenant_id, repository_id, lease_set_id, resource_type,
			resource_id, fencing_token
		) VALUES ($1, $2, 'retired-observability-set', 'file', 'retired-set-member', 1)`,
		postgres.DefaultTenantID, repositoryA,
	); err != nil {
		t.Fatal(err)
	}
	const liveRunID = "run_10000000-0000-4000-8000-000000000008"
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.runs (
			run_id, tenant_id, repository_id, objective, state, version,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, 'Exercise a live long-running worker', 'running', 2,
			clock_timestamp() - interval '1 hour',
			clock_timestamp() - interval '1 hour'
		)`, liveRunID, postgres.DefaultTenantID, repositoryA); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.leases (
			tenant_id, repository_id, resource_type, resource_id, owner_id,
			fencing_token, acquired_at, expires_at, updated_at
		) VALUES (
			$1, $2, 'scheduler', 'live-long-run', 'worker-live', 1,
			clock_timestamp() - interval '1 hour',
			clock_timestamp() + interval '1 minute', clock_timestamp()
		)`, postgres.DefaultTenantID, repositoryA); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.attempts (
			attempt_id, tenant_id, run_id, ordinal, status,
			lease_resource_type, lease_resource_id, worker_id, fencing_token,
			started_at, version, created_at, updated_at
		) VALUES (
			'attempt_observability_live', $1, $2, 1, 'running',
			'scheduler', 'live-long-run', 'worker-live', 1,
			clock_timestamp() - interval '1 hour', 2,
			clock_timestamp() - interval '1 hour',
			clock_timestamp() - interval '1 hour'
		)`, postgres.DefaultTenantID, liveRunID); err != nil {
		t.Fatal(err)
	}
	const staleRunID = "run_10000000-0000-4000-8000-000000000009"
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.runs (
			run_id, tenant_id, repository_id, objective, state, version,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, 'Detect an abandoned long-TTL worker', 'running', 2,
			clock_timestamp() - interval '1 hour',
			clock_timestamp() - interval '1 hour'
		)`, staleRunID, postgres.DefaultTenantID, repositoryA); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.leases (
			tenant_id, repository_id, resource_type, resource_id, owner_id,
			fencing_token, acquired_at, expires_at, updated_at
		) VALUES (
			$1, $2, 'scheduler', 'stale-long-run', 'worker-stale', 1,
			clock_timestamp() - interval '1 hour',
			clock_timestamp() + interval '23 hours',
			clock_timestamp() - interval '1 hour'
		)`, postgres.DefaultTenantID, repositoryA); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.attempts (
			attempt_id, tenant_id, run_id, ordinal, status,
			lease_resource_type, lease_resource_id, worker_id, fencing_token,
			started_at, version, created_at, updated_at
		) VALUES (
			'attempt_observability_stale', $1, $2, 1, 'running',
			'scheduler', 'stale-long-run', 'worker-stale', 1,
			clock_timestamp() - interval '1 hour', 2,
			clock_timestamp() - interval '1 hour',
			clock_timestamp() - interval '1 hour'
		)`, postgres.DefaultTenantID, staleRunID); err != nil {
		t.Fatal(err)
	}

	insertEventAndOutbox := func(repositoryID, eventID, aggregateID string) int64 {
		t.Helper()
		if _, err := tx.Exec(ctx, `
			INSERT INTO forja.events (
				event_id, tenant_id, repository_id, aggregate_type, aggregate_id,
				aggregate_version, event_type, schema_version, occurred_at,
				actor_type, actor_id, correlation_id, idempotency_key, payload
			) VALUES (
				$1, $2, $3, 'run', $4, 1, 'run.tested', '1.0',
				clock_timestamp(), 'system', 'observability-test',
				'observability-test', $5, '{}'::jsonb
			)`, eventID, postgres.DefaultTenantID, repositoryID, aggregateID, "idempotency-"+eventID); err != nil {
			t.Fatal(err)
		}
		var outboxID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO forja.outbox (tenant_id, repository_id, event_id)
			VALUES ($1, $2, $3)
			RETURNING outbox_id`, postgres.DefaultTenantID, repositoryID, eventID).Scan(&outboxID); err != nil {
			t.Fatal(err)
		}
		return outboxID
	}
	checkpoint := insertEventAndOutbox(repositoryA, "event_observability_a1", "aggregate-a1")
	insertEventAndOutbox(repositoryB, "event_observability_b1", "aggregate-b1")
	insertEventAndOutbox(repositoryA, "event_observability_a2", "aggregate-a2")
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.projection_checkpoints (
			tenant_id, repository_id, projector_name, last_outbox_id
		) VALUES ($1, $2, 'observability-test', $3)`,
		postgres.DefaultTenantID, repositoryA, checkpoint,
	); err != nil {
		t.Fatal(err)
	}

	reader, err := observability.NewPostgresOperationalReader(
		tx, postgres.DefaultTenantID, repositoryA,
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reader.OperationalSnapshot(ctx, observability.DefaultOperationalThresholds())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ExpiredLeases != 1 {
		t.Fatalf("expired leases = %d, want only the naturally expired lease", snapshot.ExpiredLeases)
	}
	if snapshot.ProjectionLag != 1 {
		t.Fatalf("projection lag = %d, want one unprojected authority row", snapshot.ProjectionLag)
	}
	if snapshot.StuckRuns != 1 {
		t.Fatalf(
			"stuck runs = %d, want fresh work excluded and stale long-TTL work counted",
			snapshot.StuckRuns,
		)
	}
}
