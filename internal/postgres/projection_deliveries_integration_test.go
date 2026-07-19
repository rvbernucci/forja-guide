package postgres

import (
	"crypto/sha256"
	"errors"
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
	if _, err := store.RetrievalProjectionLag(t.Context()); !fault.IsCode(err, fault.CodeUnavailable) {
		t.Fatalf("inactive retrieval consumer lag error=%v", err)
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
	if lag, err := store.RetrievalProjectionLag(t.Context()); err != nil || lag != 0 {
		t.Fatalf("published retrieval lag=%d err=%v", lag, err)
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
	if lag, err := store.RetrievalProjectionLag(t.Context()); err != nil || lag != 1 {
		t.Fatalf("pending retrieval lag=%d err=%v", lag, err)
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

func TestProjectionDeadLetterCanOnlyBeExplicitlyRequeued(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(pool, nil, DefaultTenantID, DefaultRepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := identity.ParseRunID("run_10000000-0000-4000-8000-000000000093")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(t.Context(), run, "dead letter replay", testMetadata("projection-delivery-dead")); err != nil {
		t.Fatal(err)
	}
	configuration := sha256.Sum256([]byte("qdrant-retrieval-dead-letter"))
	if err := store.EnsureProjectionConsumer(t.Context(), "qdrant.retrieval", configuration); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimProjectionDeliveries(t.Context(), "qdrant.retrieval", "worker-dead", 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%#v err=%v", claimed, err)
	}
	if err := store.FailProjectionDelivery(t.Context(), "qdrant.retrieval", claimed[0].OutboxID, "worker-dead", claimed[0].FencingToken, errors.New("dependency unavailable"), time.Now().UTC(), 1); err != nil {
		t.Fatal(err)
	}
	if err := store.RequeueProjectionDelivery(t.Context(), "qdrant.retrieval", claimed[0].OutboxID); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := store.ClaimProjectionDeliveries(t.Context(), "qdrant.retrieval", "worker-repair", 1, time.Minute)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].OutboxID != claimed[0].OutboxID || reclaimed[0].Attempts != 1 {
		t.Fatalf("reclaimed=%#v err=%v", reclaimed, err)
	}
	if err := store.RequeueProjectionDelivery(t.Context(), "qdrant.retrieval", claimed[0].OutboxID); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("non-dead requeue error=%v", err)
	}
	var deadLetters int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM forja.projection_dead_letters WHERE projector_name='qdrant.retrieval'`).Scan(&deadLetters); err != nil || deadLetters != 1 {
		t.Fatalf("dead letters=%d err=%v", deadLetters, err)
	}
}
