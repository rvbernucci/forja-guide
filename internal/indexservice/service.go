// Package indexservice coordinates immutable artifact and canonical index publication.
package indexservice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/rvbernucci/forja-guide/internal/clock"
	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/indexing"
	"github.com/rvbernucci/forja-guide/internal/knowledge"
	"github.com/rvbernucci/forja-guide/internal/observability"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

type ArtifactPublisher interface {
	PublishArtifact(context.Context, knowledge.PublishArtifactCommand) (contracts.Artifact, error)
}

type Repository interface {
	persistence.IndexRepository
}

type Service struct {
	artifacts  ArtifactPublisher
	repository Repository
	clock      clock.Clock
	observer   *observability.Observer
}

type PublishCommand struct {
	Bundle        indexing.IndexBundle
	AdapterRuns   []persistence.IndexAdapterRun
	Deltas        []persistence.IndexDelta
	Invalidations []persistence.IndexInvalidation
	Metadata      runstate.CommandMetadata
}

type Option func(*Service)

func WithObserver(observer *observability.Observer) Option {
	return func(service *Service) { service.observer = observer }
}

func New(artifacts ArtifactPublisher, repository Repository, source clock.Clock, options ...Option) (*Service, error) {
	if artifacts == nil || repository == nil {
		return nil, fmt.Errorf("artifact publisher and index repository are required")
	}
	if source == nil {
		source = clock.Real{}
	}
	service := &Service{artifacts: artifacts, repository: repository, clock: source}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service, nil
}

func (s *Service) Publish(ctx context.Context, command PublishCommand) (result contracts.RepositorySnapshot, err error) {
	ctx, handle := s.observer.Start(ctx, observability.BoundaryIndexing, observability.OperationPublishIndex)
	defer func() { handle.End(err) }()
	body, err := indexing.MarshalCanonicalBundle(command.Bundle)
	if err != nil {
		return contracts.RepositorySnapshot{}, err
	}
	digest := sha256.Sum256(body)
	contentHash := "sha256:" + hex.EncodeToString(digest[:])
	snapshotID := command.Bundle.Snapshot.SnapshotID
	artifactID := "artifact_index_" + strings.TrimPrefix(snapshotID, "snapshot_")
	operationID := deterministicOperationID(snapshotID)
	commit := command.Bundle.Snapshot.SourceCommit
	sourceRefs := make([]string, 0, len(command.Bundle.Snapshot.Adapters)+1)
	sourceRefs = append(sourceRefs, "git:"+commit)
	for _, adapter := range command.Bundle.Snapshot.Adapters {
		sourceRefs = append(sourceRefs, "adapter:"+adapter.Name+"@"+adapter.Version+":"+adapter.CapabilityHash)
	}
	artifact, err := s.artifacts.PublishArtifact(ctx, knowledge.PublishArtifactCommand{
		Intent: persistence.ArtifactPublicationIntent{
			OperationID: operationID, ArtifactID: artifactID, Kind: "index_snapshot",
			ContentHash: contentHash, SizeBytes: int64(len(body)),
			MediaType: indexing.IndexArtifactMediaType, CreatedBy: command.Metadata.ActorID,
			Provenance: contracts.Provenance{
				SourceType: "compiler", SourceRefs: sourceRefs, SourceCommit: &commit,
			},
			Metadata: map[string]any{"snapshot_id": snapshotID, "schema_version": contracts.IndexSchemaVersion},
		},
		Metadata: command.Metadata,
		Body:     bytes.NewReader(body),
	})
	if err != nil {
		return contracts.RepositorySnapshot{}, err
	}
	if artifact.ArtifactID != artifactID || artifact.ContentHash != contentHash || artifact.Kind != "index_snapshot" ||
		(artifact.Status != "active" && artifact.Status != "validated") {
		return contracts.RepositorySnapshot{}, fmt.Errorf("artifact publisher returned mismatched authority")
	}
	validatedAt := s.clock.Now().UTC().Truncate(time.Microsecond)
	publication := persistence.IndexPublication{
		Bundle: command.Bundle, AdapterRuns: command.AdapterRuns,
		Deltas: command.Deltas, Invalidations: command.Invalidations,
	}
	publication.Bundle.Snapshot.Status = "active"
	publication.Bundle.Snapshot.ArtifactID = &artifactID
	publication.Bundle.Snapshot.ArtifactContentHash = &contentHash
	publication.Bundle.Snapshot.ValidatedAt = &validatedAt
	result, err = s.repository.PublishIndexSnapshot(ctx, publication, command.Metadata)
	if err == nil {
		invalidations := make(map[string]int)
		for _, invalidation := range command.Invalidations {
			invalidations[invalidation.Reason]++
		}
		reused := 0
		for _, delta := range command.Deltas {
			if delta.ChangeKind == "reused" {
				reused++
			}
		}
		s.observer.RecordIndexStats(ctx, observability.IndexStats{
			Files:       command.Bundle.Snapshot.Counts.Files,
			Symbols:     command.Bundle.Snapshot.Counts.Symbols,
			Relations:   command.Bundle.Snapshot.Counts.Relations,
			Diagnostics: command.Bundle.Snapshot.Counts.Diagnostics,
			Reused:      reused, Invalidations: invalidations,
		})
	}
	return result, err
}

func deterministicOperationID(snapshotID string) string {
	digest := sha256.Sum256([]byte("forja-index-artifact-operation-v1\x00" + snapshotID))
	digest[6] = digest[6]&0x0f | 0x40
	digest[8] = digest[8]&0x3f | 0x80
	encoded := hex.EncodeToString(digest[:16])
	return "artifact_operation_" + encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
