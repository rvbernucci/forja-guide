package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/persistence"
)

const (
	publicationTestDelivery = "delivery_55555555-5555-4555-8555-555555555555"
	publicationTestAttempt  = "attempt_55555555-5555-4555-8555-555555555555"
)

func TestDeliveryPublicationLifecycleAndExactReplay(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	leaseSet := acquirePublicationLeaseSet(
		t, store, publicationTestDelivery, publicationTestAttempt,
	)
	intent := publicationIntentFixture(t, leaseSet, publicationTestDelivery, publicationTestAttempt, "authority-a")

	prepared, err := store.PrepareDeliveryPublication(t.Context(), intent, leaseSet)
	if err != nil {
		t.Fatalf("prepare publication: %v", err)
	}
	if prepared.State != "prepared" || prepared.Intent.IntentSHA256 != intent.IntentSHA256 {
		t.Fatalf("prepared publication = %#v", prepared)
	}
	replayed, err := store.PrepareDeliveryPublication(t.Context(), intent, leaseSet)
	if err != nil || replayed.PreparedAt != prepared.PreparedAt {
		t.Fatalf("prepare replay = %#v, err=%v", replayed, err)
	}

	changed := publicationIntentFixture(
		t, leaseSet, publicationTestDelivery, publicationTestAttempt, "authority-b",
	)
	if _, err := store.PrepareDeliveryPublication(
		t.Context(), changed, leaseSet,
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("changed intent error = %v, want conflict", err)
	}

	applyCalls := 0
	published, err := store.CompleteDeliveryPublication(
		t.Context(), intent, leaseSet,
		func(context.Context) error {
			applyCalls++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("complete publication: %v", err)
	}
	if published.State != "published" || published.PublishedAt == nil ||
		published.ObservedCommit == nil || *published.ObservedCommit != intent.ResultCommit {
		t.Fatalf("published record = %#v", published)
	}
	completedReplay, err := store.CompleteDeliveryPublication(
		t.Context(), intent, leaseSet,
		func(context.Context) error {
			t.Fatal("published replay called Git apply")
			return nil
		},
	)
	if err != nil || completedReplay.PublishedAt == nil ||
		!completedReplay.PublishedAt.Equal(*published.PublishedAt) {
		t.Fatalf("complete replay = %#v, err=%v", completedReplay, err)
	}
	if err := store.ReleaseLeaseSet(t.Context(), leaseSet); err != nil {
		t.Fatalf("release published lease set: %v", err)
	}
	preparedAfterRelease, err := store.PrepareDeliveryPublication(
		t.Context(), intent, leaseSet,
	)
	if err != nil || preparedAfterRelease.State != "published" {
		t.Fatalf("prepare replay after release = %#v, err=%v", preparedAfterRelease, err)
	}
	completedAfterRelease, err := store.CompleteDeliveryPublication(
		t.Context(), intent, leaseSet,
		func(context.Context) error {
			t.Fatal("released published replay called Git apply")
			return nil
		},
	)
	if err != nil || completedAfterRelease.PublishedAt == nil ||
		!completedAfterRelease.PublishedAt.Equal(*published.PublishedAt) {
		t.Fatalf("complete replay after release = %#v, err=%v", completedAfterRelease, err)
	}
	if applyCalls != 1 {
		t.Fatalf("Git apply calls = %d, want 1", applyCalls)
	}
	loaded, found, err := store.GetDeliveryPublication(
		t.Context(), publicationTestDelivery, publicationTestAttempt,
	)
	if err != nil || !found || loaded.State != "published" {
		t.Fatalf("loaded publication = %#v, found=%v, err=%v", loaded, found, err)
	}
}

func TestDeliveryPublicationDoesNotApplyWithStaleFence(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	deliveryID := "delivery_66666666-6666-4666-8666-666666666666"
	attemptID := "attempt_66666666-6666-4666-8666-666666666666"
	leaseSet := acquirePublicationLeaseSet(t, store, deliveryID, attemptID)
	intent := publicationIntentFixture(t, leaseSet, deliveryID, attemptID, "authority")
	if _, err := store.PrepareDeliveryPublication(t.Context(), intent, leaseSet); err != nil {
		t.Fatalf("prepare stale completion: %v", err)
	}
	stale := leaseSet.Leases[0]
	if _, err := pool.Exec(t.Context(), `
		UPDATE forja.leases SET fencing_token=fencing_token+1
		WHERE tenant_id=$1 AND repository_id=$2
		  AND resource_type=$3 AND resource_id=$4`,
		DefaultTenantID, DefaultRepositoryID, stale.ResourceType, stale.ResourceID,
	); err != nil {
		t.Fatalf("replace completion fence: %v", err)
	}
	called := false
	if _, err := store.CompleteDeliveryPublication(
		t.Context(), intent, leaseSet,
		func(context.Context) error {
			called = true
			return nil
		},
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("stale completion error = %v, want conflict", err)
	}
	if called {
		t.Fatal("stale completion invoked Git apply")
	}
}

func TestDeliveryPublicationDoesNotApplyWithShortAuthorityHorizon(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	deliveryID := "delivery_77777777-7777-4777-8777-777777777777"
	attemptID := "attempt_77777777-7777-4777-8777-777777777777"
	leaseSet, err := store.AcquireLeaseSet(
		t.Context(), attemptID,
		[]persistence.LeaseKey{
			deliveryLeaseKey("worktree", deliveryID),
			deliveryLeaseKey("file", "internal/generated"),
			deliveryLeaseKey("artifact", "evidence"),
		},
		"publication-worker", 20*time.Second,
	)
	if err != nil {
		t.Fatalf("acquire short publication lease set: %v", err)
	}
	intent := publicationIntentFixture(t, leaseSet, deliveryID, attemptID, "authority")
	if _, err := store.PrepareDeliveryPublication(t.Context(), intent, leaseSet); err != nil {
		t.Fatalf("prepare short-horizon completion: %v", err)
	}
	called := false
	if _, err := store.CompleteDeliveryPublication(
		t.Context(), intent, leaseSet,
		func(context.Context) error {
			called = true
			return nil
		},
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("short-horizon completion error = %v, want conflict", err)
	}
	if called {
		t.Fatal("short-horizon completion invoked Git apply")
	}
}

func TestDeliveryPublicationRejectsStaleFence(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	leaseSet := acquirePublicationLeaseSet(
		t, store, publicationTestDelivery, publicationTestAttempt,
	)
	intent := publicationIntentFixture(t, leaseSet, publicationTestDelivery, publicationTestAttempt, "authority")
	stale := leaseSet.Leases[0]
	if _, err := pool.Exec(t.Context(), `
		UPDATE forja.leases SET fencing_token=fencing_token+1
		WHERE tenant_id=$1 AND repository_id=$2
		  AND resource_type=$3 AND resource_id=$4`,
		DefaultTenantID, DefaultRepositoryID, stale.ResourceType, stale.ResourceID,
	); err != nil {
		t.Fatalf("replace publication fence: %v", err)
	}
	if _, err := store.PrepareDeliveryPublication(
		t.Context(), intent, leaseSet,
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("stale publication fence error = %v, want conflict", err)
	}
	if _, found, err := store.GetDeliveryPublication(
		t.Context(), publicationTestDelivery, publicationTestAttempt,
	); err != nil || found {
		t.Fatalf("stale authority created journal row: found=%v err=%v", found, err)
	}
}

func TestDeliveryPublicationRecoveryAfterLeaseRelease(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	leaseSet := acquirePublicationLeaseSet(
		t, store, publicationTestDelivery, publicationTestAttempt,
	)
	intent := publicationIntentFixture(t, leaseSet, publicationTestDelivery, publicationTestAttempt, "authority")
	if _, err := store.PrepareDeliveryPublication(t.Context(), intent, leaseSet); err != nil {
		t.Fatalf("prepare recovery intent: %v", err)
	}
	if err := store.ReleaseLeaseSet(t.Context(), leaseSet); err != nil {
		t.Fatalf("expire recovery lease: %v", err)
	}
	recovered, err := store.RecoverDeliveryPublication(
		t.Context(), intent, intent.ResultCommit,
	)
	if err != nil {
		t.Fatalf("recover exact published ref: %v", err)
	}
	if recovered.State != "published" || recovered.PublishedAt == nil ||
		recovered.ObservedCommit == nil || *recovered.ObservedCommit != intent.ResultCommit {
		t.Fatalf("recovered publication = %#v", recovered)
	}
}

func TestDeliveryPublicationConflictIsTerminal(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	leaseSet := acquirePublicationLeaseSet(
		t, store, publicationTestDelivery, publicationTestAttempt,
	)
	intent := publicationIntentFixture(t, leaseSet, publicationTestDelivery, publicationTestAttempt, "authority")
	if _, err := store.PrepareDeliveryPublication(t.Context(), intent, leaseSet); err != nil {
		t.Fatalf("prepare conflicting publication: %v", err)
	}
	observed := strings.Repeat("c", 40)
	conflicted, err := store.ConflictDeliveryPublication(t.Context(), intent, &observed)
	if err != nil {
		t.Fatalf("record publication conflict: %v", err)
	}
	if conflicted.State != "conflict" || conflicted.ObservedCommit == nil ||
		*conflicted.ObservedCommit != observed {
		t.Fatalf("conflicted publication = %#v", conflicted)
	}
	if _, err := store.RecoverDeliveryPublication(
		t.Context(), intent, intent.ResultCommit,
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("terminal conflict recovery error = %v, want conflict", err)
	}
}

func TestMigrationFiveRollbackRefusesPublicationHistory(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	leaseSet := acquirePublicationLeaseSet(
		t, store, publicationTestDelivery, publicationTestAttempt,
	)
	intent := publicationIntentFixture(t, leaseSet, publicationTestDelivery, publicationTestAttempt, "authority")
	if _, err := store.PrepareDeliveryPublication(t.Context(), intent, leaseSet); err != nil {
		t.Fatalf("prepare rollback guard publication: %v", err)
	}
	if err := RollbackLast(t.Context(), pool); err == nil {
		t.Fatal("migration 005 rollback accepted a non-empty publication journal")
	}
	if _, err := pool.Exec(t.Context(), "DELETE FROM forja.delivery_publications"); err != nil {
		t.Fatalf("clear disposable publication journal: %v", err)
	}
	if err := RollbackLast(t.Context(), pool); err != nil {
		t.Fatalf("rollback empty migration 005: %v", err)
	}
}

func acquirePublicationLeaseSet(
	t *testing.T,
	store *Store,
	deliveryID string,
	attemptID string,
) persistence.LeaseSet {
	t.Helper()
	leaseSet, err := store.AcquireLeaseSet(
		t.Context(), attemptID,
		[]persistence.LeaseKey{
			deliveryLeaseKey("worktree", deliveryID),
			deliveryLeaseKey("file", "internal/generated"),
			deliveryLeaseKey("artifact", "evidence"),
		},
		"publication-worker", time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire publication lease set: %v", err)
	}
	return leaseSet
}

func publicationIntentFixture(
	t *testing.T,
	leaseSet persistence.LeaseSet,
	deliveryID string,
	attemptID string,
	authority string,
) persistence.DeliveryPublicationIntent {
	t.Helper()
	resultCommit := strings.Repeat("a", 40)
	previousCommit := strings.Repeat("b", 40)
	fences := make([]contracts.DeliveryLeaseFence, 0, len(leaseSet.Leases))
	for _, lease := range leaseSet.Leases {
		fences = append(fences, contracts.DeliveryLeaseFence{
			ResourceType: lease.ResourceType,
			ResourceID:   lease.ResourceID,
			OwnerID:      lease.OwnerID,
			FencingToken: lease.FencingToken,
		})
	}
	receipt := contracts.DeliveryReceipt{
		DeliveryID: deliveryID, SchemaVersion: "1.0", Status: "published",
		BaseCommit: strings.Repeat("d", 40), ResultCommit: resultCommit,
		ResultTree: strings.Repeat("e", 40), PatchSHA256: strings.Repeat("f", 64),
		ChangedPaths:              []string{"internal/generated/value.go"},
		PublicationRef:            "refs/forja/deliveries/" + deliveryID,
		PublicationPreviousCommit: &previousCommit,
		AuthorID:                  "author", ValidatorID: "validator", LeaseFences: fences,
		ValidationReportRef: "evidence/validation-report.json#sha256=" + strings.Repeat("1", 64),
		EvidenceManifestRef: "evidence/manifest.json#sha256=" + strings.Repeat("2", 64),
		CreatedAt:           time.Unix(1_700_000_000, 0).UTC(),
		PublishedAt:         time.Unix(1_700_000_000, 0).UTC(),
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("marshal publication receipt fixture: %v", err)
	}
	authorityDigest := sha256.Sum256([]byte(authority))
	receiptDigest := sha256.Sum256(receiptJSON)
	intent := persistence.DeliveryPublicationIntent{
		DeliveryID: deliveryID, AttemptID: attemptID, LeaseSetID: leaseSet.LeaseSetID,
		PublicationRef: receipt.PublicationRef, PublicationPreviousCommit: &previousCommit,
		ResultCommit: resultCommit, AuthoritySHA256: fmt.Sprintf("%x", authorityDigest),
		ReceiptSHA256: fmt.Sprintf("%x", receiptDigest), ReceiptJSON: receiptJSON,
	}
	identityJSON, err := json.Marshal(struct {
		DeliveryID      string `json:"delivery_id"`
		AttemptID       string `json:"attempt_id"`
		LeaseSetID      string `json:"lease_set_id"`
		AuthoritySHA256 string `json:"authority_sha256"`
		ReceiptSHA256   string `json:"receipt_sha256"`
	}{
		intent.DeliveryID, intent.AttemptID, intent.LeaseSetID,
		intent.AuthoritySHA256, intent.ReceiptSHA256,
	})
	if err != nil {
		t.Fatalf("marshal publication identity fixture: %v", err)
	}
	identityDigest := sha256.Sum256(identityJSON)
	intent.IntentSHA256 = fmt.Sprintf("%x", identityDigest)
	return intent
}
