package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

const (
	publicationTestDelivery = "delivery_55555555-5555-4555-8555-555555555555"
	publicationTestAttempt  = "attempt_55555555-5555-4555-8555-555555555555"
)

func TestPublicationIntentRejectsMalformedLifecycleIDsBeforePersistence(t *testing.T) {
	leaseSet := persistence.LeaseSet{LeaseSetID: publicationTestAttempt}
	base := publicationIntentFixture(
		t, leaseSet, publicationTestDelivery, publicationTestAttempt, "authority",
	)
	for name, mutate := range map[string]func(*persistence.DeliveryPublicationIntent){
		"delivery": func(intent *persistence.DeliveryPublicationIntent) {
			intent.DeliveryID = "delivery-not-a-uuid"
		},
		"attempt": func(intent *persistence.DeliveryPublicationIntent) {
			intent.AttemptID = "attempt-not-a-uuid"
		},
	} {
		t.Run(name, func(t *testing.T) {
			intent := base
			mutate(&intent)
			if err := validatePublicationIntent(intent); err == nil ||
				!strings.Contains(err.Error(), "invalid "+name+" ID") {
				t.Fatalf("malformed %s ID validation error = %v", name, err)
			}
		})
	}
}

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
		func(context.Context) error { return nil },
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
	var receipt contracts.DeliveryReceipt
	if err := json.Unmarshal(intent.ReceiptJSON, &receipt); err != nil {
		t.Fatal(err)
	}
	if !published.PublishedAt.Equal(receipt.PublishedAt) ||
		published.UpdatedAt.Before(*published.PublishedAt) {
		t.Fatalf("publication timestamps = published %v updated %v receipt %v",
			published.PublishedAt, published.UpdatedAt, receipt.PublishedAt)
	}
	if published.PublishedAt.Format(time.RFC3339Nano) != receipt.PublishedAt.Format(time.RFC3339Nano) {
		t.Fatalf("publication timestamp representation = %q, want %q",
			published.PublishedAt.Format(time.RFC3339Nano), receipt.PublishedAt.Format(time.RFC3339Nano))
	}
	completedReplay, err := store.CompleteDeliveryPublication(
		t.Context(), intent, leaseSet,
		func(context.Context) error {
			t.Fatal("published replay called revalidation")
			return nil
		},
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
			t.Fatal("released published replay called revalidation")
			return nil
		},
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

func TestPreparedPublicationSerializesCancellation(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	deliveryID := "delivery_12121212-1212-4212-8212-121212121212"
	attemptID := "attempt_12121212-1212-4212-8212-121212121212"
	leaseSet := acquirePublicationLeaseSet(t, store, deliveryID, attemptID)
	intent := publicationIntentFixture(t, leaseSet, deliveryID, attemptID, "authority")
	if _, err := store.PrepareDeliveryPublication(t.Context(), intent, leaseSet); err != nil {
		t.Fatalf("prepare cancellation fence: %v", err)
	}
	runID, err := identity.ParseRunID(strings.Replace(attemptID, "attempt_", "run_", 1))
	if err != nil {
		t.Fatal(err)
	}
	metadata := runstate.CommandMetadata{
		IdempotencyKey: "cancel-prepared-publication",
		ActorType:      "human", ActorID: "operator",
		CorrelationID: "publication-cancel-test",
	}
	if _, err := store.TransitionRun(
		t.Context(), runID, 1, runstate.StateCancelling, metadata,
	); !isFaultCode(err, fault.CodeConflict) ||
		!strings.Contains(err.Error(), "committed this Run to publication") {
		t.Fatalf("prepared publication cancellation error = %v", err)
	}
	metadata.IdempotencyKey = "fail-prepared-publication"
	if _, err := store.TransitionRun(
		t.Context(), runID, 1, runstate.StateFailedRetryable, metadata,
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("prepared publication failure transition error = %v", err)
	}
	if _, err := store.ConflictDeliveryPublication(t.Context(), intent, nil); err != nil {
		t.Fatalf("retire prepared publication: %v", err)
	}
	metadata.IdempotencyKey = "cancel-retired-publication"
	if _, err := store.TransitionRun(
		t.Context(), runID, 1, runstate.StateCancelling, metadata,
	); err != nil {
		t.Fatalf("cancel after publication retirement: %v", err)
	}
}

