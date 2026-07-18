package delivery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/persistence"
)

func TestPublicationServicePersistsBeforeCASAndReleasesAfterReceipt(t *testing.T) {
	fixture := newPublicationFixture(t)
	journal := newMemoryPublicationJournal()
	leases := &recordingLeaseSets{events: &journal.events}
	service, err := NewPublicationService(fixture.manager, journal, leases, fixture.authority())
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := service.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.LeaseReleased || outcome.Replayed ||
		outcome.Receipt.ResultCommit != fixture.result.ResultCommit {
		t.Fatalf("publication outcome = %#v", outcome)
	}
	if !slices.Equal(journal.events, []string{"prepare", "complete", "release"}) {
		t.Fatalf("publication order = %q", journal.events)
	}
	if ref := strings.TrimSpace(runGitTest(
		t, fixture.repository, "rev-parse", fixture.request.PublicationRef,
	)); ref != fixture.result.ResultCommit {
		t.Fatalf("published ref = %s", ref)
	}
	// Durable publication replay must not depend on the original lease still
	// being live after its successful release.
	fixture.leaseSet.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	for index := range fixture.leaseSet.Leases {
		fixture.leaseSet.Leases[index].ExpiresAt = fixture.leaseSet.ExpiresAt
	}
	replayed, err := service.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || !replayed.LeaseReleased {
		t.Fatalf("publication replay = %#v", replayed)
	}
	if !slices.Equal(journal.events, []string{"prepare", "complete", "release"}) {
		t.Fatalf("publication replay order = %q", journal.events)
	}
}

func TestPublicationServiceRecoversCrashAfterGitCAS(t *testing.T) {
	fixture := newPublicationFixture(t)
	journal := newMemoryPublicationJournal()
	journal.failComplete = true
	leases := &recordingLeaseSets{events: &journal.events}
	service, err := NewPublicationService(fixture.manager, journal, leases, fixture.authority())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
	); err == nil || !strings.Contains(err.Error(), "persist published") {
		t.Fatalf("interrupted publication error = %v", err)
	}
	if ref := strings.TrimSpace(runGitTest(
		t, fixture.repository, "rev-parse", fixture.request.PublicationRef,
	)); ref != fixture.result.ResultCommit {
		t.Fatalf("CAS did not complete before simulated crash: %s", ref)
	}
	record, found, err := journal.GetDeliveryPublication(
		t.Context(), fixture.request.DeliveryID, fixture.request.AttemptID,
	)
	if err != nil || !found || record.State != "prepared" {
		t.Fatalf("durable recovery intent = %#v found=%v err=%v", record, found, err)
	}

	journal.failComplete = false
	outcome, err := service.Recover(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Replayed || !outcome.LeaseReleased {
		t.Fatalf("recovery outcome = %#v", outcome)
	}
	if !slices.Equal(journal.events, []string{"prepare", "complete-failed", "recover", "release"}) {
		t.Fatalf("recovery order = %q", journal.events)
	}
}

func TestPublicationServiceRechecksRefAfterRecoveryTransition(t *testing.T) {
	fixture := newPublicationFixture(t)
	journal := newMemoryPublicationJournal()
	journal.failComplete = true
	leases := &recordingLeaseSets{events: &journal.events}
	service, err := NewPublicationService(fixture.manager, journal, leases, fixture.authority())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
	); err == nil {
		t.Fatal("publication did not create the prepared crash-recovery state")
	}
	journal.failComplete = false
	journal.onRecoverComplete = func() {
		runGitTest(
			t, fixture.repository, "update-ref", "-d",
			fixture.request.PublicationRef, fixture.result.ResultCommit,
		)
	}
	if _, err := service.Recover(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
	); !errors.Is(err, ErrPublicationConflict) {
		t.Fatalf("post-recovery ref error = %v", err)
	}
	if leases.releaseCalls != 0 || slices.Contains(journal.events, "release") {
		t.Fatalf("lease released after stale recovery observation: %q", journal.events)
	}
}

