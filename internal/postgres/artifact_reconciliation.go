package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

func (s *Store) ListArtifactReconciliationCandidates(
	ctx context.Context,
	staleBefore time.Time,
	limit int,
) ([]persistence.ArtifactReconciliationCandidate, error) {
	if staleBefore.IsZero() || limit < 1 || limit > 500 {
		return nil, fault.New(fault.CodeInvalidArgument, "postgres.ListArtifactReconciliationCandidates", "reconciliation query is invalid")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT operation.operation_id, operation.artifact_id,
		       encode(operation.content_sha256, 'hex'), operation.expected_size_bytes,
		       operation.expected_media_type, operation.created_by, operation.intent,
		       operation.state, operation.version, operation.created_at,
		       operation.updated_at, object.provider_etag
		FROM forja.artifact_operations AS operation
		JOIN forja.artifact_objects AS object
		  ON object.tenant_id=operation.tenant_id
		 AND object.repository_id=operation.repository_id
		 AND object.content_sha256=operation.content_sha256
		WHERE operation.tenant_id=$1 AND operation.repository_id=$2
		  AND operation.state IN ('reserved', 'uploading', 'verified', 'reconciliation_required')
		  AND operation.updated_at <= $3
		ORDER BY operation.updated_at, operation.operation_id
		LIMIT $4`, s.tenantID, s.repositoryID, staleBefore.UTC(), limit)
	if err != nil {
		return nil, databaseError("postgres.ListArtifactReconciliationCandidates", err)
	}
	defer rows.Close()
	result := make([]persistence.ArtifactReconciliationCandidate, 0, limit)
	for rows.Next() {
		var candidate persistence.ArtifactReconciliationCandidate
		var digestHex string
		var intentJSON []byte
		var expectedETag *string
		publication := &candidate.Publication
		if err := rows.Scan(
			&publication.Intent.OperationID, &publication.Intent.ArtifactID,
			&digestHex, &publication.Intent.SizeBytes, &publication.Intent.MediaType,
			&publication.Intent.CreatedBy, &intentJSON, &publication.State,
			&publication.Version, &publication.CreatedAt, &publication.UpdatedAt,
			&expectedETag,
		); err != nil {
			return nil, databaseError("postgres.ListArtifactReconciliationCandidates.scan", err)
		}
		if err := hydrateStoredArtifactIntent(&publication.Intent, intentJSON, digestHex); err != nil {
			return nil, fault.Wrap(fault.CodeInternal, "postgres.ListArtifactReconciliationCandidates", "stored artifact intent is invalid", err)
		}
		publication.CreatedAt = publication.CreatedAt.UTC()
		publication.UpdatedAt = publication.UpdatedAt.UTC()
		if expectedETag != nil {
			candidate.ExpectedETag = *expectedETag
		}
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError("postgres.ListArtifactReconciliationCandidates.rows", err)
	}
	return result, nil
}

func (s *Store) CompleteArtifactReconciliation(
	ctx context.Context,
	operationID string,
	evidence persistence.ArtifactEvidence,
	metadata runstate.CommandMetadata,
) (contracts.Artifact, error) {
	if err := validateReconciliationCommand(operationID, metadata); err != nil {
		return contracts.Artifact{}, err
	}
	scope := "artifact_reconcile_complete:" + s.repositoryID + ":" + operationID
	requestHash := hashKnowledgeCommand(metadata, operationID)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contracts.Artifact{}, databaseError("postgres.CompleteArtifactReconciliation.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return contracts.Artifact{}, err
	}
	if replay, found, err := loadControlReplay[contracts.Artifact](ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return contracts.Artifact{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return contracts.Artifact{}, databaseError("postgres.CompleteArtifactReconciliation.replay", err)
		}
		return replay, nil
	}
	publication, found, err := loadArtifactPublicationByOperation(ctx, tx, s.tenantID, s.repositoryID, operationID, true)
	if err != nil {
		return contracts.Artifact{}, err
	}
	if !found {
		return contracts.Artifact{}, fault.New(fault.CodeNotFound, "postgres.CompleteArtifactReconciliation", "artifact operation was not found")
	}
	digest, err := decodeContentHash(publication.Intent.ContentHash)
	if err != nil {
		return contracts.Artifact{}, fault.Wrap(fault.CodeInternal, "postgres.CompleteArtifactReconciliation", "stored content hash is invalid", err)
	}
	if evidence.ObjectKey != artifactObjectKey(s.tenantID, s.repositoryID, digest) || strings.TrimSpace(evidence.ETag) == "" {
		return contracts.Artifact{}, fault.New(fault.CodeInvalidArgument, "postgres.CompleteArtifactReconciliation", "provider evidence is invalid")
	}
	var storedETag *string
	if err := tx.QueryRow(ctx, `
		SELECT provider_etag FROM forja.artifact_objects
		WHERE tenant_id=$1 AND repository_id=$2 AND content_sha256=$3
		FOR UPDATE`, s.tenantID, s.repositoryID, digest).Scan(&storedETag); err != nil {
		return contracts.Artifact{}, databaseError("postgres.CompleteArtifactReconciliation.object", err)
	}
	if storedETag != nil && *storedETag != evidence.ETag {
		return contracts.Artifact{}, fault.New(fault.CodeConflict, "postgres.CompleteArtifactReconciliation", "provider ETag changed across reconciliation")
	}
	if publication.State == "active" {
		artifact, err := loadArtifact(ctx, tx, s.tenantID, s.repositoryID, publication.Intent.ArtifactID)
		if err != nil {
			return contracts.Artifact{}, err
		}
		if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 200, artifact); err != nil {
			return contracts.Artifact{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return contracts.Artifact{}, databaseError("postgres.CompleteArtifactReconciliation.existing", err)
		}
		return artifact, nil
	}
	if publication.State == "failed" {
		return contracts.Artifact{}, fault.New(fault.CodeConflict, "postgres.CompleteArtifactReconciliation", "terminal artifact publication cannot be reconciled")
	}
	now := postgresTimestamp(s.clock.Now())
	objectResult, err := tx.Exec(ctx, `
		UPDATE forja.artifact_objects
		SET state='active', provider_checksum_sha256=NULLIF($4, ''),
			provider_etag=$5, provider_version=NULLIF($6, ''), failure_class=NULL,
			verified_at=COALESCE(verified_at, $7), activated_at=$7, updated_at=$7
		WHERE tenant_id=$1 AND repository_id=$2 AND content_sha256=$3
		  AND state IN ('reserved', 'uploading', 'verified', 'failed', 'active')`,
		s.tenantID, s.repositoryID, digest, evidence.ProviderChecksumSHA256,
		evidence.ETag, evidence.VersionID, now,
	)
	if err != nil {
		return contracts.Artifact{}, databaseError("postgres.CompleteArtifactReconciliation.activateObject", err)
	}
	if objectResult.RowsAffected() != 1 {
		return contracts.Artifact{}, fault.New(fault.CodeConflict, "postgres.CompleteArtifactReconciliation", "content object cannot be activated from its lifecycle state")
	}
	publication.State = "active"
	publication.Version++
	publication.UpdatedAt = now
	if _, err := tx.Exec(ctx, `
		UPDATE forja.artifact_operations
		SET state='active', version=$4, failure_class=NULL, updated_at=$5
		WHERE tenant_id=$1 AND repository_id=$2 AND operation_id=$3`,
		s.tenantID, s.repositoryID, operationID, publication.Version, now,
	); err != nil {
		return contracts.Artifact{}, databaseError("postgres.CompleteArtifactReconciliation.activateOperation", err)
	}
	artifact, err := insertReconciledArtifact(ctx, tx, s.tenantID, s.repositoryID, publication.Intent, publication.CreatedAt)
	if err != nil {
		return contracts.Artifact{}, err
	}
	if err := s.appendArtifactPublicationEvent(ctx, tx, "artifact.publication_reconciled", publication, metadata); err != nil {
		return contracts.Artifact{}, err
	}
	encoded, _ := json.Marshal(artifact)
	if err := s.appendEvent(ctx, tx, "artifact", artifact.ArtifactID, 1, "artifact.activated", now, encoded, metadata); err != nil {
		return contracts.Artifact{}, err
	}
	if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 200, artifact); err != nil {
		return contracts.Artifact{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.Artifact{}, databaseError("postgres.CompleteArtifactReconciliation.commit", err)
	}
	return artifact, nil
}

func (s *Store) FailArtifactReconciliation(
	ctx context.Context,
	operationID string,
	failureClass string,
	metadata runstate.CommandMetadata,
) (persistence.ArtifactPublication, error) {
	if err := validateReconciliationCommand(operationID, metadata); err != nil {
		return persistence.ArtifactPublication{}, err
	}
	if failureClass != "retryable_provider" && failureClass != "integrity" && failureClass != "interrupted" {
		return persistence.ArtifactPublication{}, fault.New(fault.CodeInvalidArgument, "postgres.FailArtifactReconciliation", "failure class is invalid")
	}
	scope := "artifact_reconcile_fail:" + s.repositoryID + ":" + operationID + ":" + failureClass
	requestHash := hashKnowledgeCommand(metadata, operationID, failureClass)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return persistence.ArtifactPublication{}, databaseError("postgres.FailArtifactReconciliation.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return persistence.ArtifactPublication{}, err
	}
	if replay, found, err := loadControlReplay[persistence.ArtifactPublication](ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return persistence.ArtifactPublication{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return persistence.ArtifactPublication{}, databaseError("postgres.FailArtifactReconciliation.replay", err)
		}
		return replay, nil
	}
	publication, found, err := loadArtifactPublicationByOperation(ctx, tx, s.tenantID, s.repositoryID, operationID, true)
	if err != nil {
		return persistence.ArtifactPublication{}, err
	}
	if !found {
		return persistence.ArtifactPublication{}, fault.New(fault.CodeNotFound, "postgres.FailArtifactReconciliation", "artifact operation was not found")
	}
	if publication.State == "active" {
		return persistence.ArtifactPublication{}, fault.New(fault.CodeConflict, "postgres.FailArtifactReconciliation", "active artifact publication cannot be failed")
	}
	digest, _ := decodeContentHash(publication.Intent.ContentHash)
	nextState := "reconciliation_required"
	if failureClass == "integrity" {
		nextState = "failed"
	}
	now := postgresTimestamp(s.clock.Now())
	publication.State = nextState
	publication.Version++
	publication.UpdatedAt = now
	if _, err := tx.Exec(ctx, `
		UPDATE forja.artifact_operations
		SET state=$4, version=$5, failure_class=$6, updated_at=$7
		WHERE tenant_id=$1 AND repository_id=$2 AND operation_id=$3`,
		s.tenantID, s.repositoryID, operationID, nextState, publication.Version,
		failureClass, now,
	); err != nil {
		return persistence.ArtifactPublication{}, databaseError("postgres.FailArtifactReconciliation.operation", err)
	}
	objectResult, err := tx.Exec(ctx, `
		UPDATE forja.artifact_objects
		SET state='failed', failure_class=$4, updated_at=$5
		WHERE tenant_id=$1 AND repository_id=$2 AND content_sha256=$3
		  AND state IN ('reserved', 'uploading', 'verified', 'failed')`,
		s.tenantID, s.repositoryID, digest, failureClass, now,
	)
	if err != nil {
		return persistence.ArtifactPublication{}, databaseError("postgres.FailArtifactReconciliation.object", err)
	}
	if objectResult.RowsAffected() != 1 {
		return persistence.ArtifactPublication{}, fault.New(fault.CodeConflict, "postgres.FailArtifactReconciliation", "content object cannot enter failed state from its lifecycle state")
	}
	payload, _ := json.Marshal(struct {
		Publication  persistence.ArtifactPublication `json:"publication"`
		FailureClass string                          `json:"failure_class"`
	}{Publication: publication, FailureClass: failureClass})
	if err := s.appendEvent(
		ctx, tx, "artifact_operation", publication.Intent.OperationID,
		publication.Version, "artifact.publication_reconciliation_failed",
		now, payload, metadata,
	); err != nil {
		return persistence.ArtifactPublication{}, err
	}
	if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 200, publication); err != nil {
		return persistence.ArtifactPublication{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return persistence.ArtifactPublication{}, databaseError("postgres.FailArtifactReconciliation.commit", err)
	}
	return publication, nil
}

func loadArtifactPublicationByOperation(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, repositoryID, operationID string,
	forUpdate bool,
) (persistence.ArtifactPublication, bool, error) {
	query := `
		SELECT operation_id, artifact_id, encode(content_sha256, 'hex'),
		       expected_size_bytes, expected_media_type, created_by, intent,
		       state, version, created_at, updated_at
		FROM forja.artifact_operations
		WHERE tenant_id=$1 AND repository_id=$2 AND operation_id=$3`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var publication persistence.ArtifactPublication
	var digestHex string
	var intentJSON []byte
	err := tx.QueryRow(ctx, query, tenantID, repositoryID, operationID).Scan(
		&publication.Intent.OperationID, &publication.Intent.ArtifactID, &digestHex,
		&publication.Intent.SizeBytes, &publication.Intent.MediaType,
		&publication.Intent.CreatedBy, &intentJSON, &publication.State,
		&publication.Version, &publication.CreatedAt, &publication.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistence.ArtifactPublication{}, false, nil
	}
	if err != nil {
		return persistence.ArtifactPublication{}, false, databaseError("postgres.loadArtifactPublicationByOperation", err)
	}
	if err := hydrateStoredArtifactIntent(&publication.Intent, intentJSON, digestHex); err != nil {
		return persistence.ArtifactPublication{}, false, fault.Wrap(fault.CodeInternal, "postgres.loadArtifactPublicationByOperation", "stored intent is invalid", err)
	}
	publication.CreatedAt = publication.CreatedAt.UTC()
	publication.UpdatedAt = publication.UpdatedAt.UTC()
	return publication, true, nil
}

func hydrateStoredArtifactIntent(intent *persistence.ArtifactPublicationIntent, encoded []byte, digestHex string) error {
	operationID := intent.OperationID
	artifactID := intent.ArtifactID
	size := intent.SizeBytes
	mediaType := intent.MediaType
	createdBy := intent.CreatedBy
	if len(encoded) <= 2 {
		return fmt.Errorf("artifact operation has no recoverable intent")
	}
	if err := json.Unmarshal(encoded, intent); err != nil {
		return err
	}
	if intent.OperationID != operationID || intent.ArtifactID != artifactID ||
		intent.ContentHash != "sha256:"+digestHex || intent.SizeBytes != size ||
		intent.MediaType != mediaType || intent.CreatedBy != createdBy {
		return fmt.Errorf("artifact operation intent differs from indexed authority")
	}
	return nil
}

func validateReconciliationCommand(operationID string, metadata runstate.CommandMetadata) error {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return err
	}
	if metadata.ActorType != "system" || !artifactOperationIDPattern.MatchString(operationID) {
		return fault.New(fault.CodePermissionDenied, "postgres.validateReconciliationCommand", "artifact reconciliation requires system authority")
	}
	return nil
}

func insertReconciledArtifact(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, repositoryID string,
	intent persistence.ArtifactPublicationIntent,
	createdAt time.Time,
) (contracts.Artifact, error) {
	digest, _ := decodeContentHash(intent.ContentHash)
	provenance, _ := json.Marshal(intent.Provenance)
	metadata := intent.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadataJSON, _ := json.Marshal(metadata)
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.artifacts (
			tenant_id, repository_id, artifact_id, operation_id, run_id, kind,
			status, version, content_sha256, media_type, size_bytes, created_by,
			provenance, metadata, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'active', 1, $7, $8, $9, $10, $11, $12, $13, $13)
		ON CONFLICT (tenant_id, repository_id, artifact_id) DO NOTHING`,
		tenantID, repositoryID, intent.ArtifactID, intent.OperationID, intent.RunID,
		intent.Kind, digest, intent.MediaType, intent.SizeBytes, intent.CreatedBy,
		provenance, metadataJSON, createdAt,
	); err != nil {
		return contracts.Artifact{}, databaseError("postgres.insertReconciledArtifact", err)
	}
	artifact, err := loadArtifact(ctx, tx, tenantID, repositoryID, intent.ArtifactID)
	if err != nil {
		return contracts.Artifact{}, err
	}
	var exact bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM forja.artifacts
			WHERE tenant_id=$1 AND repository_id=$2 AND artifact_id=$3
			  AND operation_id=$4 AND content_sha256=$5
		)`, tenantID, repositoryID, intent.ArtifactID, intent.OperationID, digest).Scan(&exact); err != nil {
		return contracts.Artifact{}, databaseError("postgres.insertReconciledArtifact.verify", err)
	}
	if !exact {
		return contracts.Artifact{}, fault.New(fault.CodeConflict, "postgres.insertReconciledArtifact", "existing artifact differs from reconciled intent")
	}
	return artifact, nil
}

var _ persistence.ArtifactReconciliationRepository = (*Store)(nil)
