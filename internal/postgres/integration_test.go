package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rvbernucci/forja-guide/internal/clock"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

func TestMigrationsCleanRollbackAndUpgrade(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)

	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatalf("migrate clean database: %v", err)
	}
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatalf("migrate database idempotently: %v", err)
	}
	var count int
	if err := pool.QueryRow(
		t.Context(),
		"SELECT count(*) FROM forja.schema_migrations",
	).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != 1 {
		t.Fatalf("migration count = %d, want 1", count)
	}
	if _, err := pool.Exec(
		t.Context(),
		"UPDATE forja.schema_migrations SET checksum='tampered'",
	); err != nil {
		t.Fatalf("tamper migration checksum: %v", err)
	}
	if err := Migrate(t.Context(), pool); err == nil {
		t.Fatal("modified applied migration was not rejected")
	}
	if err := RollbackLast(t.Context(), pool); err == nil {
		t.Fatal("rollback accepted modified migration history")
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if _, err := pool.Exec(
		t.Context(),
		"UPDATE forja.schema_migrations SET checksum=$1",
		migrations[0].checksum,
	); err != nil {
		t.Fatalf("restore migration checksum: %v", err)
	}
	if err := RollbackLast(t.Context(), pool); err != nil {
		t.Fatalf("rollback latest migration: %v", err)
	}
	var runsTableExists bool
	if err := pool.QueryRow(t.Context(), `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema='forja' AND table_name='runs'
		)`).Scan(&runsTableExists); err != nil {
		t.Fatalf("inspect tables after rollback: %v", err)
	}
	if runsTableExists {
		t.Fatal("run table survived rollback")
	}
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatalf("upgrade after rollback: %v", err)
	}
}

