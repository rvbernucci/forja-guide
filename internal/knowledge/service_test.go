package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/objectstore"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

func TestPublishArtifactCompletesVerifiedSaga(t *testing.T) {
	command := knowledgeCommand("verified body")
	repository := &fakeRepository{}
	bodies := &fakeBodyStore{evidence: objectstore.Evidence{
		ObjectKey: "verified-key", ETag: `"etag"`, VersionID: "version",
	}}
	service, err := NewService(repository, bodies)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := service.PublishArtifact(t.Context(), command)
	if err != nil {
		t.Fatal(err)
	}
	if repository.steps != "prepare,uploading,complete" || bodies.calls != 1 || artifact.ArtifactID != command.Intent.ArtifactID {
		t.Fatalf("steps=%q body calls=%d artifact=%#v", repository.steps, bodies.calls, artifact)
	}
}

func TestPublishArtifactReplaysWithoutTouchingObjectStore(t *testing.T) {
	command := knowledgeCommand("replayed body")
	want := artifactFromCommand(command)
	repository := &fakeRepository{replay: &want}
	bodies := &fakeBodyStore{}
	service, _ := NewService(repository, bodies)
	artifact, err := service.PublishArtifact(t.Context(), command)
	if err != nil {
		t.Fatal(err)
	}
	if bodies.calls != 0 || repository.steps != "prepare" || artifact.ArtifactID != want.ArtifactID {
		t.Fatal("completed replay performed external work")
	}
}

func TestPublishArtifactJournalsProviderAndIntegrityFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		failure   error
		wantClass string
	}{
		{name: "provider", failure: objectstore.ErrUnavailable, wantClass: "retryable_provider"},
		{name: "integrity", failure: objectstore.ErrIntegrity, wantClass: "integrity"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := knowledgeCommand(test.name)
			repository := &fakeRepository{}
			service, _ := NewService(repository, &fakeBodyStore{err: test.failure})
			if _, err := service.PublishArtifact(t.Context(), command); !errors.Is(err, test.failure) {
				t.Fatalf("publish error = %v", err)
			}
			if repository.failureClass != test.wantClass || repository.steps != "prepare,uploading,fail" {
				t.Fatalf("class=%q steps=%q", repository.failureClass, repository.steps)
			}
		})
	}
}

type fakeRepository struct {
	steps        string
	replay       *contracts.Artifact
	failureClass string
}

func (f *fakeRepository) Authority() control.Authority {
	return control.Authority{
		TenantID:     "00000000-0000-4000-8000-000000000001",
		RepositoryID: "00000000-0000-4000-8000-000000000002",
	}
}

func (f *fakeRepository) PrepareArtifactPublication(
	_ context.Context,
	intent persistence.ArtifactPublicationIntent,
	_ runstate.CommandMetadata,
) (persistence.ArtifactPublication, *contracts.Artifact, error) {
	f.step("prepare")
	return persistence.ArtifactPublication{Intent: intent, State: "reserved", Version: 1}, f.replay, nil
}

func (f *fakeRepository) MarkArtifactPublicationUploading(
	_ context.Context,
	intent persistence.ArtifactPublicationIntent,
	_ runstate.CommandMetadata,
) (persistence.ArtifactPublication, error) {
	f.step("uploading")
	return persistence.ArtifactPublication{Intent: intent, State: "uploading", Version: 2}, nil
}

func (f *fakeRepository) CompleteArtifactPublication(
	_ context.Context,
	intent persistence.ArtifactPublicationIntent,
	_ persistence.ArtifactEvidence,
	_ runstate.CommandMetadata,
) (contracts.Artifact, error) {
	f.step("complete")
	command := PublishArtifactCommand{Intent: intent}
	return artifactFromCommand(command), nil
}

func (f *fakeRepository) FailArtifactPublication(
	_ context.Context,
	intent persistence.ArtifactPublicationIntent,
	failureClass string,
	_ runstate.CommandMetadata,
) (persistence.ArtifactPublication, error) {
	f.step("fail")
	f.failureClass = failureClass
	return persistence.ArtifactPublication{Intent: intent, State: "failed", Version: 3}, nil
}

func (f *fakeRepository) step(value string) {
	if f.steps != "" {
		f.steps += ","
	}
	f.steps += value
}

type fakeBodyStore struct {
	evidence objectstore.Evidence
	err      error
	calls    int
}

func (f *fakeBodyStore) Publish(
	_ context.Context,
	_ objectstore.Authority,
	_ objectstore.Descriptor,
	body io.ReadSeeker,
) (objectstore.Evidence, error) {
	f.calls++
	if body == nil {
		return objectstore.Evidence{}, fmt.Errorf("body missing")
	}
	return f.evidence, f.err
}

func knowledgeCommand(body string) PublishArtifactCommand {
	digest := sha256.Sum256([]byte(body))
	return PublishArtifactCommand{
		Intent: persistence.ArtifactPublicationIntent{
			OperationID: "artifact_operation_40000000-0000-4000-8000-000000000001",
			ArtifactID:  "artifact_knowledge_service", Kind: "test_report",
			ContentHash: fmt.Sprintf("sha256:%x", digest), SizeBytes: int64(len(body)),
			MediaType: "text/plain", CreatedBy: "knowledge-test",
			Provenance: contracts.Provenance{SourceType: "test", SourceRefs: []string{"unit"}},
		},
		Metadata: runstate.CommandMetadata{
			IdempotencyKey: "knowledge-service-idempotency", ActorType: "system",
			ActorID: "knowledge-test", CorrelationID: "knowledge-service-correlation",
		},
		Body: bytes.NewReader([]byte(body)),
	}
}

func artifactFromCommand(command PublishArtifactCommand) contracts.Artifact {
	size := command.Intent.SizeBytes
	return contracts.Artifact{
		ArtifactID: command.Intent.ArtifactID, SchemaVersion: "1.0",
		Kind: command.Intent.Kind, Status: "active", ContentHash: command.Intent.ContentHash,
		MediaType: command.Intent.MediaType, SizeBytes: &size, CreatedAt: time.Unix(1, 0).UTC(),
		CreatedBy: command.Intent.CreatedBy, Provenance: command.Intent.Provenance,
	}
}
