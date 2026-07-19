package knowledge

import (
	"context"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/objectstore"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

func TestRetentionPurgesOnlyCanonicalCandidates(t *testing.T) {
	repository := &fakeRetentionRepository{candidates: []persistence.RetentionCandidate{
		{ContentHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ETag: "one", VersionID: "version-one", SizeBytes: 1, MediaType: "text/plain"},
		{ContentHash: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ETag: "two", VersionID: "version-two", SizeBytes: 1, MediaType: "text/plain"},
	}}
	purger := &fakeBodyPurger{failETag: "two"}
	service, err := NewRetentionService(repository, purger)
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.Purge(t.Context(), time.Now(), 10, runstate.CommandMetadata{
		IdempotencyKey: "retention-batch", ActorType: "system",
		ActorID: "retention-worker", CorrelationID: "retention-batch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Examined != 2 || report.Purged != 1 || report.Deferred != 1 || repository.marked != 1 {
		t.Fatalf("report=%#v marked=%d", report, repository.marked)
	}
}

type fakeRetentionRepository struct {
	candidates []persistence.RetentionCandidate
	marked     int
}

func (f *fakeRetentionRepository) Authority() control.Authority {
	return control.Authority{TenantID: "00000000-0000-4000-8000-000000000001", RepositoryID: "00000000-0000-4000-8000-000000000002"}
}

func (f *fakeRetentionRepository) TombstoneArtifact(context.Context, string, int, runstate.CommandMetadata) (contracts.Artifact, error) {
	return contracts.Artifact{}, nil
}

func (f *fakeRetentionRepository) ListArtifactRetentionCandidates(context.Context, time.Time, int) ([]persistence.RetentionCandidate, error) {
	return f.candidates, nil
}

func (f *fakeRetentionRepository) MarkArtifactObjectPurged(context.Context, string, string, string, runstate.CommandMetadata) error {
	f.marked++
	return nil
}

type fakeBodyPurger struct{ failETag string }

func (f *fakeBodyPurger) Delete(_ context.Context, _ objectstore.Authority, _ objectstore.Descriptor, etag, _ string) error {
	if etag == f.failETag {
		return objectstore.ErrUnavailable
	}
	return nil
}
