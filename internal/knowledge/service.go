// Package knowledge coordinates governed artifact, conversation, and memory workflows.
package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/objectstore"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

type Repository interface {
	persistence.ArtifactPublicationRepository
	Authority() control.Authority
}

type BodyStore interface {
	Publish(
		context.Context,
		objectstore.Authority,
		objectstore.Descriptor,
		io.ReadSeeker,
	) (objectstore.Evidence, error)
}

type Service struct {
	repository Repository
	bodies     BodyStore
}

type PublishArtifactCommand struct {
	Intent   persistence.ArtifactPublicationIntent
	Metadata runstate.CommandMetadata
	Body     io.ReadSeeker
}

func NewService(repository Repository, bodies BodyStore) (*Service, error) {
	if repository == nil || bodies == nil {
		return nil, fmt.Errorf("knowledge repository and body store are required")
	}
	return &Service{repository: repository, bodies: bodies}, nil
}

func (s *Service) PublishArtifact(
	ctx context.Context,
	command PublishArtifactCommand,
) (contracts.Artifact, error) {
	publication, replay, err := s.repository.PrepareArtifactPublication(
		ctx, command.Intent, command.Metadata,
	)
	if err != nil {
		return contracts.Artifact{}, err
	}
	if replay != nil {
		return *replay, nil
	}
	if publication.State == "failed" {
		return contracts.Artifact{}, fmt.Errorf("artifact publication is terminally failed")
	}
	if _, err := s.repository.MarkArtifactPublicationUploading(
		ctx, command.Intent, command.Metadata,
	); err != nil {
		return contracts.Artifact{}, err
	}
	descriptor, err := objectDescriptor(command.Intent)
	if err != nil {
		return contracts.Artifact{}, err
	}
	authority := s.repository.Authority()
	evidence, publishErr := s.bodies.Publish(ctx, objectstore.Authority{
		TenantID: authority.TenantID, RepositoryID: authority.RepositoryID,
	}, descriptor, command.Body)
	if publishErr != nil {
		failureClass := "interrupted"
		switch {
		case errors.Is(publishErr, objectstore.ErrIntegrity):
			failureClass = "integrity"
		case errors.Is(publishErr, objectstore.ErrUnavailable), errors.Is(publishErr, objectstore.ErrNotFound):
			failureClass = "retryable_provider"
		}
		if _, persistErr := s.repository.FailArtifactPublication(
			ctx, command.Intent, failureClass, command.Metadata,
		); persistErr != nil {
			return contracts.Artifact{}, fmt.Errorf("publish body: %w; persist failure: %v", publishErr, persistErr)
		}
		return contracts.Artifact{}, publishErr
	}
	return s.repository.CompleteArtifactPublication(
		ctx,
		command.Intent,
		persistence.ArtifactEvidence{
			ObjectKey: evidence.ObjectKey, ETag: evidence.ETag,
			VersionID:              evidence.VersionID,
			ProviderChecksumSHA256: evidence.ProviderChecksumSHA256,
		},
		command.Metadata,
	)
}

func objectDescriptor(intent persistence.ArtifactPublicationIntent) (objectstore.Descriptor, error) {
	digest, err := hex.DecodeString(strings.TrimPrefix(intent.ContentHash, "sha256:"))
	if err != nil || len(digest) != sha256.Size {
		return objectstore.Descriptor{}, fmt.Errorf("artifact content hash is invalid")
	}
	var value [sha256.Size]byte
	copy(value[:], digest)
	return objectstore.Descriptor{
		SHA256: value, SizeBytes: intent.SizeBytes, MediaType: intent.MediaType,
	}, nil
}
