package delivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/postgres"
)

func TestPublicationPostgresEndToEnd(t *testing.T) {
	databaseURL := os.Getenv("FORJA_TEST_DELIVERY_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("FORJA_TEST_DELIVERY_DATABASE_URL is not set")
	}
	pool, err := postgres.Open(t.Context(), databaseURL, 8)
	if err != nil {
		t.Fatalf("open delivery integration database: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(t.Context(), "DROP SCHEMA IF EXISTS forja CASCADE"); err != nil {
		t.Fatalf("reset delivery integration database: %v", err)
	}
	if err := postgres.Migrate(t.Context(), pool); err != nil {
		t.Fatalf("migrate delivery integration database: %v", err)
	}
	store, err := postgres.NewStore(
		pool, nil, postgres.DefaultTenantID, postgres.DefaultRepositoryID,
	)
	if err != nil {
		t.Fatal(err)
	}

	fixture := newPublicationFixture(t)
	keys := make([]persistence.LeaseKey, 0)
	for _, encoded := range contracts.ExpectedDeliveryFenceKeys(fixture.request) {
		parts := strings.SplitN(encoded, "\x00", 2)
		if len(parts) != 2 {
			t.Fatalf("invalid expected fence key %q", encoded)
		}
		keys = append(keys, persistence.LeaseKey{
			TenantID: postgres.DefaultTenantID, RepositoryID: postgres.DefaultRepositoryID,
			ResourceType: parts[0], ResourceID: parts[1],
		})
	}
	leaseSet, err := store.AcquireLeaseSet(
		t.Context(), fixture.request.AttemptID, keys, "delivery-service", time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire end-to-end lease set: %v", err)
	}
	seedPublicationRun(t, pool, store, fixture.request)
	service, err := NewPublicationService(fixture.manager, store, store, fixture.authority())
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, leaseSet,
	)
	if err != nil {
		t.Fatalf("publish end-to-end delivery: %v", err)
	}
	if !outcome.LeaseReleased || outcome.Replayed {
		t.Fatalf("end-to-end publication outcome = %#v", outcome)
	}
	record, found, err := store.GetDeliveryPublication(
		t.Context(), fixture.request.DeliveryID, fixture.request.AttemptID,
	)
	if err != nil || !found || record.State != "published" || record.PublishedAt == nil {
		t.Fatalf("durable publication record = %#v, found=%v, err=%v", record, found, err)
	}
	var leaseState string
	if err := pool.QueryRow(t.Context(), `
		SELECT state FROM forja.lease_sets
		WHERE tenant_id=$1 AND repository_id=$2 AND lease_set_id=$3`,
		postgres.DefaultTenantID, postgres.DefaultRepositoryID, leaseSet.LeaseSetID,
	).Scan(&leaseState); err != nil {
		t.Fatalf("read released lease set: %v", err)
	}
	if leaseState != "released" {
		t.Fatalf("published lease state = %q, want released", leaseState)
	}
	replayed, err := service.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, leaseSet,
	)
	if err != nil {
		t.Fatalf("replay end-to-end publication: %v", err)
	}
	if !replayed.Replayed || !replayed.LeaseReleased || !reflect.DeepEqual(replayed.Receipt, outcome.Receipt) {
		t.Fatalf("end-to-end publication replay = %#v", replayed)
	}
	if err := postgres.VerifySchema(
		t.Context(), pool, postgres.DefaultTenantID, postgres.DefaultRepositoryID,
	); err != nil {
		t.Fatalf("verify end-to-end schema: %v", err)
	}
}