func TestMigrationsRejectUnknownAppliedVersion(t *testing.T) {
	pool := migratedPool(t)
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.schema_migrations (version, name, checksum)
		VALUES (999999, 'future_unknown', 'not-known-to-this-binary')`); err != nil {
		t.Fatalf("insert unknown migration: %v", err)
	}
	if err := Migrate(t.Context(), pool); err == nil {
		t.Fatal("older binary accepted an unknown applied migration")
	}
	if err := RollbackLast(t.Context(), pool); err == nil {
		t.Fatal("rollback accepted an unknown migration history")
	}
}

func TestReadinessRequiresExactCanonicalSchema(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	store := newIntegrationStore(t, pool)
	if err := store.Ready(t.Context()); err == nil {
		t.Fatal("empty database reported ready")
	}
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	if err := store.Ready(t.Context()); err != nil {
		t.Fatalf("migrated database not ready: %v", err)
	}
	if _, err := pool.Exec(
		t.Context(),
		"UPDATE forja.schema_migrations SET checksum='drifted'",
	); err != nil {
		t.Fatalf("tamper migration ledger: %v", err)
	}
	if err := store.Ready(t.Context()); err == nil {
		t.Fatal("drifted migration ledger reported ready")
	}
}

func TestReadinessRejectsColumnAndTriggerDrift(t *testing.T) {
	t.Run("column signature", func(t *testing.T) {
		pool := migratedPool(t)
		store := newIntegrationStore(t, pool)
		if _, err := pool.Exec(
			t.Context(),
			"ALTER TABLE forja.runs DROP COLUMN objective",
		); err != nil {
			t.Fatalf("drop required column: %v", err)
		}
		if err := store.Ready(t.Context()); err == nil {
			t.Fatal("schema with a missing run objective reported ready")
		}
	})
	t.Run("append-only trigger state", func(t *testing.T) {
		pool := migratedPool(t)
		store := newIntegrationStore(t, pool)
		if _, err := pool.Exec(
			t.Context(),
			"ALTER TABLE forja.events DISABLE TRIGGER events_are_append_only",
		); err != nil {
			t.Fatalf("disable append-only trigger: %v", err)
		}
		if err := store.Ready(t.Context()); err == nil {
			t.Fatal("schema with a disabled append-only trigger reported ready")
		}
	})
	t.Run("same-name constraint behavior", func(t *testing.T) {
		pool := migratedPool(t)
		store := newIntegrationStore(t, pool)
		if _, err := pool.Exec(t.Context(), `
			ALTER TABLE forja.runs DROP CONSTRAINT runs_objective_check;
			ALTER TABLE forja.runs ADD CONSTRAINT runs_objective_check
			  CHECK (char_length(objective) >= 1)`); err != nil {
			t.Fatalf("replace objective constraint: %v", err)
		}
		if err := store.Ready(t.Context()); err == nil {
			t.Fatal("schema with a weakened same-name constraint reported ready")
		}
	})
	t.Run("same-name trigger function behavior", func(t *testing.T) {
		pool := migratedPool(t)
		store := newIntegrationStore(t, pool)
		if _, err := pool.Exec(t.Context(), `
			CREATE OR REPLACE FUNCTION forja.reject_event_mutation()
			RETURNS trigger
			LANGUAGE plpgsql
			AS $$
			BEGIN
			    RETURN OLD;
			END
			$$`); err != nil {
			t.Fatalf("replace append-only function: %v", err)
		}
		if err := store.Ready(t.Context()); err == nil {
			t.Fatal("schema with a no-op append-only function reported ready")
		}
	})
	t.Run("commit-fence trigger function behavior", func(t *testing.T) {
		pool := migratedPool(t)
		store := newIntegrationStore(t, pool)
		if _, err := pool.Exec(t.Context(), `
			CREATE OR REPLACE FUNCTION forja.enforce_attempt_commit_fence()
			RETURNS trigger
			LANGUAGE plpgsql
			AS $$
			BEGIN
			    RETURN NEW;
			END
			$$`); err != nil {
			t.Fatalf("replace commit-fence function: %v", err)
		}
		if err := store.Ready(t.Context()); err == nil {
			t.Fatal("schema with a no-op commit-fence function reported ready")
		}
	})
	t.Run("conditional append-only trigger", func(t *testing.T) {
		pool := migratedPool(t)
		store := newIntegrationStore(t, pool)
		if _, err := pool.Exec(t.Context(), `
			DROP TRIGGER events_are_append_only ON forja.events;
			CREATE TRIGGER events_are_append_only
			BEFORE UPDATE OR DELETE ON forja.events
			FOR EACH ROW WHEN (false)
			EXECUTE FUNCTION forja.reject_event_mutation()`); err != nil {
			t.Fatalf("replace append-only trigger: %v", err)
		}
		if err := store.Ready(t.Context()); err == nil {
			t.Fatal("schema with a conditional append-only trigger reported ready")
		}
	})
	t.Run("identity generation", func(t *testing.T) {
		pool := migratedPool(t)
		store := newIntegrationStore(t, pool)
		if _, err := pool.Exec(
			t.Context(),
			"ALTER TABLE forja.outbox ALTER COLUMN outbox_id DROP IDENTITY",
		); err != nil {
			t.Fatalf("drop outbox identity: %v", err)
		}
		if err := store.Ready(t.Context()); err == nil {
			t.Fatal("schema without outbox identity reported ready")
		}
	})
	t.Run("required index", func(t *testing.T) {
		pool := migratedPool(t)
		store := newIntegrationStore(t, pool)
		if _, err := pool.Exec(
			t.Context(),
			"DROP INDEX forja.outbox_claim_idx",
		); err != nil {
			t.Fatalf("drop outbox claim index: %v", err)
		}
		if err := store.Ready(t.Context()); err == nil {
			t.Fatal("schema without the outbox claim index reported ready")
		}
	})
	t.Run("unexpected event trigger", func(t *testing.T) {
		pool := migratedPool(t)
		store := newIntegrationStore(t, pool)
		if _, err := pool.Exec(t.Context(), `
			CREATE FUNCTION forja.swallow_event_insert()
			RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
			    RETURN NULL;
			END
			$$;
			CREATE TRIGGER unexpected_event_insert
			BEFORE INSERT ON forja.events
			FOR EACH ROW EXECUTE FUNCTION forja.swallow_event_insert()`); err != nil {
			t.Fatalf("add event-swallowing trigger: %v", err)
		}
		if err := store.Ready(t.Context()); err == nil {
			t.Fatal("schema with an unexpected event trigger reported ready")
		}
		if _, err := store.CreateRun(
			t.Context(),
			mustRunID(t),
			"Reject swallowed event writes",
			testMetadata("swallowed-event"),
		); !fault.IsCode(err, fault.CodeInternal) {
			t.Fatalf("swallowed event write error = %v, want internal", err)
		}
		var runs int
		if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM forja.runs").Scan(&runs); err != nil {
			t.Fatalf("count runs after swallowed event: %v", err)
		}
		if runs != 0 {
			t.Fatalf("swallowed event committed %d aggregate rows", runs)
		}
	})
}

func TestPostgresVerifyRejectsSemanticSchemaDrift(t *testing.T) {
	for name, drift := range map[string]string{
		"disabled trigger": `
			ALTER TABLE forja.events DISABLE TRIGGER events_are_append_only`,
		"no-op trigger function": `
			CREATE OR REPLACE FUNCTION forja.reject_event_mutation()
			RETURNS trigger
			LANGUAGE plpgsql
			AS $$
			BEGIN
			    RETURN OLD;
			END
			$$`,
	} {
		t.Run(name, func(t *testing.T) {
			pool := migratedPool(t)
			verify := exec.CommandContext(
				t.Context(),
				"../../scripts/postgres_verify.sh",
				integrationDatabaseURL(t),
			)
			if output, err := verify.CombinedOutput(); err != nil {
				t.Fatalf("verify canonical schema: %v\n%s", err, output)
			}
			if _, err := pool.Exec(t.Context(), drift); err != nil {
				t.Fatalf("apply semantic drift: %v", err)
			}
			verify = exec.CommandContext(
				t.Context(),
				"../../scripts/postgres_verify.sh",
				integrationDatabaseURL(t),
			)
			if output, err := verify.CombinedOutput(); err == nil {
				t.Fatalf("verification accepted semantic drift\n%s", output)
			}
		})
	}
}

func TestPostgresVerifyRejectsDurabilityContradictions(t *testing.T) {
	t.Run("missing idempotency receipt", func(t *testing.T) {
		pool := migratedPool(t)
		store := newIntegrationStore(t, pool)
		if _, err := store.CreateRun(
			t.Context(),
			mustRunID(t),
			"Detect a lost idempotency receipt",
			testMetadata("verify-missing-receipt"),
		); err != nil {
			t.Fatalf("create run: %v", err)
		}
		if _, err := pool.Exec(
			t.Context(),
			"DELETE FROM forja.idempotency_keys",
		); err != nil {
			t.Fatalf("delete idempotency receipt: %v", err)
		}
		requirePostgresVerifyFailure(t)
	})
	t.Run("corrupt idempotency response", func(t *testing.T) {
		pool := migratedPool(t)
		store := newIntegrationStore(t, pool)
		if _, err := store.CreateRun(
			t.Context(),
			mustRunID(t),
			"Detect a corrupt idempotency response",
			testMetadata("verify-corrupt-receipt"),
		); err != nil {
			t.Fatalf("create run: %v", err)
		}
		if _, err := pool.Exec(t.Context(), `
			UPDATE forja.idempotency_keys
			SET response_body=jsonb_set(response_body, '{objective}', '"corrupt"')`,
		); err != nil {
			t.Fatalf("corrupt idempotency response: %v", err)
		}
		requirePostgresVerifyFailure(t)
	})
	t.Run("boolean attempt version cannot impersonate integer", func(t *testing.T) {
		pool := migratedPool(t)
		store := newIntegrationStore(t, pool)
		runID := mustRunID(t)
		if _, err := store.CreateRun(
			t.Context(),
			runID,
			"Reject JSON scalar type confusion",
			testMetadata("verify-attempt-type-run"),
		); err != nil {
			t.Fatalf("create run: %v", err)
		}
		lease, err := store.AcquireLease(
			t.Context(),
			persistence.LeaseKey{
				TenantID:     DefaultTenantID,
				ResourceType: "scheduler",
				ResourceID:   "verify-attempt-type",
			},
			"verify-attempt-type",
			time.Minute,
		)
		if err != nil {
			t.Fatalf("acquire lease: %v", err)
		}
		attempt, err := store.CreateAttempt(
			t.Context(),
			runID,
			"queued",
			testMetadata("verify-attempt-type-create"),
			persistence.LeaseProof{
				LeaseKey:     lease.LeaseKey,
				OwnerID:      lease.OwnerID,
				FencingToken: lease.FencingToken,
			},
		)
		if err != nil {
			t.Fatalf("create attempt: %v", err)
		}
		if _, err := pool.Exec(
			t.Context(),
			"ALTER TABLE forja.events DISABLE TRIGGER events_are_append_only",
		); err != nil {
			t.Fatalf("disable event mutation guard: %v", err)
		}
		if _, err := pool.Exec(t.Context(), `
			UPDATE forja.events
			SET payload=jsonb_set(payload, '{version}', 'true'::jsonb)
			WHERE aggregate_type='attempt' AND aggregate_id=$1`,
			attempt.AttemptID,
		); err != nil {
			t.Fatalf("corrupt attempt payload type: %v", err)
		}
		if _, err := pool.Exec(
			t.Context(),
			"ALTER TABLE forja.events ENABLE TRIGGER events_are_append_only",
		); err != nil {
			t.Fatalf("restore event mutation guard: %v", err)
		}
		requirePostgresVerifyFailure(t)
	})
	t.Run("boolean receipt version cannot impersonate integer", func(t *testing.T) {
		pool := migratedPool(t)
		store := newIntegrationStore(t, pool)
		runID := mustRunID(t)
		if _, err := store.CreateRun(
			t.Context(),
			runID,
			"Reject receipt scalar type confusion",
			testMetadata("verify-receipt-type-run"),
		); err != nil {
			t.Fatalf("create run: %v", err)
		}
		lease, err := store.AcquireLease(
			t.Context(),
			persistence.LeaseKey{
				TenantID:     DefaultTenantID,
				ResourceType: "scheduler",
				ResourceID:   "verify-receipt-type",
			},
			"verify-receipt-type",
			time.Minute,
		)
		if err != nil {
			t.Fatalf("acquire lease: %v", err)
		}
		if _, err := store.CreateAttempt(
			t.Context(),
			runID,
			"queued",
			testMetadata("verify-receipt-type-create"),
			persistence.LeaseProof{
				LeaseKey:     lease.LeaseKey,
				OwnerID:      lease.OwnerID,
				FencingToken: lease.FencingToken,
			},
		); err != nil {
			t.Fatalf("create attempt: %v", err)
		}
		if _, err := pool.Exec(t.Context(), `
			UPDATE forja.idempotency_keys
			SET response_body=jsonb_set(response_body, '{version}', 'true'::jsonb)
			WHERE scope LIKE 'create_attempt:%'`); err != nil {
			t.Fatalf("corrupt receipt payload type: %v", err)
		}
		requirePostgresVerifyFailure(t)
	})
	t.Run("missing outbox row", func(t *testing.T) {
		pool := migratedPool(t)
		store := newIntegrationStore(t, pool)
		if _, err := store.CreateRun(
			t.Context(),
			mustRunID(t),
			"Detect a lost outbox row",
			testMetadata("verify-missing-outbox"),
		); err != nil {
			t.Fatalf("create run: %v", err)
		}
		if _, err := pool.Exec(t.Context(), "DELETE FROM forja.outbox"); err != nil {
			t.Fatalf("delete outbox row: %v", err)
		}
		requirePostgresVerifyFailure(t)
	})
	t.Run("canonical run differs from replay", func(t *testing.T) {
		pool := migratedPool(t)
		store := newIntegrationStore(t, pool)
		runID := mustRunID(t)
		if _, err := store.CreateRun(
			t.Context(),
			runID,
			"Detect canonical run drift",
			testMetadata("verify-run-drift"),
		); err != nil {
			t.Fatalf("create run: %v", err)
		}
		if _, err := pool.Exec(t.Context(), `
			UPDATE forja.runs
			SET state='awaiting_approval', version=2,
			    updated_at=updated_at + interval '1 second'
			WHERE run_id=$1`,
			runID.String(),
		); err != nil {
			t.Fatalf("drift canonical run: %v", err)
		}
		if err := store.RebuildRunProjection(
			t.Context(),
			"canonical-drift",
		); !fault.IsCode(err, fault.CodeConflict) {
			t.Fatalf("runtime replay error = %v, want conflict", err)
		}
		requirePostgresVerifyFailure(t)
	})
	t.Run("attempt differs from creation event", func(t *testing.T) {
		pool := migratedPool(t)
		store := newIntegrationStore(t, pool)
		runID := mustRunID(t)
		if _, err := store.CreateRun(
			t.Context(),
			runID,
			"Detect canonical attempt drift",
			testMetadata("verify-attempt-run"),
		); err != nil {
			t.Fatalf("create run: %v", err)
		}
		lease, err := store.AcquireLease(
			t.Context(),
			persistence.LeaseKey{
				TenantID:     DefaultTenantID,
				ResourceType: "scheduler",
				ResourceID:   "verify-attempt",
			},
			"verify-scheduler",
			time.Minute,
		)
		if err != nil {
			t.Fatalf("acquire lease: %v", err)
		}
		if _, err := store.CreateAttempt(
			t.Context(),
			runID,
			"queued",
			testMetadata("verify-attempt-create"),
			persistence.LeaseProof{
				LeaseKey:     lease.LeaseKey,
				OwnerID:      lease.OwnerID,
				FencingToken: lease.FencingToken,
			},
		); err != nil {
			t.Fatalf("create attempt: %v", err)
		}
		if _, err := pool.Exec(t.Context(), "DELETE FROM forja.attempts"); err != nil {
			t.Fatalf("delete canonical attempt: %v", err)
		}
		requirePostgresVerifyFailure(t)
	})
}

func TestCanonicalEventsAreAppendOnly(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	runID := mustRunID(t)
	if _, err := store.CreateRun(
		t.Context(),
		runID,
		"Protect immutable events",
		testMetadata("immutable-event"),
	); err != nil {
		t.Fatalf("create event source: %v", err)
	}
	if _, err := pool.Exec(
		t.Context(),
		"UPDATE forja.events SET event_type='run.changed' WHERE aggregate_id=$1",
		runID.String(),
	); err == nil {
		t.Fatal("database allowed event mutation")
	}
	if _, err := pool.Exec(
		t.Context(),
		"DELETE FROM forja.events WHERE aggregate_id=$1",
		runID.String(),
	); err == nil {
		t.Fatal("database allowed event deletion")
	}
}

func TestRunStateSurvivesRestartAndCommandsReplay(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	runID := mustRunID(t)
	metadata := testMetadata("create-replay")

	created, err := store.CreateRun(t.Context(), runID, "Persist this run", metadata)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	replayed, err := store.CreateRun(t.Context(), runID, "Persist this run", metadata)
	if err != nil {
		t.Fatalf("replay create run: %v", err)
	}
	if replayed != created {
		t.Fatalf("replayed run differs: got %#v want %#v", replayed, created)
	}
	retryCandidate := mustRunID(t)
	replayedWithNewCandidate, err := store.CreateRun(
		t.Context(),
		retryCandidate,
		"Persist this run",
		metadata,
	)
	if err != nil {
		t.Fatalf("replay create run with a new generated candidate: %v", err)
	}
	if replayedWithNewCandidate != created {
		t.Fatalf(
			"retry with a new candidate differs: got %#v want %#v",
			replayedWithNewCandidate,
			created,
		)
	}

	pool.Close()
	reopened, err := Open(t.Context(), integrationDatabaseURL(t), 8)
	if err != nil {
		t.Fatalf("reopen pool: %v", err)
	}
	t.Cleanup(reopened.Close)
	restarted := newIntegrationStore(t, reopened)
	got, err := restarted.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatalf("read after restart: %v", err)
	}
	if got != created {
		t.Fatalf("run after restart differs: got %#v want %#v", got, created)
	}
	var events, outbox int
	if err := reopened.QueryRow(t.Context(), `
		SELECT
		  (SELECT count(*) FROM forja.events),
		  (SELECT count(*) FROM forja.outbox)`,
	).Scan(&events, &outbox); err != nil {
		t.Fatalf("count atomic records: %v", err)
	}
	if events != 1 || outbox != 1 {
		t.Fatalf("events/outbox = %d/%d, want 1/1", events, outbox)
	}
}

func TestPostgresTimestampsAreStableAtDatabasePrecision(t *testing.T) {
	pool := migratedPool(t)
	sourceTime := time.Date(2026, 7, 16, 12, 34, 56, 123456789, time.UTC)
	store, err := NewStore(
		pool,
		clock.Fixed{Time: sourceTime},
		DefaultTenantID,
		DefaultRepositoryID,
	)
	if err != nil {
		t.Fatalf("create fixed-clock store: %v", err)
	}
	runID := mustRunID(t)
	created, err := store.CreateRun(
		t.Context(),
		runID,
		"Normalize database timestamps",
		testMetadata("timestamp-create"),
	)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	wantTime := sourceTime.Truncate(time.Microsecond)
	if created.CreatedAt != wantTime || created.UpdatedAt != wantTime {
		t.Fatalf(
			"created timestamps = %s/%s, want %s",
			created.CreatedAt,
			created.UpdatedAt,
			wantTime,
		)
	}
	persisted, err := store.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatalf("read persisted run: %v", err)
	}
	if persisted != created {
		t.Fatalf("persisted run differs: got %#v want %#v", persisted, created)
	}
	if err := store.RebuildRunProjection(t.Context(), "timestamp-precision"); err != nil {
		t.Fatalf("replay precision-normalized event: %v", err)
	}
}

func TestIdempotencyBindsActorAndCausationIdentity(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	runID := mustRunID(t)
	metadata := testMetadata("audit-bound-create")
	if _, err := store.CreateRun(
		t.Context(),
		runID,
		"Bind audit identity",
		metadata,
	); err != nil {
		t.Fatalf("create run: %v", err)
	}
	differentActor := metadata
	differentActor.ActorID = "different-actor"
	if _, err := store.CreateRun(
		t.Context(),
		mustRunID(t),
		"Bind audit identity",
		differentActor,
	); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("different actor replay error = %v, want conflict", err)
	}
	differentCausation := metadata
	cause := "event_parent"
	differentCausation.CausationID = &cause
	if _, err := store.CreateRun(
		t.Context(),
		mustRunID(t),
		"Bind audit identity",
		differentCausation,
	); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("different causation replay error = %v, want conflict", err)
	}
}

func TestDuplicateAttemptCommandCreatesOneAttempt(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	runID := mustRunID(t)
	if _, err := store.CreateRun(
		t.Context(),
		runID,
		"Execute an idempotent attempt",
		testMetadata("create-run-attempt"),
	); err != nil {
		t.Fatalf("create run: %v", err)
	}
	metadata := testMetadata("attempt-replay")
	lease, err := store.AcquireLease(
		t.Context(),
		persistence.LeaseKey{
			TenantID:     DefaultTenantID,
			ResourceType: "scheduler",
			ResourceID:   "attempt-allocator",
		},
		"scheduler-attempts",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire scheduler lease: %v", err)
	}
	proof := persistence.LeaseProof{
		LeaseKey:     lease.LeaseKey,
		OwnerID:      lease.OwnerID,
		FencingToken: lease.FencingToken,
	}
	first, err := store.CreateAttempt(
		t.Context(),
		runID,
		"queued",
		metadata,
		proof,
	)
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	second, err := store.CreateAttempt(
		t.Context(),
		runID,
		"queued",
		metadata,
		proof,
	)
	if err != nil {
		t.Fatalf("replay attempt: %v", err)
	}
	if first != second {
		t.Fatalf("replayed attempt differs: got %#v want %#v", second, first)
	}
	var count int
	if err := pool.QueryRow(
		t.Context(),
		"SELECT count(*) FROM forja.attempts WHERE run_id=$1",
		runID.String(),
	).Scan(&count); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if count != 1 {
		t.Fatalf("attempt count = %d, want 1", count)
	}
	var events, outbox int
	if err := pool.QueryRow(t.Context(), `
		SELECT
		  (SELECT count(*) FROM forja.events
		   WHERE aggregate_type='attempt' AND aggregate_id=$1),
		  (SELECT count(*) FROM forja.outbox AS o
		   JOIN forja.events AS e ON e.event_id=o.event_id
		   WHERE e.aggregate_type='attempt' AND e.aggregate_id=$1)`,
		first.AttemptID,
	).Scan(&events, &outbox); err != nil {
		t.Fatalf("count attempt event/outbox: %v", err)
	}
	if events != 1 || outbox != 1 {
		t.Fatalf("attempt events/outbox = %d/%d, want 1/1", events, outbox)
	}
}

func TestRepositoryBoundStoreRejectsCrossRepositoryAuthority(t *testing.T) {
	pool := migratedPool(t)
	const otherRepositoryID = "00000000-0000-4000-8000-000000000003"
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.repositories (
			repository_id, tenant_id, canonical_name
		) VALUES ($1, $2, 'local/other')`,
		otherRepositoryID,
		DefaultTenantID,
	); err != nil {
		t.Fatalf("create second repository: %v", err)
	}
	first := newIntegrationStore(t, pool)
	second, err := NewStore(pool, nil, DefaultTenantID, otherRepositoryID)
	if err != nil {
		t.Fatalf("create second repository store: %v", err)
	}
	sharedMetadata := testMetadata("cross-repository-idempotency")
	firstID := mustRunID(t)
	secondID := mustRunID(t)
	firstRun, err := first.CreateRun(
		t.Context(),
		firstID,
		"Repository one command",
		sharedMetadata,
	)
	if err != nil {
		t.Fatalf("create first repository run: %v", err)
	}
	secondRun, err := second.CreateRun(
		t.Context(),
		secondID,
		"Repository two command",
		sharedMetadata,
	)
	if err != nil {
		t.Fatalf("create second repository run: %v", err)
	}
	if firstRun.RunID == secondRun.RunID {
		t.Fatal("repository-scoped commands unexpectedly replayed one result")
	}
	if _, err := second.GetRun(
		t.Context(),
		firstID,
	); !fault.IsCode(err, fault.CodeNotFound) {
		t.Fatalf("cross-repository read error = %v, want not found", err)
	}
	if _, err := second.TransitionRun(
		t.Context(),
		firstID,
		firstRun.Version,
		runstate.StateAwaitingApproval,
		testMetadata("cross-repository-transition"),
	); !fault.IsCode(err, fault.CodeNotFound) {
		t.Fatalf("cross-repository transition error = %v, want not found", err)
	}
	repositoryLeaseKey := persistence.LeaseKey{
		TenantID:     DefaultTenantID,
		ResourceType: "scheduler",
		ResourceID:   "cross-repository-attempt",
	}
	firstLease, err := first.AcquireLease(
		t.Context(),
		repositoryLeaseKey,
		"scheduler-default",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire first repository lease: %v", err)
	}
	lease, err := second.AcquireLease(
		t.Context(),
		repositoryLeaseKey,
		"scheduler-other",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire independent second repository lease: %v", err)
	}
	if firstLease.OwnerID == lease.OwnerID {
		t.Fatal("repository-scoped leases unexpectedly share one owner")
	}
	if _, err := second.CreateAttempt(
		t.Context(),
		secondID,
		"queued",
		testMetadata("foreign-repository-proof"),
		persistence.LeaseProof{
			LeaseKey:     firstLease.LeaseKey,
			OwnerID:      firstLease.OwnerID,
			FencingToken: firstLease.FencingToken,
		},
	); !fault.IsCode(err, fault.CodeInvalidArgument) {
		t.Fatalf("foreign repository proof error = %v, want invalid argument", err)
	}
	if _, err := second.CreateAttempt(
		t.Context(),
		firstID,
		"queued",
		testMetadata("cross-repository-attempt"),
		persistence.LeaseProof{
			LeaseKey:     lease.LeaseKey,
			OwnerID:      lease.OwnerID,
			FencingToken: lease.FencingToken,
		},
	); !fault.IsCode(err, fault.CodeNotFound) {
		t.Fatalf("cross-repository attempt error = %v, want not found", err)
	}
	if err := first.RebuildRunProjection(t.Context(), "repo-index"); err != nil {
		t.Fatalf("rebuild first repository projection: %v", err)
	}
	if err := second.RebuildRunProjection(t.Context(), "repo-index"); err != nil {
		t.Fatalf("rebuild second repository projection: %v", err)
	}
	var firstCount, secondCount int
	if err := pool.QueryRow(t.Context(), `
		SELECT
		  count(*) FILTER (WHERE repository_id=$1),
		  count(*) FILTER (WHERE repository_id=$2)
		FROM forja.run_projections
		WHERE tenant_id=$3 AND projector_name='repo-index'`,
		DefaultRepositoryID,
		otherRepositoryID,
		DefaultTenantID,
	).Scan(&firstCount, &secondCount); err != nil {
		t.Fatalf("count repository projections: %v", err)
	}
	if firstCount != 1 || secondCount != 1 {
		t.Fatalf(
			"repository projection counts = %d/%d, want 1/1",
			firstCount,
			secondCount,
		)
	}
}