func TestPublishedPublicationAllowsOnlyRunCompletion(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	deliveryID := "delivery_14141414-1414-4414-8414-141414141414"
	attemptID := "attempt_14141414-1414-4414-8414-141414141414"
	leaseSet := acquirePublicationLeaseSet(t, store, deliveryID, attemptID)
	intent := publicationIntentFixture(t, leaseSet, deliveryID, attemptID, "authority")
	if _, err := store.PrepareDeliveryPublication(t.Context(), intent, leaseSet); err != nil {
		t.Fatalf("prepare completion fence: %v", err)
	}
	if _, err := store.CompleteDeliveryPublication(
		t.Context(), intent, leaseSet,
		func(context.Context) error { return nil },
		func(context.Context) error { return nil },
	); err != nil {
		t.Fatalf("publish completion fence: %v", err)
	}
	runID, err := identity.ParseRunID(strings.Replace(attemptID, "attempt_", "run_", 1))
	if err != nil {
		t.Fatal(err)
	}
	metadata := runstate.CommandMetadata{
		IdempotencyKey: "fail-published-publication",
		ActorType:      "system", ActorID: "publisher",
		CorrelationID: "publication-completion-test",
	}
	if _, err := store.TransitionRun(
		t.Context(), runID, 1, runstate.StateFailedTerminal, metadata,
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("published publication failure transition error = %v", err)
	}
	metadata.IdempotencyKey = "complete-published-publication"
	completed, err := store.TransitionRun(
		t.Context(), runID, 1, runstate.StateCompleted, metadata,
	)
	if err != nil || completed.State != string(runstate.StateCompleted) {
		t.Fatalf("published publication completion = %#v, err=%v", completed, err)
	}
}

func TestPublicationPreparationRequiresValidatingRun(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	deliveryID := "delivery_13131313-1313-4313-8313-131313131313"
	attemptID := "attempt_13131313-1313-4313-8313-131313131313"
	leaseSet := acquirePublicationLeaseSet(t, store, deliveryID, attemptID)
	runID := strings.Replace(attemptID, "attempt_", "run_", 1)
	if _, err := pool.Exec(t.Context(), `
		UPDATE forja.runs SET state='cancelling', version=version+1
		WHERE tenant_id=$1 AND repository_id=$2 AND run_id=$3`,
		DefaultTenantID, DefaultRepositoryID, runID,
	); err != nil {
		t.Fatal(err)
	}
	intent := publicationIntentFixture(t, leaseSet, deliveryID, attemptID, "authority")
	if _, err := store.PrepareDeliveryPublication(
		t.Context(), intent, leaseSet,
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("cancelling Run preparation error = %v, want conflict", err)
	}
	if _, found, err := store.GetDeliveryPublication(
		t.Context(), deliveryID, attemptID,
	); err != nil || found {
		t.Fatalf("cancelled preparation persisted: found=%v err=%v", found, err)
	}
}