func TestPublicationServiceRejectsChangedRefAndRecordsConflict(t *testing.T) {
	fixture := newPublicationFixture(t)
	journal := newMemoryPublicationJournal()
	leases := &recordingLeaseSets{events: &journal.events}
	service, err := NewPublicationService(fixture.manager, journal, leases, fixture.authority())
	if err != nil {
		t.Fatal(err)
	}
	other := createUnrelatedCommit(t, fixture.repository)
	runGitTest(t, fixture.repository, "update-ref", fixture.request.PublicationRef, other)
	if _, err := service.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
	); !errors.Is(err, ErrPublicationConflict) {
		t.Fatalf("changed-ref publication error = %v", err)
	}
	if !slices.Equal(journal.events, []string{"prepare", "conflict"}) {
		t.Fatalf("conflict order = %q", journal.events)
	}
	if ref := strings.TrimSpace(runGitTest(
		t, fixture.repository, "rev-parse", fixture.request.PublicationRef,
	)); ref != other {
		t.Fatalf("conflicting ref was overwritten: %s", ref)
	}
}

func TestPublicationServiceRejectsDescendantInsteadOfExactRef(t *testing.T) {
	fixture := newPublicationFixture(t)
	journal := newMemoryPublicationJournal()
	service, err := NewPublicationService(
		fixture.manager, journal, &recordingLeaseSets{events: &journal.events}, fixture.authority(),
	)
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, fixture.repository, "update-ref", "-d", fixture.request.PublicationRef)
	runGitTest(
		t, fixture.repository, "update-ref",
		fixture.request.PublicationRef+"/descendant", fixture.result.BaseCommit,
	)
	if _, err := service.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
	); err == nil || !strings.Contains(err.Error(), "does not identify a canonical commit") {
		t.Fatalf("descendant ref publication error = %v", err)
	}
	if !slices.Equal(journal.events, []string{"prepare"}) {
		t.Fatalf("descendant ref publication events = %q", journal.events)
	}
}

func TestPublicationServiceDoesNotReleaseBeforeDurableReceipt(t *testing.T) {
	fixture := newPublicationFixture(t)
	journal := newMemoryPublicationJournal()
	journal.failComplete = true
	leases := &recordingLeaseSets{events: &journal.events}
	service, err := NewPublicationService(fixture.manager, journal, leases, fixture.authority())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
	); err == nil {
		t.Fatal("publication with failed receipt persistence succeeded")
	}
	if leases.releaseCalls != 0 || slices.Contains(journal.events, "release") {
		t.Fatalf("lease released before durable receipt: events=%q", journal.events)
	}
}