func TestRunCannotReferenceSprintFromAnotherRepository(t *testing.T) {
	pool := migratedPool(t)
	const (
		otherRepositoryID = "00000000-0000-4000-8000-000000000003"
		sprintID          = "00000000-0000-4000-8000-000000000004"
	)
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.repositories (
			repository_id, tenant_id, canonical_name
		) VALUES ($1, $2, 'local/cross-sprint')`,
		otherRepositoryID,
		DefaultTenantID,
	); err != nil {
		t.Fatalf("create second repository: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.sprints (
			sprint_id, tenant_id, repository_id, sequence_number, title
		) VALUES ($1, $2, $3, 2, 'Default repository sprint')`,
		sprintID,
		DefaultTenantID,
		DefaultRepositoryID,
	); err != nil {
		t.Fatalf("create default repository sprint: %v", err)
	}
	now := time.Now().UTC()
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.runs (
			run_id, tenant_id, repository_id, sprint_id, objective,
			state, version, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 'Reject foreign sprint', 'draft', 1, $5, $5)`,
		mustRunID(t).String(),
		DefaultTenantID,
		otherRepositoryID,
		sprintID,
		now,
	); err == nil {
		t.Fatal("run accepted a sprint owned by another repository")
	}
}

func TestAttemptCannotCommitAfterLeaseExpiresWhileWaitingForRun(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	runID := mustRunID(t)
	if _, err := store.CreateRun(
		t.Context(),
		runID,
		"Expire a blocked attempt lease",
		testMetadata("blocked-attempt-run"),
	); err != nil {
		t.Fatalf("create run: %v", err)
	}
	lease, err := store.AcquireLease(
		t.Context(),
		persistence.LeaseKey{
			TenantID:     DefaultTenantID,
			ResourceType: "scheduler",
			ResourceID:   "blocked-attempt",
		},
		"blocked-scheduler",
		25*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("acquire short scheduler lease: %v", err)
	}
	blocker, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin run lock holder: %v", err)
	}
	defer func() { _ = blocker.Rollback(t.Context()) }()
	if _, err := blocker.Exec(t.Context(), `
		SELECT 1
		FROM forja.runs
		WHERE tenant_id=$1 AND repository_id=$2 AND run_id=$3
		FOR UPDATE`,
		DefaultTenantID,
		DefaultRepositoryID,
		runID.String(),
	); err != nil {
		t.Fatalf("lock run: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		_, createErr := store.CreateAttempt(
			context.Background(),
			runID,
			"queued",
			testMetadata("blocked-attempt-write"),
			persistence.LeaseProof{
				LeaseKey:     lease.LeaseKey,
				OwnerID:      lease.OwnerID,
				FencingToken: lease.FencingToken,
			},
		)
		result <- createErr
	}()
	time.Sleep(50 * time.Millisecond)
	if err := blocker.Commit(t.Context()); err != nil {
		t.Fatalf("release run lock: %v", err)
	}
	select {
	case err := <-result:
		if !fault.IsCode(err, fault.CodeConflict) {
			t.Fatalf("expired blocked attempt error = %v, want conflict", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked attempt did not return after run lock release")
	}
	var count int
	if err := pool.QueryRow(
		t.Context(),
		"SELECT count(*) FROM forja.attempts WHERE run_id=$1",
		runID.String(),
	).Scan(&count); err != nil {
		t.Fatalf("count blocked attempts: %v", err)
	}
	if count != 0 {
		t.Fatalf("expired lease created %d blocked attempts", count)
	}
}

func TestAttemptChecksLeaseAfterWaitingForWatermark(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	runID := mustRunID(t)
	if _, err := store.CreateRun(
		t.Context(),
		runID,
		"Fence a watermark-blocked attempt",
		testMetadata("watermark-attempt-run"),
	); err != nil {
		t.Fatalf("create run: %v", err)
	}
	lease, err := store.AcquireLease(
		t.Context(),
		persistence.LeaseKey{
			TenantID:     DefaultTenantID,
			ResourceType: "scheduler",
			ResourceID:   "watermark-attempt",
		},
		"watermark-scheduler",
		25*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("acquire short scheduler lease: %v", err)
	}
	blocker, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin watermark lock holder: %v", err)
	}
	defer func() { _ = blocker.Rollback(t.Context()) }()
	if _, err := blocker.Exec(
		t.Context(),
		"SELECT pg_advisory_xact_lock($1)",
		advisoryLockKey(outboxWatermarkLock),
	); err != nil {
		t.Fatalf("hold watermark lock: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		_, createErr := store.CreateAttempt(
			context.Background(),
			runID,
			"queued",
			testMetadata("watermark-attempt-write"),
			persistence.LeaseProof{
				LeaseKey:     lease.LeaseKey,
				OwnerID:      lease.OwnerID,
				FencingToken: lease.FencingToken,
			},
		)
		result <- createErr
	}()
	time.Sleep(50 * time.Millisecond)
	if err := blocker.Commit(t.Context()); err != nil {
		t.Fatalf("release watermark lock: %v", err)
	}
	select {
	case err := <-result:
		if !fault.IsCode(err, fault.CodeConflict) {
			t.Fatalf("expired watermark attempt error = %v, want conflict", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watermark-blocked attempt did not return")
	}
	var count int
	if err := pool.QueryRow(
		t.Context(),
		"SELECT count(*) FROM forja.attempts WHERE run_id=$1",
		runID.String(),
	).Scan(&count); err != nil {
		t.Fatalf("count watermark-blocked attempts: %v", err)
	}
	if count != 0 {
		t.Fatalf("expired lease created %d watermark-blocked attempts", count)
	}
}

func TestAttemptFinalFenceUsesDatabaseClock(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	runID := mustRunID(t)
	if _, err := store.CreateRun(
		t.Context(),
		runID,
		"Expire an attempt at its final fence",
		testMetadata("final-fence-run"),
	); err != nil {
		t.Fatalf("create run: %v", err)
	}
	lease, err := store.AcquireLease(
		t.Context(),
		persistence.LeaseKey{
			TenantID:     DefaultTenantID,
			ResourceType: "scheduler",
			ResourceID:   "final-fence-attempt",
		},
		"final-fence-scheduler",
		25*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("acquire scheduler lease: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		CREATE FUNCTION forja.delay_receipt_insert()
		RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
		    PERFORM pg_sleep(0.05);
		    RETURN NEW;
		END
		$$;
		CREATE TRIGGER delay_receipt_insert
		BEFORE INSERT ON forja.idempotency_keys
		FOR EACH ROW EXECUTE FUNCTION forja.delay_receipt_insert()`); err != nil {
		t.Fatalf("install receipt delay: %v", err)
	}
	_, err = store.CreateAttempt(
		t.Context(),
		runID,
		"queued",
		testMetadata("final-fence-attempt"),
		persistence.LeaseProof{
			LeaseKey:     lease.LeaseKey,
			OwnerID:      lease.OwnerID,
			FencingToken: lease.FencingToken,
		},
	)
	if !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("expired final fence error = %v, want conflict", err)
	}
	var attempts, events, receipts int
	if err := pool.QueryRow(t.Context(), `
		SELECT
		  (SELECT count(*) FROM forja.attempts),
		  (SELECT count(*) FROM forja.events WHERE aggregate_type='attempt'),
		  (SELECT count(*) FROM forja.idempotency_keys
		   WHERE scope LIKE 'create_attempt:%')`,
	).Scan(&attempts, &events, &receipts); err != nil {
		t.Fatalf("count rolled-back attempt records: %v", err)
	}
	if attempts != 0 || events != 0 || receipts != 0 {
		t.Fatalf(
			"expired final fence committed attempts/events/receipts=%d/%d/%d",
			attempts,
			events,
			receipts,
		)
	}
}

