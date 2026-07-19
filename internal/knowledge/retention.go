package knowledge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/objectstore"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

type RetentionRepository interface {
	persistence.ArtifactRetentionRepository
	Authority() control.Authority
}

type BodyPurger interface {
	Delete(context.Context, objectstore.Authority, objectstore.Descriptor, string) error
}

type RetentionReport struct {
	Examined int
	Purged   int
	Deferred int
}

type RetentionService struct {
	repository RetentionRepository
	bodies     BodyPurger
}

func NewRetentionService(repository RetentionRepository, bodies BodyPurger) (*RetentionService, error) {
	if repository == nil || bodies == nil {
		return nil, fmt.Errorf("retention repository and body purger are required")
	}
	return &RetentionService{repository: repository, bodies: bodies}, nil
}

func (s *RetentionService) Purge(
	ctx context.Context,
	tombstonedBefore time.Time,
	limit int,
	metadata runstate.CommandMetadata,
) (RetentionReport, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return RetentionReport{}, err
	}
	if metadata.ActorType != "system" {
		return RetentionReport{}, fmt.Errorf("physical purge requires system authority")
	}
	candidates, err := s.repository.ListArtifactRetentionCandidates(ctx, tombstonedBefore, limit)
	if err != nil {
		return RetentionReport{}, err
	}
	authority := s.repository.Authority()
	report := RetentionReport{Examined: len(candidates)}
	for _, candidate := range candidates {
		descriptor, descriptorErr := objectDescriptor(persistence.ArtifactPublicationIntent{
			ContentHash: candidate.ContentHash,
			SizeBytes:   candidate.SizeBytes,
			MediaType:   candidate.MediaType,
		})
		if descriptorErr != nil {
			report.Deferred++
			continue
		}
		deleteErr := s.bodies.Delete(ctx, objectstore.Authority{
			TenantID: authority.TenantID, RepositoryID: authority.RepositoryID,
		}, descriptor, candidate.ETag)
		if deleteErr != nil && !errors.Is(deleteErr, objectstore.ErrNotFound) {
			report.Deferred++
			continue
		}
		itemMetadata := reconciliationMetadata(metadata, candidate.ContentHash)
		if err := s.repository.MarkArtifactObjectPurged(
			ctx, candidate.ContentHash, candidate.ETag, itemMetadata,
		); err != nil {
			report.Deferred++
			continue
		}
		report.Purged++
	}
	return report, nil
}