func TestPublicationPostgresRecoversCrashAfterGitCAS(t *testing.T) {
	databaseURL := os.Getenv("FORJA_TEST_DELIVERY_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("FORJA_TEST_DELIVERY_DATABASE_URL is not set")
	}
	pool, err := postgres.Open(t.Context(), databaseURL, 8)
	if err != nil {
		t.Fatalf("open delivery integration database: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(t.Context(), "DROP SCHEMA IF EXISTS forja CASCADE"); err != nil {
		t.Fatalf("reset delivery integration database: %v", err)
	}
	if err := postgres.Migrate(t.Context(), pool); err != nil {
		t.Fatalf("migrate delivery integration database: %v", err)
	}
	store, err := postgres.NewStore(
		pool, nil, postgres.DefaultTenantID, postgres.DefaultRepositoryID,
	)
	if err != nil {
		t.Fatal(err)
	}

	fixture := newPublicationFixture(t)
	keys := make([]persistence.LeaseKey, 0)
	for _, encoded := range contracts.ExpectedDeliveryFenceKeys(fixture.request) {
		parts := strings.SplitN(encoded, "\x00", 2)
		if len(parts) != 2 {
			t.Fatalf("invalid expected fence key %q", encoded)
		}
		keys = append(keys, persistence.LeaseKey{
			TenantID: postgres.DefaultTenantID, RepositoryID: postgres.DefaultRepositoryID,
			ResourceType: parts[0], ResourceID: parts[1],
		})
	}
	leaseSet, err := store.AcquireLeaseSet(
		t.Context(), fixture.request.AttemptID, keys, "delivery-service", time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire crash-recovery lease set: %v", err)
	}
	seedPublicationRun(t, pool, store, fixture.request)
	if _, err := pool.Exec(t.Context(), `
		CREATE OR REPLACE FUNCTION forja.fail_publication_commit()
		RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.state = 'published' AND OLD.state = 'prepared' THEN
				RAISE EXCEPTION 'simulated failure after Git CAS before publication commit';
			END IF;
			RETURN NEW;
		END
		$$`); err != nil {
		t.Fatalf("create publication failure function: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		CREATE TRIGGER fail_publication_commit
		BEFORE UPDATE ON forja.delivery_publications
		FOR EACH ROW EXECUTE FUNCTION forja.fail_publication_commit()`); err != nil {
		t.Fatalf("create publication failure trigger: %v", err)
	}
	removeFailureTrigger := func(ctx context.Context) error {
		var removalErrors []error
		if _, err := pool.Exec(ctx, `
			DROP TRIGGER IF EXISTS fail_publication_commit ON forja.delivery_publications`); err != nil {
			removalErrors = append(removalErrors, fmt.Errorf("drop publication failure trigger: %w", err))
		}
		if _, err := pool.Exec(ctx, `
			DROP FUNCTION IF EXISTS forja.fail_publication_commit()`); err != nil {
			removalErrors = append(removalErrors, fmt.Errorf("drop publication failure function: %w", err))
		}
		return errors.Join(removalErrors...)
	}
	t.Cleanup(func() {
		if err := removeFailureTrigger(context.Background()); err != nil {
			t.Errorf("clean publication failure injection: %v", err)
		}
	})

	service, err := NewPublicationService(
		fixture.manager, store, store, fixture.authority(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, leaseSet,
	); err == nil {
		t.Fatalf("crash-window publication error = %v", err)
	}
	if ref := strings.TrimSpace(runGitTest(
		t, fixture.repository, "rev-parse", fixture.request.PublicationRef,
	)); ref != fixture.result.ResultCommit {
		t.Fatalf("Git CAS before crash = %s", ref)
	}
	prepared, found, err := store.GetDeliveryPublication(
		t.Context(), fixture.request.DeliveryID, fixture.request.AttemptID,
	)
	if err != nil || !found || prepared.State != "prepared" || prepared.PublishedAt != nil {
		t.Fatalf("journal before restart = %#v, found=%v, err=%v", prepared, found, err)
	}
	if err := removeFailureTrigger(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Fresh service and store instances model a process restart over the same
	// durable PostgreSQL journal and Git repository.
	restartedStore, err := postgres.NewStore(
		pool, nil, postgres.DefaultTenantID, postgres.DefaultRepositoryID,
	)
	if err != nil {
		t.Fatal(err)
	}
	restartedService, err := NewPublicationService(
		fixture.manager, restartedStore, restartedStore, fixture.authority(),
	)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restartedService.Recover(
		t.Context(), fixture.request, fixture.result, fixture.bundle, leaseSet,
	)
	if err != nil {
		t.Fatalf("recover crash-window publication: %v", err)
	}
	if !recovered.Replayed || !recovered.LeaseReleased {
		t.Fatalf("recovered publication outcome = %#v", recovered)
	}
	published, found, err := restartedStore.GetDeliveryPublication(
		t.Context(), fixture.request.DeliveryID, fixture.request.AttemptID,
	)
	if err != nil || !found || published.State != "published" || published.PublishedAt == nil ||
		published.ObservedCommit == nil || *published.ObservedCommit != fixture.result.ResultCommit ||
		!reflect.DeepEqual(published.Intent.ReceiptJSON, prepared.Intent.ReceiptJSON) {
		t.Fatalf("journal after restart recovery = %#v, found=%v, err=%v", published, found, err)
	}
	var leaseState string
	if err := pool.QueryRow(t.Context(), `
		SELECT state FROM forja.lease_sets
		WHERE tenant_id=$1 AND repository_id=$2 AND lease_set_id=$3`,
		postgres.DefaultTenantID, postgres.DefaultRepositoryID, leaseSet.LeaseSetID,
	).Scan(&leaseState); err != nil {
		t.Fatalf("read recovered lease set: %v", err)
	}
	if leaseState != "released" {
		t.Fatalf("recovered lease state = %q, want released", leaseState)
	}
	replayed, err := restartedService.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, leaseSet,
	)
	if err != nil || !replayed.Replayed || !replayed.LeaseReleased ||
		!reflect.DeepEqual(replayed.Receipt, recovered.Receipt) {
		t.Fatalf("recovered publication replay = %#v, err=%v", replayed, err)
	}
}

func seedPublicationRun(
	t *testing.T,
	pool *pgxpool.Pool,
	store *postgres.Store,
	request contracts.DeliveryRequest,
) {
	t.Helper()
	schedulerResource := "publication-scheduler:" + request.AttemptID
	schedulerLease, err := store.AcquireLease(
		t.Context(),
		persistence.LeaseKey{
			TenantID: postgres.DefaultTenantID, RepositoryID: postgres.DefaultRepositoryID,
			ResourceType: "scheduler", ResourceID: schedulerResource,
		},
		"publication-scheduler", time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire publication scheduler lease: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.runs (
			run_id, tenant_id, repository_id, objective, state, version,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, 'validating', 1,
		          clock_timestamp(), clock_timestamp())`,
		request.RunID, postgres.DefaultTenantID, postgres.DefaultRepositoryID,
		request.Objective,
	); err != nil {
		t.Fatalf("seed publication Run: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.attempts (
			attempt_id, tenant_id, run_id, ordinal, status,
			lease_resource_type, lease_resource_id, worker_id, fencing_token,
			started_at, finished_at, version
		) VALUES ($1, $2, $3, $4, 'succeeded',
		          'scheduler', $5, $6, $7,
		          clock_timestamp(), clock_timestamp(), 3)`,
		request.AttemptID, postgres.DefaultTenantID, request.RunID,
		request.AttemptOrdinal, schedulerResource,
		schedulerLease.OwnerID, schedulerLease.FencingToken,
	); err != nil {
		t.Fatalf("seed publication attempt: %v", err)
	}
}