func TestAttemptCommitFenceUsesDatabaseClock(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	runID := mustRunID(t)
	if _, err := store.CreateRun(
		t.Context(),
		runID,
		"Expire an attempt while committing",
		testMetadata("commit-fence-run"),
	); err != nil {
		t.Fatalf("create run: %v", err)
	}
	lease, err := store.AcquireLease(
		t.Context(),
		persistence.LeaseKey{
			TenantID:     DefaultTenantID,
			ResourceType: "scheduler",
			ResourceID:   "commit-fence-attempt",
		},
		"commit-fence-scheduler",
		100*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("acquire scheduler lease: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		CREATE FUNCTION forja.delay_attempt_commit()
		RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
		    PERFORM pg_sleep(0.2);
		    RETURN NEW;
		END
		$$;
		CREATE CONSTRAINT TRIGGER aa_delay_attempt_commit
		AFTER INSERT ON forja.attempts
		DEFERRABLE INITIALLY DEFERRED
		FOR EACH ROW EXECUTE FUNCTION forja.delay_attempt_commit()`); err != nil {
		t.Fatalf("install attempt commit delay: %v", err)
	}
	_, err = store.CreateAttempt(
		t.Context(),
		runID,
		"queued",
		testMetadata("commit-fence-attempt"),
		persistence.LeaseProof{
			LeaseKey:     lease.LeaseKey,
			OwnerID:      lease.OwnerID,
			FencingToken: lease.FencingToken,
		},
	)
	if !fault.IsCode(err, fault.CodeConflict) ||
		!strings.Contains(err.Error(), "postgres.CreateAttempt.commit") {
		t.Fatalf("expired commit fence error = %v, want commit conflict", err)
	}
	var attempts int
	if err := pool.QueryRow(
		t.Context(),
		"SELECT count(*) FROM forja.attempts",
	).Scan(&attempts); err != nil {
		t.Fatalf("count commit-fenced attempts: %v", err)
	}
	if attempts != 0 {
		t.Fatalf("expired commit fence persisted %d attempts", attempts)
	}
}

func TestStaleSchedulerCannotCreateAttempt(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	runID := mustRunID(t)
	if _, err := store.CreateRun(
		t.Context(),
		runID,
		"Fence stale attempt writers",
		testMetadata("fence-attempt-run"),
	); err != nil {
		t.Fatalf("create run: %v", err)
	}
	key := persistence.LeaseKey{
		TenantID:     DefaultTenantID,
		ResourceType: "scheduler",
		ResourceID:   "attempt-allocator",
	}
	first, err := store.AcquireLease(
		t.Context(),
		key,
		"scheduler-old",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	if err := store.ReleaseLease(
		t.Context(),
		key,
		first.OwnerID,
		first.FencingToken,
	); err != nil {
		t.Fatalf("release first lease: %v", err)
	}
	second, err := store.AcquireLease(
		t.Context(),
		key,
		"scheduler-new",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("take over scheduler lease: %v", err)
	}
	if second.FencingToken <= first.FencingToken {
		t.Fatalf("takeover token = %d, first = %d", second.FencingToken, first.FencingToken)
	}
	_, err = store.CreateAttempt(
		t.Context(),
		runID,
		"queued",
		testMetadata("stale-attempt-write"),
		persistence.LeaseProof{
			LeaseKey:     first.LeaseKey,
			OwnerID:      first.OwnerID,
			FencingToken: first.FencingToken,
		},
	)
	if !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("stale attempt error = %v, want conflict", err)
	}
	var count int
	if err := pool.QueryRow(
		t.Context(),
		"SELECT count(*) FROM forja.attempts WHERE run_id=$1",
		runID.String(),
	).Scan(&count); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if count != 0 {
		t.Fatalf("stale scheduler created %d attempts", count)
	}
}

func TestAttemptReplayRequiresCurrentBoundSchedulerLease(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	runID := mustRunID(t)
	if _, err := store.CreateRun(
		t.Context(),
		runID,
		"Fence replayed attempt receipts",
		testMetadata("attempt-fence-run"),
	); err != nil {
		t.Fatalf("create run: %v", err)
	}
	key := persistence.LeaseKey{
		TenantID:     DefaultTenantID,
		ResourceType: "scheduler",
		ResourceID:   "attempt-replay",
	}
	oldLease, err := store.AcquireLease(
		t.Context(),
		key,
		"scheduler-old",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire old scheduler lease: %v", err)
	}
	metadata := testMetadata("attempt-fenced-replay")
	oldProof := persistence.LeaseProof{
		LeaseKey:     oldLease.LeaseKey,
		OwnerID:      oldLease.OwnerID,
		FencingToken: oldLease.FencingToken,
	}
	if _, err := store.CreateAttempt(
		t.Context(),
		runID,
		"queued",
		metadata,
		oldProof,
	); err != nil {
		t.Fatalf("create first attempt: %v", err)
	}
	if err := store.ReleaseLease(
		t.Context(),
		key,
		oldLease.OwnerID,
		oldLease.FencingToken,
	); err != nil {
		t.Fatalf("release old scheduler lease: %v", err)
	}
	newLease, err := store.AcquireLease(
		t.Context(),
		key,
		"scheduler-new",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire replacement scheduler lease: %v", err)
	}
	if _, err := store.CreateAttempt(
		t.Context(),
		runID,
		"queued",
		metadata,
		oldProof,
	); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("stale replay error = %v, want conflict", err)
	}
	newProof := persistence.LeaseProof{
		LeaseKey:     newLease.LeaseKey,
		OwnerID:      newLease.OwnerID,
		FencingToken: newLease.FencingToken,
	}
	if _, err := store.CreateAttempt(
		t.Context(),
		runID,
		"queued",
		metadata,
		newProof,
	); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("cross-owner replay error = %v, want conflict", err)
	}
}

func TestIdempotencyKeyMayBeScopedAcrossAggregates(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	runs := []identity.RunID{mustRunID(t), mustRunID(t)}
	for index, runID := range runs {
		if _, err := store.CreateRun(
			t.Context(),
			runID,
			fmt.Sprintf("Scoped idempotency run %d", index),
			testMetadata(fmt.Sprintf("scope-create-%d", index)),
		); err != nil {
			t.Fatalf("create run %d: %v", index, err)
		}
	}
	for index, runID := range runs {
		if _, err := store.TransitionRun(
			t.Context(),
			runID,
			1,
			runstate.StateAwaitingApproval,
			testMetadata("shared-transition-key"),
		); err != nil {
			t.Fatalf("transition run %d with scoped key: %v", index, err)
		}
	}
}

func TestConcurrentLeaseOwnershipUsesFencing(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	key := persistence.LeaseKey{
		TenantID:     DefaultTenantID,
		ResourceType: "scheduler",
		ResourceID:   "global",
	}
	type result struct {
		lease persistence.Lease
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for _, owner := range []string{"scheduler-a", "scheduler-b"} {
		wait.Add(1)
		go func(ownerID string) {
			defer wait.Done()
			<-start
			lease, err := store.AcquireLease(
				context.Background(),
				key,
				ownerID,
				10*time.Second,
			)
			results <- result{lease: lease, err: err}
		}(owner)
	}
	close(start)
	wait.Wait()
	close(results)

	successes := make([]persistence.Lease, 0, 1)
	conflicts := 0
	for item := range results {
		if item.err == nil {
			successes = append(successes, item.lease)
		} else if fault.IsCode(item.err, fault.CodeConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected lease result: %v", item.err)
		}
	}
	if len(successes) != 1 || conflicts != 1 {
		t.Fatalf("lease results = %d success, %d conflicts; want 1/1", len(successes), conflicts)
	}
	winner := successes[0]
	if err := store.ReleaseLease(
		t.Context(),
		key,
		winner.OwnerID,
		winner.FencingToken,
	); err != nil {
		t.Fatalf("release lease: %v", err)
	}
	takeover, err := store.AcquireLease(
		t.Context(),
		key,
		"scheduler-takeover",
		10*time.Second,
	)
	if err != nil {
		t.Fatalf("take over released lease: %v", err)
	}
	if takeover.FencingToken <= winner.FencingToken {
		t.Fatalf(
			"takeover fencing token = %d, want > %d",
			takeover.FencingToken,
			winner.FencingToken,
		)
	}
	if _, err := store.RenewLease(
		t.Context(),
		key,
		winner.OwnerID,
		winner.FencingToken,
		time.Second,
	); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("stale renewal error = %v, want conflict", err)
	}
}

func TestTenantBoundStoreRejectsForeignLeaseKey(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	_, err := store.AcquireLease(
		t.Context(),
		persistence.LeaseKey{
			TenantID:     "00000000-0000-4000-8000-000000000099",
			ResourceType: "scheduler",
			ResourceID:   "foreign-tenant",
		},
		"scheduler",
		time.Minute,
	)
	if !fault.IsCode(err, fault.CodeInvalidArgument) {
		t.Fatalf("foreign tenant lease error = %v, want invalid argument", err)
	}
}

func TestOutboxClaimsDoNotOverlapAndDeadLetter(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	for index := 0; index < 2; index++ {
		if _, err := store.CreateRun(
			t.Context(),
			mustRunID(t),
			fmt.Sprintf("Outbox message %d", index),
			testMetadata(fmt.Sprintf("outbox-create-%d", index)),
		); err != nil {
			t.Fatalf("create outbox source %d: %v", index, err)
		}
	}
	type claimResult struct {
		messages []persistence.OutboxMessage
		err      error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	var wait sync.WaitGroup
	for _, worker := range []string{"projector-a", "projector-b"} {
		wait.Add(1)
		go func(workerID string) {
			defer wait.Done()
			<-start
			messages, err := store.ClaimOutbox(
				context.Background(),
				workerID,
				1,
				time.Minute,
			)
			results <- claimResult{messages: messages, err: err}
		}(worker)
	}
	close(start)
	wait.Wait()
	close(results)

	seen := make(map[int64]persistence.OutboxMessage)
	for result := range results {
		if result.err != nil {
			t.Fatalf("claim outbox: %v", result.err)
		}
		if len(result.messages) != 1 {
			t.Fatalf("claim count = %d, want 1", len(result.messages))
		}
		message := result.messages[0]
		if _, duplicate := seen[message.OutboxID]; duplicate {
			t.Fatalf("outbox row %d was claimed twice", message.OutboxID)
		}
		seen[message.OutboxID] = message
	}
	for _, message := range seen {
		var owner string
		if err := pool.QueryRow(t.Context(), `
			SELECT locked_by FROM forja.outbox WHERE outbox_id=$1`,
			message.OutboxID,
		).Scan(&owner); err != nil {
			t.Fatalf("read outbox owner: %v", err)
		}
		if err := store.FailOutbox(
			t.Context(),
			message.OutboxID,
			owner,
			message.FencingToken,
			errors.New("projection failed"),
			time.Now().UTC(),
			1,
		); err != nil {
			t.Fatalf("dead-letter outbox: %v", err)
		}
	}
	var dead int
	if err := pool.QueryRow(
		t.Context(),
		"SELECT count(*) FROM forja.projection_dead_letters",
	).Scan(&dead); err != nil {
		t.Fatalf("count dead letters: %v", err)
	}
	if dead != 2 {
		t.Fatalf("dead-letter count = %d, want 2", dead)
	}
}

func TestExpiredOutboxClaimCannotPublish(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	if _, err := store.CreateRun(
		t.Context(),
		mustRunID(t),
		"Reject stale outbox ownership",
		testMetadata("outbox-expiry-create"),
	); err != nil {
		t.Fatalf("create outbox source: %v", err)
	}
	messages, err := store.ClaimOutbox(
		t.Context(),
		"stale-projector",
		1,
		time.Millisecond,
	)
	if err != nil {
		t.Fatalf("claim outbox: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("claim count = %d, want 1", len(messages))
	}
	time.Sleep(5 * time.Millisecond)
	err = store.CompleteOutbox(
		t.Context(),
		messages[0].OutboxID,
		"stale-projector",
		messages[0].FencingToken,
	)
	if !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("expired completion error = %v, want conflict", err)
	}
	err = store.FailOutbox(
		t.Context(),
		messages[0].OutboxID,
		"stale-projector",
		messages[0].FencingToken,
		errors.New("late projection failure"),
		time.Now().UTC().Add(time.Minute),
		3,
	)
	if !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("expired failure error = %v, want conflict", err)
	}
	reclaimed, err := store.ClaimOutbox(
		t.Context(),
		"replacement-projector",
		1,
		time.Minute,
	)
	if err != nil {
		t.Fatalf("reclaim outbox: %v", err)
	}
	if len(reclaimed) != 1 ||
		reclaimed[0].FencingToken <= messages[0].FencingToken {
		t.Fatalf("replacement claim = %#v", reclaimed)
	}
}

func TestFailOutboxRechecksClaimAtFinalWrite(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	if _, err := store.CreateRun(
		t.Context(),
		mustRunID(t),
		"Fence a delayed outbox failure",
		testMetadata("outbox-final-fence"),
	); err != nil {
		t.Fatalf("create outbox source: %v", err)
	}
	messages, err := store.ClaimOutbox(
		t.Context(),
		"delayed-dispatcher",
		1,
		25*time.Millisecond,
	)
	if err != nil || len(messages) != 1 {
		t.Fatalf("claim outbox: messages=%#v err=%v", messages, err)
	}
	if _, err := pool.Exec(t.Context(), `
		CREATE FUNCTION forja.delay_dead_letter_insert()
		RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
		    PERFORM pg_sleep(0.05);
		    RETURN NEW;
		END
		$$;
		CREATE TRIGGER delay_dead_letter_insert
		BEFORE INSERT ON forja.projection_dead_letters
		FOR EACH ROW EXECUTE FUNCTION forja.delay_dead_letter_insert()`); err != nil {
		t.Fatalf("install dead-letter delay: %v", err)
	}
	err = store.FailOutbox(
		t.Context(),
		messages[0].OutboxID,
		"delayed-dispatcher",
		messages[0].FencingToken,
		errors.New("delayed terminal failure"),
		time.Now().UTC(),
		1,
	)
	if !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("delayed failure error = %v, want conflict", err)
	}
	var deadLetters int
	if err := pool.QueryRow(
		t.Context(),
		"SELECT count(*) FROM forja.projection_dead_letters",
	).Scan(&deadLetters); err != nil {
		t.Fatalf("count dead letters: %v", err)
	}
	if deadLetters != 0 {
		t.Fatalf("expired failure committed %d dead letters", deadLetters)
	}
}

func TestOutboxCommitFenceUsesDatabaseClock(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	if _, err := store.CreateRun(
		t.Context(),
		mustRunID(t),
		"Expire an outbox claim while committing",
		testMetadata("outbox-commit-fence"),
	); err != nil {
		t.Fatalf("create outbox source: %v", err)
	}
	messages, err := store.ClaimOutbox(
		t.Context(),
		"commit-fenced-dispatcher",
		1,
		100*time.Millisecond,
	)
	if err != nil || len(messages) != 1 {
		t.Fatalf("claim outbox: messages=%#v err=%v", messages, err)
	}
	if _, err := pool.Exec(t.Context(), `
		CREATE FUNCTION forja.delay_outbox_commit()
		RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
		    PERFORM pg_sleep(0.2);
		    RETURN NEW;
		END
		$$;
		CREATE CONSTRAINT TRIGGER aa_delay_outbox_commit
		AFTER UPDATE ON forja.outbox
		DEFERRABLE INITIALLY DEFERRED
		FOR EACH ROW
		WHEN (OLD.state='inflight' AND NEW.state<>'inflight')
		EXECUTE FUNCTION forja.delay_outbox_commit()`); err != nil {
		t.Fatalf("install outbox commit delay: %v", err)
	}
	err = store.FailOutbox(
		t.Context(),
		messages[0].OutboxID,
		"commit-fenced-dispatcher",
		messages[0].FencingToken,
		errors.New("commit-time projection failure"),
		time.Now().UTC(),
		1,
	)
	if !fault.IsCode(err, fault.CodeConflict) ||
		!strings.Contains(err.Error(), "postgres.FailOutbox.commit") {
		t.Fatalf("expired outbox commit error = %v, want commit conflict", err)
	}
	var state string
	if err := pool.QueryRow(
		t.Context(),
		"SELECT state FROM forja.outbox WHERE outbox_id=$1",
		messages[0].OutboxID,
	).Scan(&state); err != nil {
		t.Fatalf("read commit-fenced outbox row: %v", err)
	}
	if state != "inflight" {
		t.Fatalf("expired outbox commit changed state to %q", state)
	}
}

func TestEventReplayRebuildsExpectedProjection(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	runID := mustRunID(t)
	created, err := store.CreateRun(
		t.Context(),
		runID,
		"Rebuild this read model",
		testMetadata("projection-create"),
	)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	updated, err := store.TransitionRun(
		t.Context(),
		runID,
		created.Version,
		runstate.StateAwaitingApproval,
		testMetadata("projection-transition"),
	)
	if err != nil {
		t.Fatalf("transition run: %v", err)
	}
	if err := store.RebuildRunProjection(t.Context(), "run-index"); err != nil {
		t.Fatalf("rebuild projection: %v", err)
	}
	var state string
	var version int
	if err := pool.QueryRow(t.Context(), `
		SELECT state, aggregate_version
		FROM forja.run_projections
		WHERE tenant_id=$1 AND repository_id=$2
		  AND projector_name='run-index' AND run_id=$3`,
		DefaultTenantID,
		DefaultRepositoryID,
		runID.String(),
	).Scan(&state, &version); err != nil {
		t.Fatalf("read projection: %v", err)
	}
	if state != updated.State || version != updated.Version {
		t.Fatalf(
			"projection state/version = %s/%d, want %s/%d",
			state,
			version,
			updated.State,
			updated.Version,
		)
	}
}

func TestReplayRejectsCorruptEventStream(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	fixture, err := os.ReadFile("testdata/corrupt_event_gap.sql")
	if err != nil {
		t.Fatalf("read corruption fixture: %v", err)
	}
	_, err = pool.Exec(t.Context(), string(fixture))
	if err != nil {
		t.Fatalf("insert corruption fixture: %v", err)
	}
	err = store.RebuildRunProjection(t.Context(), "corruption-test")
	if !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("rebuild error = %v, want conflict", err)
	}
}

func TestReplayRejectsSemanticallyInvalidEventStream(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	runID := mustRunID(t)
	created, err := store.CreateRun(
		t.Context(),
		runID,
		"Reject semantic event corruption",
		testMetadata("semantic-corruption-create"),
	)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	corrupt := created
	corrupt.State = string(runstate.StateCompleted)
	corrupt.Version = 2
	corrupt.UpdatedAt = corrupt.UpdatedAt.Add(time.Second)
	payload, err := json.Marshal(corrupt)
	if err != nil {
		t.Fatalf("encode corrupt payload: %v", err)
	}
	eventID, err := newPrefixedID("event")
	if err != nil {
		t.Fatalf("generate corrupt event ID: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.events (
			event_id, tenant_id, repository_id, aggregate_type, aggregate_id,
			aggregate_version, event_type, schema_version, occurred_at,
			actor_type, actor_id, correlation_id, idempotency_key, payload
		) VALUES (
			$1, $2, $3, 'run', $4, 2, 'run.transitioned', '1.0', $5,
			'system', 'corruption-fixture', 'semantic-corruption',
			'semantic-corruption', $6
		)`,
		eventID,
		DefaultTenantID,
		DefaultRepositoryID,
		runID.String(),
		corrupt.UpdatedAt,
		payload,
	); err != nil {
		t.Fatalf("insert semantic corruption: %v", err)
	}
	verify := exec.CommandContext(
		t.Context(),
		"../../scripts/postgres_verify.sh",
		integrationDatabaseURL(t),
	)
	if output, err := verify.CombinedOutput(); err == nil {
		t.Fatalf("external verifier accepted semantic corruption\n%s", output)
	}
	if err := store.RebuildRunProjection(
		t.Context(),
		"semantic-corruption-test",
	); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("semantic rebuild error = %v, want conflict", err)
	}
}

