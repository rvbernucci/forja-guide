package delivery

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

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
	service, err := NewPublicationService(fixture.manager, store, store)
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