func TestPublicationServiceRejectsChangedPersistedEvidenceBeforeJournal(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*testing.T, string)
	}{
		{
			name: "modified",
			change: func(t *testing.T, evidencePath string) {
				t.Helper()
				if err := os.WriteFile(evidencePath, []byte("tampered\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing",
			change: func(t *testing.T, evidencePath string) {
				t.Helper()
				if err := os.Remove(evidencePath); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newPublicationFixture(t)
			reportPath, _, _ := strings.Cut(fixture.bundle.ReportRef, "#sha256=")
			scenario.change(
				t, filepath.Join(fixture.bundle.PhysicalRoot, filepath.FromSlash(reportPath)),
			)
			journal := newMemoryPublicationJournal()
			service, err := NewPublicationService(
				fixture.manager, journal,
				&recordingLeaseSets{events: &journal.events}, fixture.authority(),
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Publish(
				t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
			); err == nil || !strings.Contains(err.Error(), "persisted publication evidence") {
				t.Fatalf("changed evidence publication error = %v", err)
			}
			if len(journal.events) != 0 {
				t.Fatalf("changed evidence reached publication journal: %q", journal.events)
			}
		})
	}
}

func TestPublicationServiceRevalidatesEvidenceInsidePublicationFence(t *testing.T) {
	fixture := newPublicationFixture(t)
	reportPath, _, _ := strings.Cut(fixture.bundle.ReportRef, "#sha256=")
	journal := newMemoryPublicationJournal()
	journal.onPrepare = func() {
		if err := os.WriteFile(
			filepath.Join(fixture.bundle.PhysicalRoot, filepath.FromSlash(reportPath)),
			[]byte("changed after prepare\n"), 0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	leases := &recordingLeaseSets{events: &journal.events}
	service, err := NewPublicationService(fixture.manager, journal, leases, fixture.authority())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
	); err == nil || !strings.Contains(err.Error(), "revalidate persisted publication evidence") {
		t.Fatalf("post-prepare evidence error = %v", err)
	}
	if leases.releaseCalls != 0 || !slices.Equal(journal.events, []string{"prepare"}) {
		t.Fatalf("post-prepare evidence events = %q", journal.events)
	}
	if ref := strings.TrimSpace(runGitTest(
		t, fixture.repository, "rev-parse", fixture.request.PublicationRef,
	)); ref != *fixture.request.PublicationPreviousCommit {
		t.Fatalf("post-prepare evidence changed publication ref: %s", ref)
	}
}

func TestVerifyPersistedEvidenceRootStaysBoundAfterPathReplacement(t *testing.T) {
	fixture := newPublicationFixture(t)
	reportPath, _, _ := strings.Cut(fixture.bundle.ReportRef, "#sha256=")
	manifestPath, _, _ := strings.Cut(fixture.bundle.ManifestRef, "#sha256=")
	expected := make(map[string]contracts.EvidenceEntry, len(fixture.bundle.Manifest.Entries))
	for _, entry := range fixture.bundle.Manifest.Entries {
		expected[entry.Path] = entry
	}
	root, err := os.OpenRoot(fixture.bundle.PhysicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	detached := fixture.bundle.PhysicalRoot + ".detached"
	if err := os.Rename(fixture.bundle.PhysicalRoot, detached); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixture.bundle.PhysicalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(fixture.bundle.PhysicalRoot, "replacement"), []byte("tampered\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := verifyPersistedEvidenceRoot(
		root, fixture.bundle, reportPath, manifestPath, expected,
	); err != nil {
		t.Fatalf("descriptor-bound evidence verification failed after path replacement: %v", err)
	}
}

func TestOpenedEvidenceRootMustMatchPreOpenIdentity(t *testing.T) {
	original := t.TempDir()
	expectedIdentity, err := os.Lstat(original)
	if err != nil {
		t.Fatal(err)
	}
	detached := original + ".detached"
	if err := os.Rename(original, detached); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(original)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	openedIdentity, err := root.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	if err := requireSameEvidenceRoot(expectedIdentity, openedIdentity); err == nil {
		t.Fatal("replacement evidence root passed the pre-open identity check")
	}
}

func TestPublicationServiceRechecksRefAfterConcurrentCompletion(t *testing.T) {
	fixture := newPublicationFixture(t)
	journal := newMemoryPublicationJournal()
	journal.completeAsConcurrentPublish = true
	journal.onConcurrentComplete = func() {
		runGitTest(
			t, fixture.repository, "update-ref", "--no-deref",
			fixture.request.PublicationRef, fixture.result.ResultCommit,
			*fixture.request.PublicationPreviousCommit,
		)
		runGitTest(
			t, fixture.repository, "update-ref", "-d",
			fixture.request.PublicationRef, fixture.result.ResultCommit,
		)
	}
	leases := &recordingLeaseSets{events: &journal.events}
	service, err := NewPublicationService(fixture.manager, journal, leases, fixture.authority())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
	); !errors.Is(err, ErrPublicationConflict) {
		t.Fatalf("concurrent completion ref error = %v", err)
	}
	if leases.releaseCalls != 0 || slices.Contains(journal.events, "release") {
		t.Fatalf("lease released after stale concurrent observation: %q", journal.events)
	}
	if !slices.Equal(journal.events, []string{"prepare", "complete-replayed"}) {
		t.Fatalf("concurrent completion events = %q", journal.events)
	}
}

func TestPublicationServiceUsesStablePublicationOperationTimestamp(t *testing.T) {
	fixture := newPublicationFixture(t)
	journal := newMemoryPublicationJournal()
	leases := &recordingLeaseSets{events: &journal.events}
	service, err := NewPublicationService(fixture.manager, journal, leases, fixture.authority())
	if err != nil {
		t.Fatal(err)
	}
	publicationAt := fixture.bundle.Report.CreatedAt.Add(time.Second).UTC().Truncate(time.Microsecond)
	service.now = func() time.Time { return publicationAt }
	outcome, err := service.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Receipt.CreatedAt.Equal(publicationAt) ||
		!outcome.Receipt.PublishedAt.Equal(publicationAt) ||
		outcome.Receipt.PublishedAt.Equal(fixture.bundle.Report.CreatedAt) {
		t.Fatalf("publication receipt timestamps = %#v", outcome.Receipt)
	}
	if journal.record.PublishedAt == nil || !journal.record.PublishedAt.Equal(publicationAt) {
		t.Fatalf("journal publication timestamp = %v, want %v", journal.record.PublishedAt, publicationAt)
	}

	service.now = func() time.Time { return publicationAt.Add(time.Hour) }
	replayed, err := service.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || !replayed.Receipt.PublishedAt.Equal(publicationAt) {
		t.Fatalf("publication timestamp replay = %#v", replayed)
	}
}

func TestPublicationServicePreservesRecordedNanosecondTimestamp(t *testing.T) {
	fixture := newPublicationFixture(t)
	journal := newMemoryPublicationJournal()
	leases := &recordingLeaseSets{events: &journal.events}
	service, err := NewPublicationService(fixture.manager, journal, leases, fixture.authority())
	if err != nil {
		t.Fatal(err)
	}
	legacyZone := time.FixedZone("legacy-offset", 5*60*60+30*60)
	legacyAt := fixture.bundle.Report.CreatedAt.Add(time.Second + 123*time.Nanosecond).In(legacyZone)
	if legacyAt.Equal(legacyAt.Truncate(time.Microsecond)) {
		legacyAt = legacyAt.Add(time.Nanosecond)
	}
	legacyCreatedAt := fixture.bundle.Report.CreatedAt.Add(500 * time.Millisecond).In(legacyZone)
	intent, _, err := service.publicationIntent(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
		publicationReceiptTimes{CreatedAt: legacyCreatedAt, PublishedAt: legacyAt},
	)
	if err != nil {
		t.Fatal(err)
	}
	preparedAt := time.Now().UTC()
	journal.record = persistence.DeliveryPublication{
		Intent: intent, State: "prepared", PreparedAt: preparedAt, UpdatedAt: preparedAt,
	}
	journal.found = true
	runGitTest(
		t, fixture.repository, "update-ref", "--no-deref",
		fixture.request.PublicationRef, fixture.result.ResultCommit,
		*fixture.request.PublicationPreviousCommit,
	)

	outcome, err := service.Recover(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Replayed || !outcome.Receipt.CreatedAt.Equal(legacyCreatedAt) ||
		!outcome.Receipt.PublishedAt.Equal(legacyAt) {
		t.Fatalf("nanosecond timestamp recovery = %#v, want %v", outcome, legacyAt)
	}
	if len(intent.ReceiptJSON) == 0 ||
		outcome.Receipt.PublishedAt.Equal(outcome.Receipt.PublishedAt.Truncate(time.Microsecond)) {
		t.Fatalf("recorded timestamp precision was lost: %v", outcome.Receipt.PublishedAt)
	}
	if got, want := outcome.Receipt.CreatedAt.Format(time.RFC3339Nano), legacyCreatedAt.Format(time.RFC3339Nano); got != want {
		t.Fatalf("recorded created_at representation = %q, want %q", got, want)
	}
	if got, want := outcome.Receipt.PublishedAt.Format(time.RFC3339Nano), legacyAt.Format(time.RFC3339Nano); got != want {
		t.Fatalf("recorded published_at representation = %q, want %q", got, want)
	}
}

func TestPublicationServiceReleasesWhenPrepareObservesConcurrentPublish(t *testing.T) {
	fixture := newPublicationFixture(t)
	journal := newMemoryPublicationJournal()
	journal.publishDuringPrepare = true
	journal.onPrepare = func() {
		runGitTest(
			t, fixture.repository, "update-ref", "--no-deref",
			fixture.request.PublicationRef, fixture.result.ResultCommit,
			*fixture.request.PublicationPreviousCommit,
		)
	}
	leases := &recordingLeaseSets{events: &journal.events}
	service, err := NewPublicationService(fixture.manager, journal, leases, fixture.authority())
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Replayed || !outcome.LeaseReleased || leases.releaseCalls != 1 {
		t.Fatalf("concurrent publication outcome = %#v release_calls=%d", outcome, leases.releaseCalls)
	}
	if !slices.Equal(journal.events, []string{"prepare", "release"}) {
		t.Fatalf("concurrent publication events = %q", journal.events)
	}
}

func TestPublicationServiceRejectsIncompleteLeaseAuthority(t *testing.T) {
	fixture := newPublicationFixture(t)
	fixture.leaseSet.Leases = fixture.leaseSet.Leases[1:]
	journal := newMemoryPublicationJournal()
	service, err := NewPublicationService(
		fixture.manager, journal, &recordingLeaseSets{events: &journal.events}, fixture.authority(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
	); err == nil || !strings.Contains(err.Error(), "fences disagree") {
		t.Fatalf("incomplete lease authority error = %v", err)
	}
	if len(journal.events) != 0 {
		t.Fatalf("invalid authority reached publication journal: %q", journal.events)
	}
}

func TestPublicationServiceCannotRedirectRepositoryAuthority(t *testing.T) {
	fixture := newPublicationFixture(t)
	otherRepository, _, otherBase := deliveryRepository(t)
	runGitTest(t, otherRepository, "update-ref", fixture.request.PublicationRef, otherBase)

	journal := newMemoryPublicationJournal()
	service, err := NewPublicationService(
		fixture.manager, journal, &recordingLeaseSets{events: &journal.events}, fixture.authority(),
	)
	if err != nil {
		t.Fatal(err)
	}
	redirected := fixture.request
	redirected.RepositoryPath = otherRepository
	if _, err := service.Publish(
		t.Context(), redirected, fixture.result, fixture.bundle, fixture.leaseSet,
	); err == nil || !strings.Contains(err.Error(), "repository authority") {
		t.Fatalf("redirected repository publication error = %v", err)
	}
	if len(journal.events) != 0 {
		t.Fatalf("redirected repository reached publication journal: %q", journal.events)
	}
	if ref := strings.TrimSpace(runGitTest(
		t, otherRepository, "rev-parse", fixture.request.PublicationRef,
	)); ref != otherBase {
		t.Fatalf("redirected publication mutated repository B: %s", ref)
	}
}

func TestPublicationServiceRejectsRepositoryPathReplacement(t *testing.T) {
	fixture := newPublicationFixture(t)
	replacementRepository, _, replacementBase := deliveryRepository(t)
	runGitTest(t, replacementRepository, "update-ref", fixture.request.PublicationRef, replacementBase)

	journal := newMemoryPublicationJournal()
	service, err := NewPublicationService(
		fixture.manager, journal, &recordingLeaseSets{events: &journal.events}, fixture.authority(),
	)
	if err != nil {
		t.Fatal(err)
	}
	originalRepository := fixture.repository + "-authorized"
	if err := os.Rename(fixture.repository, originalRepository); err != nil {
		t.Fatalf("move authorized repository: %v", err)
	}
	if err := os.Rename(replacementRepository, fixture.repository); err != nil {
		t.Fatalf("replace authorized repository path: %v", err)
	}

	if _, err := service.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
	); err == nil || !strings.Contains(err.Error(), "authority changed on disk") {
		t.Fatalf("replaced repository authority error = %v", err)
	}
	if len(journal.events) != 0 {
		t.Fatalf("replaced repository reached publication journal: %q", journal.events)
	}
	if ref := strings.TrimSpace(runGitTest(
		t, fixture.repository, "rev-parse", fixture.request.PublicationRef,
	)); ref != replacementBase {
		t.Fatalf("replacement repository was mutated: %s", ref)
	}
}

func TestPublicationServiceRejectsLeaseFromAnotherRepository(t *testing.T) {
	fixture := newPublicationFixture(t)
	fixture.leaseSet.Leases[0].RepositoryID = "00000000-0000-4000-8000-000000000099"
	journal := newMemoryPublicationJournal()
	service, err := NewPublicationService(
		fixture.manager, journal, &recordingLeaseSets{events: &journal.events}, fixture.authority(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(
		t.Context(), fixture.request, fixture.result, fixture.bundle, fixture.leaseSet,
	); err == nil || !strings.Contains(err.Error(), "inconsistent ownership proof") {
		t.Fatalf("cross-repository lease publication error = %v", err)
	}
	if len(journal.events) != 0 {
		t.Fatalf("cross-repository lease reached publication journal: %q", journal.events)
	}
}

type publicationFixture struct {
	repository string
	manager    *WorktreeManager
	request    contracts.DeliveryRequest
	result     CommitResult
	bundle     ValidationBundle
	leaseSet   persistence.LeaseSet
}

func (f publicationFixture) authority() RepositoryAuthority {
	return RepositoryAuthority{
		TenantID: f.request.TenantID, RepositoryID: f.request.RepositoryID,
		RepositoryPath: f.request.RepositoryPath,
	}
}

func newPublicationFixture(t *testing.T) publicationFixture {
	t.Helper()
	repository, root, base := deliveryRepository(t)
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	worktree, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(worktree.Path, "internal/generated/value.txt"),
		[]byte("published\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	result, err := manager.CreateResultCommit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	validation := testValidationService(
		t, manager, t.TempDir(),
		[]ValidatorDefinition{testValidatorDefinition(t, "unit-tests", "pass")}, nil,
	)
	bundle, err := validation.Validate(t.Context(), request, result)
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "update-ref", request.PublicationRef, base)
	now := time.Now().UTC()
	owner := "delivery-service"
	leaseSet := persistence.LeaseSet{
		LeaseSetID: request.AttemptID, OwnerID: owner,
		AcquiredAt: now, ExpiresAt: now.Add(time.Minute),
		Leases: []persistence.Lease{
			publicationLease(t, request, owner, "worktree", request.DeliveryID, 4, now),
			publicationLease(t, request, owner, "file", "internal/generated", 3, now),
			publicationLease(t, request, owner, "artifact", "evidence", 1, now),
			publicationLease(t, request, owner, "file", "internal", 2, now),
		},
	}
	return publicationFixture{
		repository: repository, manager: manager, request: request,
		result: result, bundle: bundle, leaseSet: leaseSet,
	}
}

func publicationLease(
	t *testing.T,
	request contracts.DeliveryRequest,
	owner string,
	resourceType string,
	resourceID string,
	token int64,
	now time.Time,
) persistence.Lease {
	t.Helper()
	storageTenantID, storageRepositoryID, err := contracts.RepositoryStorageIdentity(
		request.TenantID, request.RepositoryID,
	)
	if err != nil {
		t.Fatal(err)
	}
	return persistence.Lease{
		LeaseKey: persistence.LeaseKey{
			TenantID: storageTenantID, RepositoryID: storageRepositoryID,
			ResourceType: resourceType, ResourceID: resourceID,
		},
		OwnerID: owner, FencingToken: token,
		AcquiredAt: now, ExpiresAt: now.Add(time.Minute),
	}
}

func createUnrelatedCommit(t *testing.T, repository string) string {
	t.Helper()
	worktree := t.TempDir()
	runGitTest(t, worktree, "init", "--quiet")
	runGitTest(t, worktree, "config", "user.name", "Forja Test")
	runGitTest(t, worktree, "config", "user.email", "forja-test@example.invalid")
	if err := os.WriteFile(filepath.Join(worktree, "unrelated.txt"), []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, worktree, "add", "unrelated.txt")
	runGitTest(t, worktree, "commit", "--quiet", "-m", "unrelated")
	commit := strings.TrimSpace(runGitTest(t, worktree, "rev-parse", "HEAD"))
	bundle := filepath.Join(t.TempDir(), "commit.bundle")
	runGitTest(t, worktree, "bundle", "create", bundle, "HEAD")
	runGitTest(t, repository, "fetch", "--quiet", bundle, commit)
	return commit
}

type memoryPublicationJournal struct {
	record                      persistence.DeliveryPublication
	found                       bool
	failComplete                bool
	publishDuringPrepare        bool
	completeAsConcurrentPublish bool
	onPrepare                   func()
	onConcurrentComplete        func()
	onRecoverComplete           func()
	events                      []string
}

func newMemoryPublicationJournal() *memoryPublicationJournal {
	return &memoryPublicationJournal{}
}

func (m *memoryPublicationJournal) GetDeliveryPublication(
	_ context.Context,
	deliveryID string,
	attemptID string,
) (persistence.DeliveryPublication, bool, error) {
	if !m.found || m.record.Intent.DeliveryID != deliveryID ||
		m.record.Intent.AttemptID != attemptID {
		return persistence.DeliveryPublication{}, false, nil
	}
	return clonePublicationRecord(m.record), true, nil
}

func (m *memoryPublicationJournal) PrepareDeliveryPublication(
	_ context.Context,
	intent persistence.DeliveryPublicationIntent,
	_ persistence.LeaseSet,
) (persistence.DeliveryPublication, error) {
	m.events = append(m.events, "prepare")
	if m.found {
		if err := requireExactPublicationRecord(m.record, intent); err != nil {
			return persistence.DeliveryPublication{}, err
		}
		return clonePublicationRecord(m.record), nil
	}
	now := time.Now().UTC()
	m.record = persistence.DeliveryPublication{
		Intent: clonePublicationIntent(intent), State: "prepared",
		PreparedAt: now, UpdatedAt: now,
	}
	m.found = true
	if m.onPrepare != nil {
		m.onPrepare()
	}
	if m.publishDuringPrepare {
		now, err := recordedPublicationTime(m.record)
		if err != nil {
			return persistence.DeliveryPublication{}, err
		}
		m.record.State = "published"
		m.record.ObservedCommit = cloneOptionalString(&intent.ResultCommit)
		m.record.PublishedAt = &now
		m.record.UpdatedAt = now
	}
	return clonePublicationRecord(m.record), nil
}

func (m *memoryPublicationJournal) CompleteDeliveryPublication(
	ctx context.Context,
	intent persistence.DeliveryPublicationIntent,
	_ persistence.LeaseSet,
	revalidate func(context.Context) error,
	apply func(context.Context) error,
) (persistence.DeliveryPublication, error) {
	if revalidate == nil || apply == nil {
		return persistence.DeliveryPublication{}, fmt.Errorf("revalidation or apply callback is missing")
	}
	if !m.found {
		return persistence.DeliveryPublication{}, fmt.Errorf("intent is missing")
	}
	if err := requireExactPublicationRecord(m.record, intent); err != nil {
		return persistence.DeliveryPublication{}, err
	}
	if m.completeAsConcurrentPublish {
		publishedAt, err := recordedPublicationTime(m.record)
		if err != nil {
			return persistence.DeliveryPublication{}, err
		}
		m.record.State = "published"
		m.record.ObservedCommit = cloneOptionalString(&intent.ResultCommit)
		m.record.PublishedAt = &publishedAt
		m.record.UpdatedAt = time.Now().UTC()
		m.events = append(m.events, "complete-replayed")
		if m.onConcurrentComplete != nil {
			m.onConcurrentComplete()
		}
		return clonePublicationRecord(m.record), nil
	}
	if m.record.State == "published" {
		return clonePublicationRecord(m.record), nil
	}
	if m.record.State != "prepared" {
		return persistence.DeliveryPublication{}, fmt.Errorf("intent is not prepared")
	}
	if err := revalidate(ctx); err != nil {
		return persistence.DeliveryPublication{}, err
	}
	if err := apply(ctx); err != nil {
		return persistence.DeliveryPublication{}, err
	}
	if m.failComplete {
		m.events = append(m.events, "complete-failed")
		return persistence.DeliveryPublication{}, fmt.Errorf("simulated persistence failure")
	}
	m.events = append(m.events, "complete")
	now, err := recordedPublicationTime(m.record)
	if err != nil {
		return persistence.DeliveryPublication{}, err
	}
	m.record.State = "published"
	m.record.ObservedCommit = cloneOptionalString(&intent.ResultCommit)
	m.record.PublishedAt = &now
	m.record.UpdatedAt = now
	return clonePublicationRecord(m.record), nil
}

func (m *memoryPublicationJournal) RecoverDeliveryPublication(
	_ context.Context,
	intent persistence.DeliveryPublicationIntent,
	observedCommit string,
) (persistence.DeliveryPublication, error) {
	m.events = append(m.events, "recover")
	if !m.found || observedCommit != intent.ResultCommit {
		return persistence.DeliveryPublication{}, fmt.Errorf("recovery observation is invalid")
	}
	if err := requireExactPublicationRecord(m.record, intent); err != nil {
		return persistence.DeliveryPublication{}, err
	}
	now, err := recordedPublicationTime(m.record)
	if err != nil {
		return persistence.DeliveryPublication{}, err
	}
	m.record.State = "published"
	m.record.ObservedCommit = &observedCommit
	m.record.PublishedAt = &now
	m.record.UpdatedAt = now
	if m.onRecoverComplete != nil {
		m.onRecoverComplete()
	}
	return clonePublicationRecord(m.record), nil
}

func (m *memoryPublicationJournal) ConflictDeliveryPublication(
	_ context.Context,
	intent persistence.DeliveryPublicationIntent,
	observedCommit *string,
) (persistence.DeliveryPublication, error) {
	m.events = append(m.events, "conflict")
	if !m.found {
		return persistence.DeliveryPublication{}, fmt.Errorf("intent is missing")
	}
	if err := requireExactPublicationRecord(m.record, intent); err != nil {
		return persistence.DeliveryPublication{}, err
	}
	m.record.State = "conflict"
	m.record.ObservedCommit = cloneOptionalString(observedCommit)
	m.record.UpdatedAt = time.Now().UTC()
	return clonePublicationRecord(m.record), nil
}

type recordingLeaseSets struct {
	events       *[]string
	releaseCalls int
	releaseErr   error
	released     bool
}

func (*recordingLeaseSets) AcquireLeaseSet(
	context.Context, string, []persistence.LeaseKey, string, time.Duration,
) (persistence.LeaseSet, error) {
	return persistence.LeaseSet{}, fmt.Errorf("not implemented by publication fixture")
}

func (*recordingLeaseSets) RenewLeaseSet(
	context.Context, persistence.LeaseSet, time.Duration,
) (persistence.LeaseSet, error) {
	return persistence.LeaseSet{}, fmt.Errorf("not implemented by publication fixture")
}

func (r *recordingLeaseSets) ReleaseLeaseSet(
	_ context.Context,
	_ persistence.LeaseSet,
) error {
	if r.released {
		return nil
	}
	r.releaseCalls++
	*r.events = append(*r.events, "release")
	if r.releaseErr != nil {
		return r.releaseErr
	}
	r.released = true
	return nil
}

func clonePublicationRecord(value persistence.DeliveryPublication) persistence.DeliveryPublication {
	value.Intent = clonePublicationIntent(value.Intent)
	value.ObservedCommit = cloneOptionalString(value.ObservedCommit)
	if value.PublishedAt != nil {
		published := *value.PublishedAt
		value.PublishedAt = &published
	}
	return value
}

func clonePublicationIntent(
	value persistence.DeliveryPublicationIntent,
) persistence.DeliveryPublicationIntent {
	value.PublicationPreviousCommit = cloneOptionalString(value.PublicationPreviousCommit)
	value.ReceiptJSON = bytes.Clone(value.ReceiptJSON)
	return value
}