func TestEventWritersAndProjectionShareWatermarkLock(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	locker, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin lock holder: %v", err)
	}
	defer func() { _ = locker.Rollback(t.Context()) }()
	if _, err := locker.Exec(
		t.Context(),
		"SELECT pg_advisory_xact_lock($1)",
		advisoryLockKey(outboxWatermarkLock),
	); err != nil {
		t.Fatalf("hold watermark lock: %v", err)
	}
	createResult := make(chan error, 1)
	runID := mustRunID(t)
	go func() {
		_, createErr := store.CreateRun(
			context.Background(),
			runID,
			"Wait for the event watermark protocol",
			testMetadata("watermark-lock-create"),
		)
		createResult <- createErr
	}()
	select {
	case err := <-createResult:
		t.Fatalf("event writer bypassed watermark lock: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if err := locker.Commit(t.Context()); err != nil {
		t.Fatalf("release watermark lock: %v", err)
	}
	select {
	case err := <-createResult:
		if err != nil {
			t.Fatalf("event writer after lock release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event writer remained blocked after watermark release")
	}
	if err := store.RebuildRunProjection(t.Context(), "watermark-test"); err != nil {
		t.Fatalf("rebuild projection: %v", err)
	}
	var checkpoint, maximum int64
	if err := pool.QueryRow(t.Context(), `
		SELECT c.last_outbox_id, COALESCE(max(o.outbox_id), 0)
		FROM forja.projection_checkpoints AS c
		LEFT JOIN forja.outbox AS o
		  ON o.tenant_id=c.tenant_id AND o.repository_id=c.repository_id
		WHERE c.tenant_id=$1 AND c.repository_id=$2
		  AND c.projector_name='watermark-test'
		GROUP BY c.last_outbox_id`,
		DefaultTenantID,
		DefaultRepositoryID,
	).Scan(&checkpoint, &maximum); err != nil {
		t.Fatalf("read watermark: %v", err)
	}
	if checkpoint != maximum {
		t.Fatalf("checkpoint=%d max=%d", checkpoint, maximum)
	}
}

func TestBackupRestoreRoundTrip(t *testing.T) {
	if os.Getenv("FORJA_TEST_BACKUP_RESTORE") != "1" {
		t.Skip("FORJA_TEST_BACKUP_RESTORE is not enabled")
	}
	for _, command := range []string{"pg_dump", "pg_restore", "psql"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Skipf("%s is unavailable", command)
		}
	}
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	runID := mustRunID(t)
	if _, err := store.CreateRun(
		t.Context(),
		runID,
		"Survive a full backup and restore",
		testMetadata("backup-restore-run"),
	); err != nil {
		t.Fatalf("create backup source: %v", err)
	}
	lease, err := store.AcquireLease(
		t.Context(),
		persistence.LeaseKey{
			TenantID:     DefaultTenantID,
			ResourceType: "scheduler",
			ResourceID:   "backup-restore-attempt",
		},
		"backup-restore-scheduler",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire backup scheduler lease: %v", err)
	}
	attempt, err := store.CreateAttempt(
		t.Context(),
		runID,
		"queued",
		testMetadata("backup-restore-attempt"),
		persistence.LeaseProof{
			LeaseKey:     lease.LeaseKey,
			OwnerID:      lease.OwnerID,
			FencingToken: lease.FencingToken,
		},
	)
	if err != nil {
		t.Fatalf("create backup attempt: %v", err)
	}
	verify := exec.CommandContext(
		t.Context(),
		"../../scripts/postgres_verify.sh",
		integrationDatabaseURL(t),
	)
	if output, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("schema verification failed: %v\n%s", err, output)
	}
	if _, err := pool.Exec(
		t.Context(),
		"UPDATE forja.schema_migrations SET checksum='tampered-backup'",
	); err != nil {
		t.Fatalf("tamper migration ledger: %v", err)
	}
	verify = exec.CommandContext(
		t.Context(),
		"../../scripts/postgres_verify.sh",
		integrationDatabaseURL(t),
	)
	if output, err := verify.CombinedOutput(); err == nil {
		t.Fatalf("verification accepted a tampered ledger\n%s", output)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if _, err := pool.Exec(
		t.Context(),
		"UPDATE forja.schema_migrations SET checksum=$1",
		migrations[0].checksum,
	); err != nil {
		t.Fatalf("restore migration checksum: %v", err)
	}
	backupPath := filepath.Join(t.TempDir(), "forja.dump")
	backup := exec.CommandContext(
		t.Context(),
		"../../scripts/postgres_backup.sh",
		integrationDatabaseURL(t),
		backupPath,
	)
	if output, err := backup.CombinedOutput(); err != nil {
		t.Fatalf("backup failed: %v\n%s", err, output)
	}
	refuseOverwrite := exec.CommandContext(
		t.Context(),
		"../../scripts/postgres_backup.sh",
		integrationDatabaseURL(t),
		backupPath,
	)
	if output, err := refuseOverwrite.CombinedOutput(); err == nil {
		t.Fatalf("backup silently overwrote a recovery point\n%s", output)
	}
	refused := exec.CommandContext(
		t.Context(),
		"../../scripts/postgres_restore.sh",
		integrationDatabaseURL(t),
		backupPath,
	)
	if output, err := refused.CombinedOutput(); err == nil {
		t.Fatalf("restore unexpectedly replaced a non-empty target\n%s", output)
	}
	if _, err := store.GetRun(t.Context(), runID); err != nil {
		t.Fatalf("refused restore damaged existing state: %v", err)
	}
	if _, err := pool.Exec(t.Context(), "DROP SCHEMA forja CASCADE"); err != nil {
		t.Fatalf("destroy source before restore: %v", err)
	}
	restore := exec.CommandContext(
		t.Context(),
		"../../scripts/postgres_restore.sh",
		integrationDatabaseURL(t),
		backupPath,
	)
	if output, err := restore.CombinedOutput(); err != nil {
		t.Fatalf("restore failed: %v\n%s", err, output)
	}
	restored, err := store.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatalf("read restored run: %v", err)
	}
	if restored.RunID != runID.String() || restored.Objective != "Survive a full backup and restore" {
		t.Fatalf("restored run = %#v", restored)
	}
	var restoredAttempt persistence.Attempt
	if err := pool.QueryRow(t.Context(), `
		SELECT attempt_id, run_id, ordinal, status,
		       lease_resource_type, lease_resource_id,
		       worker_id, fencing_token, version, created_at
		FROM forja.attempts
		WHERE tenant_id=$1 AND run_id=$2 AND attempt_id=$3`,
		DefaultTenantID,
		runID.String(),
		attempt.AttemptID,
	).Scan(
		&restoredAttempt.AttemptID,
		&restoredAttempt.RunID,
		&restoredAttempt.Ordinal,
		&restoredAttempt.Status,
		&restoredAttempt.LeaseResourceType,
		&restoredAttempt.LeaseResourceID,
		&restoredAttempt.WorkerID,
		&restoredAttempt.FencingToken,
		&restoredAttempt.Version,
		&restoredAttempt.CreatedAt,
	); err != nil {
		t.Fatalf("read restored attempt: %v", err)
	}
	if restoredAttempt.AttemptID != attempt.AttemptID ||
		restoredAttempt.RunID != attempt.RunID ||
		restoredAttempt.Ordinal != attempt.Ordinal ||
		restoredAttempt.Status != attempt.Status ||
		restoredAttempt.LeaseResourceType != attempt.LeaseResourceType ||
		restoredAttempt.LeaseResourceID != attempt.LeaseResourceID ||
		restoredAttempt.WorkerID != attempt.WorkerID ||
		restoredAttempt.FencingToken != attempt.FencingToken ||
		restoredAttempt.Version != attempt.Version ||
		!restoredAttempt.CreatedAt.Equal(attempt.CreatedAt) {
		t.Fatalf("restored attempt = %#v, want %#v", restoredAttempt, attempt)
	}
	corrupt := restored
	corrupt.State = string(runstate.StateCompleted)
	corrupt.Version = 2
	corrupt.UpdatedAt = corrupt.UpdatedAt.Add(time.Second)
	payload, err := json.Marshal(corrupt)
	if err != nil {
		t.Fatalf("encode corrupt restored event: %v", err)
	}
	eventID, err := newPrefixedID("event")
	if err != nil {
		t.Fatalf("generate corrupt restored event ID: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.events (
			event_id, tenant_id, repository_id, aggregate_type, aggregate_id,
			aggregate_version, event_type, schema_version, occurred_at,
			actor_type, actor_id, correlation_id, idempotency_key, payload
		) VALUES (
			$1, $2, $3, 'run', $4, 2, 'run.transitioned', '1.0', $5,
			'system', 'restore-corruption', 'restore-corruption',
			'restore-corruption', $6
		)`,
		eventID,
		DefaultTenantID,
		DefaultRepositoryID,
		runID.String(),
		corrupt.UpdatedAt,
		payload,
	); err != nil {
		t.Fatalf("insert corrupt restored event: %v", err)
	}
	corruptBackupPath := filepath.Join(t.TempDir(), "forja-corrupt.dump")
	corruptBackup := exec.CommandContext(
		t.Context(),
		"../../scripts/postgres_backup.sh",
		integrationDatabaseURL(t),
		corruptBackupPath,
	)
	if output, err := corruptBackup.CombinedOutput(); err != nil {
		t.Fatalf("back up corrupt source: %v\n%s", err, output)
	}
	if _, err := pool.Exec(t.Context(), "DROP SCHEMA forja CASCADE"); err != nil {
		t.Fatalf("empty target before corrupt restore: %v", err)
	}
	corruptRestore := exec.CommandContext(
		t.Context(),
		"../../scripts/postgres_restore.sh",
		integrationDatabaseURL(t),
		corruptBackupPath,
	)
	if output, err := corruptRestore.CombinedOutput(); err == nil {
		t.Fatalf("restore accepted semantically corrupt archive\n%s", output)
	}
}

func integrationDatabaseURL(t *testing.T) string {
	t.Helper()
	value := os.Getenv("FORJA_TEST_DATABASE_URL")
	if value == "" {
		t.Skip("FORJA_TEST_DATABASE_URL is not set")
	}
	return value
}

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := Open(t.Context(), integrationDatabaseURL(t), 8)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func migratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatalf("migrate integration database: %v", err)
	}
	return pool
}

func resetDatabase(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), "DROP SCHEMA IF EXISTS forja CASCADE"); err != nil {
		t.Fatalf("reset integration database: %v", err)
	}
}

func newIntegrationStore(t *testing.T, pool *pgxpool.Pool) *Store {
	t.Helper()
	store, err := NewStore(pool, nil, DefaultTenantID, DefaultRepositoryID)
	if err != nil {
		t.Fatalf("create integration store: %v", err)
	}
	return store
}

func requirePostgresVerifyFailure(t *testing.T) {
	t.Helper()
	verify := exec.CommandContext(
		t.Context(),
		"../../scripts/postgres_verify.sh",
		integrationDatabaseURL(t),
	)
	if output, err := verify.CombinedOutput(); err == nil {
		t.Fatalf("PostgreSQL verification accepted contradictory state\n%s", output)
	}
}

func mustRunID(t *testing.T) identity.RunID {
	t.Helper()
	id, err := identity.NewRunID()
	if err != nil {
		t.Fatalf("generate run ID: %v", err)
	}
	return id
}

func testMetadata(key string) runstate.CommandMetadata {
	return runstate.CommandMetadata{
		IdempotencyKey: key,
		ActorType:      "system",
		ActorID:        "integration-suite",
		CorrelationID:  key,
	}
}