func TestPreparedPublicationReplayRechecksLifecycleFence(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	deliveryID := "delivery_15151515-1515-4515-8515-151515151515"
	attemptID := "attempt_15151515-1515-4515-8515-151515151515"
	leaseSet := acquirePublicationLeaseSet(t, store, deliveryID, attemptID)
	intent := publicationIntentFixture(t, leaseSet, deliveryID, attemptID, "authority")
	if _, err := store.PrepareDeliveryPublication(t.Context(), intent, leaseSet); err != nil {
		t.Fatalf("prepare legacy replay fixture: %v", err)
	}
	runID := strings.Replace(attemptID, "attempt_", "run_", 1)
	if _, err := pool.Exec(t.Context(), `
		UPDATE forja.runs SET state='cancelling', version=version+1
		WHERE tenant_id=$1 AND repository_id=$2 AND run_id=$3`,
		DefaultTenantID, DefaultRepositoryID, runID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareDeliveryPublication(
		t.Context(), intent, leaseSet,
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("prepared replay lifecycle error = %v, want conflict", err)
	}
	called := false
	if _, err := store.CompleteDeliveryPublication(
		t.Context(), intent, leaseSet,
		func(context.Context) error { called = true; return nil },
		func(context.Context) error { called = true; return nil },
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("prepared completion lifecycle error = %v, want conflict", err)
	}
	if called {
		t.Fatal("invalid legacy prepared replay invoked publication callbacks")
	}
}

func TestPreparedPublicationReplayRequiresOriginalAttempt(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	deliveryID := "delivery_16161616-1616-4616-8616-161616161616"
	attemptID := "attempt_16161616-1616-4616-8616-161616161616"
	leaseSet := acquirePublicationLeaseSet(t, store, deliveryID, attemptID)
	intent := publicationIntentFixture(t, leaseSet, deliveryID, attemptID, "authority")
	if _, err := store.PrepareDeliveryPublication(t.Context(), intent, leaseSet); err != nil {
		t.Fatalf("prepare orphan replay fixture: %v", err)
	}
	if _, err := pool.Exec(
		t.Context(), "DELETE FROM forja.attempts WHERE tenant_id=$1 AND attempt_id=$2",
		DefaultTenantID, attemptID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareDeliveryPublication(
		t.Context(), intent, leaseSet,
	); !isFaultCode(err, fault.CodeNotFound) {
		t.Fatalf("orphan prepared replay error = %v, want not found", err)
	}
	called := false
	if _, err := store.CompleteDeliveryPublication(
		t.Context(), intent, leaseSet,
		func(context.Context) error { called = true; return nil },
		func(context.Context) error { called = true; return nil },
	); !isFaultCode(err, fault.CodeNotFound) {
		t.Fatalf("orphan prepared completion error = %v, want not found", err)
	}
	if called {
		t.Fatal("orphan prepared replay invoked publication callbacks")
	}
}

func TestGovernedResumeRejectsLegacyPublicationJournal(t *testing.T) {
	for _, test := range []struct {
		name         string
		deliveryID   string
		attemptID    string
		journalState string
		runState     runstate.State
	}{
		{
			name:         "prepared from failed retryable",
			deliveryID:   "delivery_17171717-1717-4717-8717-171717171717",
			attemptID:    "attempt_17171717-1717-4717-8717-171717171717",
			journalState: "prepared", runState: runstate.StateFailedRetryable,
		},
		{
			name:         "published from awaiting decision",
			deliveryID:   "delivery_18181818-1818-4818-8818-181818181818",
			attemptID:    "attempt_18181818-1818-4818-8818-181818181818",
			journalState: "published", runState: runstate.StateAwaitingDecision,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := migratedPool(t)
			store := newIntegrationStore(t, pool)
			leaseSet := acquirePublicationLeaseSet(
				t, store, test.deliveryID, test.attemptID,
			)
			intent := publicationIntentFixture(
				t, leaseSet, test.deliveryID, test.attemptID, "authority",
			)
			if _, err := store.PrepareDeliveryPublication(t.Context(), intent, leaseSet); err != nil {
				t.Fatal(err)
			}
			if test.journalState == "published" {
				if _, err := store.CompleteDeliveryPublication(
					t.Context(), intent, leaseSet,
					func(context.Context) error { return nil },
					func(context.Context) error { return nil },
				); err != nil {
					t.Fatal(err)
				}
			}
			runIDText := strings.Replace(test.attemptID, "attempt_", "run_", 1)
			if _, err := pool.Exec(t.Context(), `
				UPDATE forja.runs SET state=$1, version=2
				WHERE tenant_id=$2 AND repository_id=$3 AND run_id=$4`,
				test.runState, DefaultTenantID, DefaultRepositoryID, runIDText,
			); err != nil {
				t.Fatal(err)
			}
			runID, err := identity.ParseRunID(runIDText)
			if err != nil {
				t.Fatal(err)
			}
			metadata := runstate.CommandMetadata{
				IdempotencyKey: "resume-legacy-publication-" + test.journalState,
				ActorType:      "human", ActorID: "operator",
				CorrelationID: "legacy-publication-resume",
			}
			if _, err := store.ResumeRun(
				t.Context(), runID, 2, metadata,
			); !isFaultCode(err, fault.CodeConflict) {
				t.Fatalf("legacy %s journal resume error = %v, want conflict", test.journalState, err)
			}
			stored, err := store.GetRun(t.Context(), runID)
			if err != nil || stored.State != string(test.runState) {
				t.Fatalf("legacy journal changed Run = %#v, err=%v", stored, err)
			}
		})
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
		"publication-worker", time.Duration(contracts.MinimumPublicationLeaseTTLMS)*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("acquire short publication lease set: %v", err)
	}
	seedPublicationAttempt(t, store, attemptID)
	intent := publicationIntentFixture(t, leaseSet, deliveryID, attemptID, "authority")
	if _, err := store.PrepareDeliveryPublication(t.Context(), intent, leaseSet); err != nil {
		t.Fatalf("prepare short-horizon completion: %v", err)
	}
	agePublicationLeaseSet(t, pool, leaseSet.LeaseSetID, 21*time.Second)
	called := false
	if _, err := store.CompleteDeliveryPublication(
		t.Context(), intent, leaseSet,
		func(context.Context) error {
			called = true
			return nil
		},
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

func TestDeliveryPublicationRechecksAuthorityAfterRevalidation(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	deliveryID := "delivery_88888888-8888-4888-8888-888888888888"
	attemptID := "attempt_88888888-8888-4888-8888-888888888888"
	leaseSet, err := store.AcquireLeaseSet(
		t.Context(), attemptID,
		[]persistence.LeaseKey{
			deliveryLeaseKey("worktree", deliveryID),
			deliveryLeaseKey("file", "internal/generated"),
			deliveryLeaseKey("artifact", "evidence"),
		},
		"publication-worker", time.Duration(contracts.MinimumPublicationLeaseTTLMS)*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("acquire publication lease set: %v", err)
	}
	seedPublicationAttempt(t, store, attemptID)
	intent := publicationIntentFixture(t, leaseSet, deliveryID, attemptID, "authority")
	if _, err := store.PrepareDeliveryPublication(t.Context(), intent, leaseSet); err != nil {
		t.Fatalf("prepare publication: %v", err)
	}
	agePublicationLeaseSet(t, pool, leaseSet.LeaseSetID, 5*time.Second)
	revalidated := false
	applied := false
	if _, err := store.CompleteDeliveryPublication(
		t.Context(), intent, leaseSet,
		func(context.Context) error {
			revalidated = true
			time.Sleep(16 * time.Second)
			return nil
		},
		func(context.Context) error {
			applied = true
			return nil
		},
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("post-revalidation authority error = %v, want conflict", err)
	}
	if !revalidated || applied {
		t.Fatalf("publication callbacks: revalidated=%v applied=%v", revalidated, applied)
	}
}

func TestDeliveryPublicationRejectsLeaseDurationOutsideIntentAuthority(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	deliveryID := "delivery_99999999-9999-4999-8999-999999999999"
	attemptID := "attempt_99999999-9999-4999-8999-999999999999"
	leaseSet, err := store.AcquireLeaseSet(
		t.Context(), attemptID,
		[]persistence.LeaseKey{
			deliveryLeaseKey("worktree", deliveryID),
			deliveryLeaseKey("file", "internal/generated"),
			deliveryLeaseKey("artifact", "evidence"),
		},
		"publication-worker", 61*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	seedPublicationAttempt(t, store, attemptID)
	intent := publicationIntentFixture(t, leaseSet, deliveryID, attemptID, "authority")
	if _, err := store.PrepareDeliveryPublication(
		t.Context(), intent, leaseSet,
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("mismatched lease duration error = %v, want conflict", err)
	}
	if _, found, err := store.GetDeliveryPublication(t.Context(), deliveryID, attemptID); err != nil || found {
		t.Fatalf("mismatched duration persisted intent: found=%v err=%v", found, err)
	}
}

func TestDeliveryPublicationRejectsMemberDurationOutsideIntentAuthority(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	deliveryID := "delivery_a1a1a1a1-a1a1-41a1-81a1-a1a1a1a1a1a1"
	attemptID := "attempt_a1a1a1a1-a1a1-41a1-81a1-a1a1a1a1a1a1"
	leaseSet := acquirePublicationLeaseSet(t, store, deliveryID, attemptID)
	intent := publicationIntentFixture(t, leaseSet, deliveryID, attemptID, "authority")
	tampered := leaseSet.Leases[0]
	if _, err := pool.Exec(t.Context(), `
		UPDATE forja.leases
		SET updated_at=updated_at - interval '1 second'
		WHERE tenant_id=$1 AND repository_id=$2
		  AND resource_type=$3 AND resource_id=$4`,
		DefaultTenantID, DefaultRepositoryID, tampered.ResourceType, tampered.ResourceID,
	); err != nil {
		t.Fatalf("tamper publication lease member duration: %v", err)
	}
	if _, err := store.PrepareDeliveryPublication(
		t.Context(), intent, leaseSet,
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("tampered member duration error = %v, want conflict", err)
	}
	if _, found, err := store.GetDeliveryPublication(t.Context(), deliveryID, attemptID); err != nil || found {
		t.Fatalf("tampered member duration persisted intent: found=%v err=%v", found, err)
	}
}

func TestDeliveryPublicationAbandonmentAllowsCleanRetry(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	deliveryID := "delivery_abababab-abab-4bab-8bab-abababababab"
	firstAttempt := "attempt_abababab-abab-4bab-8bab-abababababab"
	firstLease := acquirePublicationLeaseSet(t, store, deliveryID, firstAttempt)
	firstIntent := publicationIntentFixture(t, firstLease, deliveryID, firstAttempt, "authority-1")
	if _, err := store.PrepareDeliveryPublication(t.Context(), firstIntent, firstLease); err != nil {
		t.Fatal(err)
	}
	abandoned, err := store.AbandonDeliveryPublication(
		t.Context(), firstIntent,
		func(context.Context) (*string, error) {
			return firstIntent.PublicationPreviousCommit, nil
		},
	)
	if err != nil || abandoned.State != "abandoned" {
		t.Fatalf("abandoned publication = %#v err=%v", abandoned, err)
	}
	if err := store.ReleaseLeaseSet(t.Context(), firstLease); err != nil {
		t.Fatal(err)
	}

	secondAttempt := "attempt_cdcdcdcd-cdcd-4dcd-8dcd-cdcdcdcdcdcd"
	secondLease := acquirePublicationLeaseSet(t, store, deliveryID, secondAttempt)
	secondIntent := publicationIntentFixture(t, secondLease, deliveryID, secondAttempt, "authority-2")
	prepared, err := store.PrepareDeliveryPublication(t.Context(), secondIntent, secondLease)
	if err != nil || prepared.State != "prepared" {
		t.Fatalf("clean retry publication = %#v err=%v", prepared, err)
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
	var receipt contracts.DeliveryReceipt
	if err := json.Unmarshal(intent.ReceiptJSON, &receipt); err != nil {
		t.Fatal(err)
	}
	if !recovered.PublishedAt.Equal(receipt.PublishedAt) {
		t.Fatalf("recovery timestamp = %v, want %v", recovered.PublishedAt, receipt.PublishedAt)
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

func TestMigrationRollbackPreservesLeaseAndPublicationAuthority(t *testing.T) {
	pool := migratedPool(t)
	rollbackToMigrationVersion(t, pool, 6)
	store := newIntegrationStore(t, pool)
	leaseSet := acquirePublicationLeaseSet(
		t, store, publicationTestDelivery, publicationTestAttempt,
	)
	intent := publicationIntentFixture(t, leaseSet, publicationTestDelivery, publicationTestAttempt, "authority")
	if _, err := store.PrepareDeliveryPublication(t.Context(), intent, leaseSet); err != nil {
		t.Fatalf("prepare rollback guard publication: %v", err)
	}
	if err := RollbackLast(t.Context(), pool); err == nil {
		t.Fatal("migration 006 rollback accepted an active lease set")
	}
	if err := store.ReleaseLeaseSet(t.Context(), leaseSet); err != nil {
		t.Fatalf("release rollback-guard lease set: %v", err)
	}
	if err := RollbackLast(t.Context(), pool); err != nil {
		t.Fatalf("rollback migration 006 after release: %v", err)
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
	seedPublicationAttempt(t, store, attemptID)
	return leaseSet
}

func seedPublicationAttempt(t *testing.T, store *Store, attemptID string) string {
	t.Helper()
	runID := strings.Replace(attemptID, "attempt_", "run_", 1)
	schedulerResource := "publication-scheduler:" + attemptID
	schedulerLease, err := store.AcquireLease(
		t.Context(),
		persistence.LeaseKey{
			TenantID: DefaultTenantID, RepositoryID: DefaultRepositoryID,
			ResourceType: "scheduler", ResourceID: schedulerResource,
		},
		"publication-worker", time.Minute,
	)
	if err != nil {
		t.Fatalf("seed publication scheduler lease: %v", err)
	}
	if _, err := store.pool.Exec(t.Context(), `
		INSERT INTO forja.runs (
			run_id, tenant_id, repository_id, objective, state, version,
			created_at, updated_at
		) VALUES ($1, $2, $3, 'Publish the validated delivery.', 'validating', 1,
		          clock_timestamp(), clock_timestamp())
		ON CONFLICT (run_id) DO NOTHING`,
		runID, DefaultTenantID, DefaultRepositoryID,
	); err != nil {
		t.Fatalf("seed publication Run: %v", err)
	}
	if _, err := store.pool.Exec(t.Context(), `
		INSERT INTO forja.attempts (
			attempt_id, tenant_id, run_id, ordinal, status,
			lease_resource_type, lease_resource_id, worker_id,
			fencing_token, started_at, finished_at, version
		) VALUES ($1, $2, $3, 1, 'succeeded',
		          'scheduler', $4, $5,
		          $6, clock_timestamp(), clock_timestamp(), 3)
		ON CONFLICT (attempt_id) DO NOTHING`,
		attemptID, DefaultTenantID, runID, schedulerResource,
		schedulerLease.OwnerID, schedulerLease.FencingToken,
	); err != nil {
		t.Fatalf("seed publication attempt: %v", err)
	}
	return runID
}

func agePublicationLeaseSet(
	t *testing.T,
	pool *pgxpool.Pool,
	leaseSetID string,
	age time.Duration,
) {
	t.Helper()
	approvedTTL := time.Duration(contracts.MinimumPublicationLeaseTTLMS) * time.Millisecond
	var updatedAt, expiresAt time.Time
	if err := pool.QueryRow(t.Context(), `
		WITH stamp AS MATERIALIZED (
			SELECT clock_timestamp() - $1::interval AS value
		)
		SELECT value, value + $2::interval FROM stamp`,
		intervalString(age), intervalString(approvedTTL),
	).Scan(&updatedAt, &expiresAt); err != nil {
		t.Fatalf("calculate aged lease timestamps: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		UPDATE forja.lease_sets
		SET updated_at=$1, expires_at=$2
		WHERE tenant_id=$3 AND repository_id=$4 AND lease_set_id=$5`,
		updatedAt, expiresAt, DefaultTenantID, DefaultRepositoryID, leaseSetID,
	); err != nil {
		t.Fatalf("age lease set: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		UPDATE forja.leases AS lease
		SET updated_at=$1, expires_at=$2
		FROM forja.lease_set_members AS member
		WHERE member.tenant_id=$3 AND member.repository_id=$4
		  AND member.lease_set_id=$5
		  AND lease.tenant_id=member.tenant_id
		  AND lease.repository_id=member.repository_id
		  AND lease.resource_type=member.resource_type
		  AND lease.resource_id=member.resource_id`,
		updatedAt, expiresAt, DefaultTenantID, DefaultRepositoryID, leaseSetID,
	); err != nil {
		t.Fatalf("age lease set members: %v", err)
	}
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
	receiptZone := time.FixedZone("receipt-offset", 5*60*60+30*60)
	receipt := contracts.DeliveryReceipt{
		DeliveryID: deliveryID, SchemaVersion: contracts.DeliverySchemaVersion, Status: "published",
		TenantID: "tenant_" + DefaultTenantID, RepositoryID: "repo_" + DefaultRepositoryID,
		BaseCommit: strings.Repeat("d", 40), ResultCommit: resultCommit,
		ResultTree: strings.Repeat("e", 40), PatchSHA256: strings.Repeat("f", 64),
		ChangedPaths:              []string{"internal/generated/value.go"},
		PublicationRef:            "refs/forja/deliveries/" + deliveryID,
		PublicationPreviousCommit: &previousCommit,
		AuthorID:                  "author", ValidatorID: "validator", LeaseFences: fences,
		ValidationReportRef: "evidence/validation-report.json#sha256=" + strings.Repeat("1", 64),
		EvidenceManifestRef: "evidence/manifest.json#sha256=" + strings.Repeat("2", 64),
		CreatedAt:           time.Unix(1_700_000_000, 123_456_789).In(receiptZone),
		PublishedAt:         time.Unix(1_700_000_000, 987_654_321).In(receiptZone),
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("marshal publication receipt fixture: %v", err)
	}
	authorityDigest := sha256.Sum256([]byte(authority))
	receiptDigest := sha256.Sum256(receiptJSON)
	intent := persistence.DeliveryPublicationIntent{
		DeliveryID: deliveryID, TenantID: DefaultTenantID,
		RepositoryID: DefaultRepositoryID, AttemptID: attemptID, LeaseSetID: leaseSet.LeaseSetID,
		LeaseTTLMS:     contracts.MinimumPublicationLeaseTTLMS,
		PublicationRef: receipt.PublicationRef, PublicationPreviousCommit: &previousCommit,
		ResultCommit: resultCommit, AuthoritySHA256: fmt.Sprintf("%x", authorityDigest),
		ReceiptSHA256: fmt.Sprintf("%x", receiptDigest), ReceiptJSON: receiptJSON,
	}
	identityJSON, err := json.Marshal(struct {
		DeliveryID      string `json:"delivery_id"`
		TenantID        string `json:"tenant_id"`
		RepositoryID    string `json:"repository_id"`
		AttemptID       string `json:"attempt_id"`
		LeaseSetID      string `json:"lease_set_id"`
		LeaseTTLMS      int    `json:"lease_ttl_ms"`
		AuthoritySHA256 string `json:"authority_sha256"`
		ReceiptSHA256   string `json:"receipt_sha256"`
	}{
		intent.DeliveryID, intent.TenantID, intent.RepositoryID,
		intent.AttemptID, intent.LeaseSetID, intent.LeaseTTLMS,
		intent.AuthoritySHA256, intent.ReceiptSHA256,
	})
	if err != nil {
		t.Fatalf("marshal publication identity fixture: %v", err)
	}
	identityDigest := sha256.Sum256(identityJSON)
	intent.IntentSHA256 = fmt.Sprintf("%x", identityDigest)
	return intent
}
