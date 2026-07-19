package knowledge

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/objectstore"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

func TestReconcilerClassifiesProviderEvidence(t *testing.T) {
	repository := &fakeReconciliationRepository{candidates: []persistence.ArtifactReconciliationCandidate{
		reconciliationCandidate("artifact_operation_60000000-0000-4000-8000-000000000001", "good"),
		reconciliationCandidate("artifact_operation_60000000-0000-4000-8000-000000000002", "missing"),
		reconciliationCandidate("artifact_operation_60000000-0000-4000-8000-000000000003", "corrupt"),
	}}
	verifier := &fakeBodyVerifier{failures: map[string]error{
		digestFor("missing"): objectstore.ErrNotFound,
		digestFor("corrupt"): objectstore.ErrIntegrity,
	}}
	reconciler, err := NewReconciler(repository, verifier)
	if err != nil {
		t.Fatal(err)
	}
	report, err := reconciler.Reconcile(t.Context(), time.Now(), 10, runstate.CommandMetadata{
		IdempotencyKey: "reconcile-batch", ActorType: "system",
		ActorID: "reconciler", CorrelationID: "reconcile-batch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Examined != 3 || report.Completed != 1 || report.Retryable != 1 || report.Terminal != 1 {
		t.Fatalf("report=%#v", report)
	}
	if len(repository.completed) != 1 || repository.failed["interrupted"] != 1 || repository.failed["integrity"] != 1 {
		t.Fatalf("completed=%v failed=%v", repository.completed, repository.failed)
	}
	for _, metadata := range repository.metadata {
		if len(metadata.IdempotencyKey) > 200 || metadata.ActorType != "system" {
			t.Fatalf("derived metadata=%#v", metadata)
		}
	}
}

func TestReconcilerPersistsInvalidDescriptorAsCanonicalConflict(t *testing.T) {
	candidate := reconciliationCandidate(
		"artifact_operation_60000000-0000-4000-8000-000000000004",
		"invalid-descriptor",
	)
	candidate.Publication.Intent.ContentHash = "sha256:not-a-digest"
	repository := &fakeReconciliationRepository{
		candidates: []persistence.ArtifactReconciliationCandidate{candidate},
	}
	reconciler, err := NewReconciler(repository, &fakeBodyVerifier{})
	if err != nil {
		t.Fatal(err)
	}
	report, err := reconciler.Reconcile(t.Context(), time.Now(), 10, runstate.CommandMetadata{
		IdempotencyKey: "reconcile-invalid-descriptor", ActorType: "system",
		ActorID: "reconciler", CorrelationID: "reconcile-invalid-descriptor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Examined != 1 || report.Terminal != 1 || report.Retryable != 0 ||
		repository.failed["canonical_conflict"] != 1 {
		t.Fatalf("report=%#v failed=%v", report, repository.failed)
	}
}

type fakeReconciliationRepository struct {
	candidates []persistence.ArtifactReconciliationCandidate
	completed  []string
	failed     map[string]int
	metadata   []runstate.CommandMetadata
}

func (f *fakeReconciliationRepository) Authority() control.Authority {
	return control.Authority{TenantID: "00000000-0000-4000-8000-000000000001", RepositoryID: "00000000-0000-4000-8000-000000000002"}
}

func (f *fakeReconciliationRepository) ListArtifactReconciliationCandidates(context.Context, time.Time, int) ([]persistence.ArtifactReconciliationCandidate, error) {
	return f.candidates, nil
}

func (f *fakeReconciliationRepository) CompleteArtifactReconciliation(_ context.Context, operationID string, _ persistence.ArtifactEvidence, metadata runstate.CommandMetadata) (contracts.Artifact, error) {
	f.completed = append(f.completed, operationID)
	f.metadata = append(f.metadata, metadata)
	return contracts.Artifact{ArtifactID: operationID}, nil
}

func (f *fakeReconciliationRepository) FailArtifactReconciliation(_ context.Context, _ string, class string, metadata runstate.CommandMetadata) (persistence.ArtifactPublication, error) {
	if f.failed == nil {
		f.failed = make(map[string]int)
	}
	f.failed[class]++
	f.metadata = append(f.metadata, metadata)
	return persistence.ArtifactPublication{}, nil
}

type fakeBodyVerifier struct{ failures map[string]error }

func (f *fakeBodyVerifier) Verify(_ context.Context, authority objectstore.Authority, descriptor objectstore.Descriptor) (objectstore.Evidence, error) {
	if authority.TenantID == "" || authority.RepositoryID == "" {
		return objectstore.Evidence{}, errors.New("missing authority")
	}
	digest := bytesToHex(descriptor.SHA256[:])
	if err := f.failures[digest]; err != nil {
		return objectstore.Evidence{}, err
	}
	return objectstore.Evidence{ObjectKey: "derived", ETag: "etag"}, nil
}

func reconciliationCandidate(operationID, body string) persistence.ArtifactReconciliationCandidate {
	digest := sha256.Sum256([]byte(body))
	return persistence.ArtifactReconciliationCandidate{Publication: persistence.ArtifactPublication{
		Intent: persistence.ArtifactPublicationIntent{
			OperationID: operationID, ArtifactID: "artifact_" + body,
			ContentHash: "sha256:" + bytesToHex(digest[:]), SizeBytes: int64(len(body)),
			MediaType: "text/plain", CreatedBy: "test",
		},
	}}
}

func digestFor(body string) string {
	digest := sha256.Sum256([]byte(body))
	return bytesToHex(digest[:])
}

func bytesToHex(value []byte) string {
	const alphabet = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for index, item := range value {
		encoded[index*2] = alphabet[item>>4]
		encoded[index*2+1] = alphabet[item&15]
	}
	return string(encoded)
}
