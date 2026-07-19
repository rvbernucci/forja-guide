package postgres

import (
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/identity"
)

func TestProjectionDeliveriesBackfillAndAdvanceIndependently(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(pool, nil, DefaultTenantID, DefaultRepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	firstRun, err := identity.ParseRunID("run_10000000-0000-4000-8000-000000000091")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(t.Context(), firstRun, "projection delivery backlog", testMetadata("projection-delivery-first")); err != nil {
		t.Fatal(err)
	}
	configuration := sha256.Sum256([]byte("qdrant-retrieval-v1"))
	if err := store.EnsureProjectionConsumer(t.Context(), "qdrant.retrieval", configuration); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimProjectionDeliveries(t.Context(), "qdrant.retrieval", "worker-a", 10, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].ProjectorName != "qdrant.retrieval" {
		t.Fatalf("backfill claimed=%#v err=%v", claimed, err)
	}
	if err := store.CompleteProjectionDelivery(t.Context(), "qdrant.retrieval", claimed[0].OutboxID, "worker-a", claimed[0].FencingToken); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteProjectionDelivery(t.Context(), "qdrant.retrieval", claimed[0].OutboxID, "worker-a", claimed[0].FencingToken); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("stale completion error=%v", err)
	}
	var checkpoint int64
	if err := pool.QueryRow(t.Context(), `
		SELECT last_outbox_id FROM forja.projection_checkpoints
		WHERE tenant_id=$1 AND repository_id=$2 AND projector_name='qdrant.retrieval'`,
		DefaultTenantID, DefaultRepositoryID).Scan(&checkpoint); err != nil || checkpoint != claimed[0].OutboxID {
		t.Fatalf("checkpoint=%d err=%v want=%d", checkpoint, err, claimed[0].OutboxID)
	}

	secondRun, err := identity.ParseRunID("run_10000000-0000-4000-8000-000000000092")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(t.Context(), secondRun, "projection delivery fanout", testMetadata("projection-delivery-second")); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimProjectionDeliveries(t.Context(), "qdrant.retrieval", "worker-b", 10, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].AggregateID != secondRun.String() {
		t.Fatalf("trigger fanout claimed=%#v err=%v", claimed, err)
	}

	if err := store.EnsureProjectionConsumer(t.Context(), "neo4j.lineage", configuration); err != nil {
		t.Fatal(err)
	}
	neo4j, err := store.ClaimProjectionDeliveries(t.Context(), "neo4j.lineage", "worker-c", 10, time.Minute)
	if err != nil || len(neo4j) != 2 {
		t.Fatalf("independent consumer backlog=%#v err=%v", neo4j, err)
	}
	wrongConfiguration := sha256.Sum256([]byte(strings.Repeat("different", 3)))
	if err := store.EnsureProjectionConsumer(t.Context(), "qdrant.retrieval", wrongConfiguration); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("configuration drift error=%v", err)
	}
}
