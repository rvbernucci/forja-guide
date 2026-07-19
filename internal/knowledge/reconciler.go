package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/objectstore"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

type ReconciliationRepository interface {
	persistence.ArtifactReconciliationRepository
	Authority() control.Authority
}

type BodyVerifier interface {
	Verify(context.Context, objectstore.Authority, objectstore.Descriptor) (objectstore.Evidence, error)
}

type ReconciliationResult struct {
	OperationID string
	Outcome     string
	Failure     string
}

type ReconciliationReport struct {
	Examined  int
	Completed int
	Retryable int
	Terminal  int
	Results   []ReconciliationResult
}

type Reconciler struct {
	repository ReconciliationRepository
	bodies     BodyVerifier
}

func NewReconciler(repository ReconciliationRepository, bodies BodyVerifier) (*Reconciler, error) {
	if repository == nil || bodies == nil {
		return nil, fmt.Errorf("artifact reconciliation repository and body verifier are required")
	}
	return &Reconciler{repository: repository, bodies: bodies}, nil
}

func (r *Reconciler) Reconcile(
	ctx context.Context,
	staleBefore time.Time,
	limit int,
	metadata runstate.CommandMetadata,
) (ReconciliationReport, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return ReconciliationReport{}, err
	}
	if metadata.ActorType != "system" {
		return ReconciliationReport{}, fmt.Errorf("artifact reconciliation requires system authority")
	}
	candidates, err := r.repository.ListArtifactReconciliationCandidates(ctx, staleBefore, limit)
	if err != nil {
		return ReconciliationReport{}, err
	}
	authority := r.repository.Authority()
	report := ReconciliationReport{
		Examined: len(candidates),
		Results:  make([]ReconciliationResult, 0, len(candidates)),
	}
	for _, candidate := range candidates {
		result := ReconciliationResult{OperationID: candidate.Publication.Intent.OperationID}
		itemMetadata := reconciliationMetadata(metadata, result.OperationID)
		descriptor, descriptorErr := objectDescriptor(candidate.Publication.Intent)
		if descriptorErr != nil {
			if _, failErr := r.repository.FailArtifactReconciliation(
				ctx, result.OperationID, "canonical_conflict", itemMetadata,
			); failErr != nil {
				result.Outcome = "retryable"
				result.Failure = "canonical_persistence"
				report.Retryable++
			} else {
				result.Outcome = "terminal"
				result.Failure = "canonical_conflict"
				report.Terminal++
			}
			report.Results = append(report.Results, result)
			continue
		}
		evidence, verifyErr := r.bodies.Verify(ctx, objectstore.Authority{
			TenantID: authority.TenantID, RepositoryID: authority.RepositoryID,
		}, descriptor)
		if verifyErr == nil && candidate.ExpectedETag != "" && candidate.ExpectedETag != evidence.ETag {
			verifyErr = objectstore.ErrIntegrity
		}
		if verifyErr == nil {
			_, completeErr := r.repository.CompleteArtifactReconciliation(
				ctx, result.OperationID, persistence.ArtifactEvidence{
					ObjectKey: evidence.ObjectKey, ETag: evidence.ETag,
					VersionID:              evidence.VersionID,
					ProviderChecksumSHA256: evidence.ProviderChecksumSHA256,
				}, itemMetadata,
			)
			if completeErr == nil {
				result.Outcome = "completed"
				report.Completed++
				report.Results = append(report.Results, result)
				continue
			}
			verifyErr = completeErr
		}
		failureClass := "interrupted"
		result.Outcome = "retryable"
		switch {
		case errors.Is(verifyErr, objectstore.ErrIntegrity):
			failureClass = "integrity"
			result.Outcome = "terminal"
		case errors.Is(verifyErr, objectstore.ErrUnavailable):
			failureClass = "retryable_provider"
		case errors.Is(verifyErr, objectstore.ErrNotFound):
			failureClass = "interrupted"
		}
		result.Failure = failureClass
		if _, failErr := r.repository.FailArtifactReconciliation(
			ctx, result.OperationID, failureClass, itemMetadata,
		); failErr != nil {
			result.Outcome = "retryable"
			result.Failure = "canonical_persistence"
		}
		if result.Outcome == "terminal" {
			report.Terminal++
		} else {
			report.Retryable++
		}
		report.Results = append(report.Results, result)
	}
	return report, nil
}

func reconciliationMetadata(
	base runstate.CommandMetadata,
	operationID string,
) runstate.CommandMetadata {
	digest := sha256.Sum256([]byte(operationID))
	suffix := hex.EncodeToString(digest[:8])
	maximumBase := 200 - 1 - len(suffix)
	if len(base.IdempotencyKey) > maximumBase {
		base.IdempotencyKey = base.IdempotencyKey[:maximumBase]
	}
	base.IdempotencyKey += ":" + suffix
	return base
}
