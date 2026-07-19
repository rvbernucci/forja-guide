package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

var (
	artifactOperationIDPattern = regexp.MustCompile(`^artifact_operation_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	artifactPublicIDPattern    = regexp.MustCompile(`^artifact_[A-Za-z0-9_-]+$`)
	artifactRunIDPattern       = regexp.MustCompile(`^run_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	artifactCommitPattern      = regexp.MustCompile(`^[a-f0-9]{7,64}$`)
)

func (s *Store) PrepareArtifactPublication(
	ctx context.Context,
	intent persistence.ArtifactPublicationIntent,
	metadata runstate.CommandMetadata,
) (persistence.ArtifactPublication, *contracts.Artifact, error) {
	requestHash, digest, err := validateArtifactIntent(intent, metadata)
	if err != nil {
		return persistence.ArtifactPublication{}, nil, err
	}
	scope := artifactPublicationScope(s.repositoryID, intent.ArtifactID)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return persistence.ArtifactPublication{}, nil, databaseError("postgres.PrepareArtifactPublication.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return persistence.ArtifactPublication{}, nil, err
	}
	if replay, found, err := loadControlReplay[contracts.Artifact](
		ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash,
	); err != nil {
		return persistence.ArtifactPublication{}, nil, err
	} else if found {
		publication := persistence.ArtifactPublication{
			Intent: intent, State: "active", Version: 1,
			CreatedAt: replay.CreatedAt, UpdatedAt: replay.CreatedAt,
		}
		if err := tx.Commit(ctx); err != nil {
			return persistence.ArtifactPublication{}, nil, databaseError("postgres.PrepareArtifactPublication.replay", err)
		}
		return publication, &replay, nil
	}
	publication, storedHash, found, err := loadArtifactPublicationByArtifact(ctx, tx, s.tenantID, s.repositoryID, intent.ArtifactID, true)
	if err != nil {
		return persistence.ArtifactPublication{}, nil, err
	}
	if found {
		if !bytes.Equal(storedHash, requestHash) || publication.Intent.OperationID != intent.OperationID {
			return persistence.ArtifactPublication{}, nil, fault.New(
				fault.CodeConflict, "postgres.PrepareArtifactPublication", "artifact ID was reserved by a different command",
			)
		}
		if err := tx.Commit(ctx); err != nil {
			return persistence.ArtifactPublication{}, nil, databaseError("postgres.PrepareArtifactPublication.existing", err)
		}
		return publication, nil, nil
	}
	now := postgresTimestamp(s.clock.Now())
	objectKey := artifactObjectKey(s.tenantID, s.repositoryID, digest)
	intentJSON, err := json.Marshal(intent)
	if err != nil {
		return persistence.ArtifactPublication{}, nil, fault.Wrap(fault.CodeInvalidArgument, "postgres.PrepareArtifactPublication", "encode artifact intent", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.artifact_objects (
			tenant_id, repository_id, content_sha256, object_key, size_bytes,
			media_type, state, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'reserved', $7, $7)
		ON CONFLICT (tenant_id, repository_id, content_sha256) DO NOTHING`,
		s.tenantID, s.repositoryID, digest, objectKey, intent.SizeBytes, intent.MediaType, now,
	); err != nil {
		return persistence.ArtifactPublication{}, nil, databaseError("postgres.PrepareArtifactPublication.reserveObject", err)
	}
	var storedKey, storedMediaType string
	var storedSize int64
	if err := tx.QueryRow(ctx, `
		SELECT object_key, size_bytes, media_type
		FROM forja.artifact_objects
		WHERE tenant_id=$1 AND repository_id=$2 AND content_sha256=$3
		FOR UPDATE`, s.tenantID, s.repositoryID, digest,
	).Scan(&storedKey, &storedSize, &storedMediaType); err != nil {
		return persistence.ArtifactPublication{}, nil, databaseError("postgres.PrepareArtifactPublication.verifyObject", err)
	}
	if storedKey != objectKey || storedSize != intent.SizeBytes || storedMediaType != intent.MediaType {
		return persistence.ArtifactPublication{}, nil, fault.New(
			fault.CodeConflict, "postgres.PrepareArtifactPublication", "content hash is bound to different metadata",
		)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.artifact_operations (
			tenant_id, repository_id, operation_id, artifact_id, content_sha256,
			expected_size_bytes, expected_media_type, request_sha256, intent, state,
			version, created_by, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'reserved', 1, $10, $11, $11)`,
		s.tenantID, s.repositoryID, intent.OperationID, intent.ArtifactID, digest,
		intent.SizeBytes, intent.MediaType, requestHash, intentJSON, intent.CreatedBy, now,
	); err != nil {
		return persistence.ArtifactPublication{}, nil, databaseError("postgres.PrepareArtifactPublication.insert", err)
	}
	publication = persistence.ArtifactPublication{Intent: intent, State: "reserved", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.appendArtifactPublicationEvent(ctx, tx, "artifact.publication_reserved", publication, metadata); err != nil {
		return persistence.ArtifactPublication{}, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return persistence.ArtifactPublication{}, nil, databaseError("postgres.PrepareArtifactPublication.commit", err)
	}
	return publication, nil, nil
}

func (s *Store) MarkArtifactPublicationUploading(
	ctx context.Context,
	intent persistence.ArtifactPublicationIntent,
	metadata runstate.CommandMetadata,
) (persistence.ArtifactPublication, error) {
	requestHash, digest, err := validateArtifactIntent(intent, metadata)
	if err != nil {
		return persistence.ArtifactPublication{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return persistence.ArtifactPublication{}, databaseError("postgres.MarkArtifactPublicationUploading.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, artifactPublicationScope(s.repositoryID, intent.ArtifactID), metadata.IdempotencyKey); err != nil {
		return persistence.ArtifactPublication{}, err
	}
	publication, storedHash, found, err := loadArtifactPublicationByArtifact(ctx, tx, s.tenantID, s.repositoryID, intent.ArtifactID, true)
	if err != nil {
		return persistence.ArtifactPublication{}, err
	}
	if !found || !bytes.Equal(storedHash, requestHash) || publication.Intent.OperationID != intent.OperationID {
		return persistence.ArtifactPublication{}, fault.New(fault.CodeConflict, "postgres.MarkArtifactPublicationUploading", "artifact publication intent does not match its reservation")
	}
	if publication.State == "active" || publication.State == "uploading" {
		if err := tx.Commit(ctx); err != nil {
			return persistence.ArtifactPublication{}, databaseError("postgres.MarkArtifactPublicationUploading.existing", err)
		}
		return publication, nil
	}
	var failureClass *string
	if err := tx.QueryRow(ctx, `
		SELECT failure_class FROM forja.artifact_operations
		WHERE tenant_id=$1 AND repository_id=$2 AND operation_id=$3`,
		s.tenantID, s.repositoryID, intent.OperationID,
	).Scan(&failureClass); err != nil {
		return persistence.ArtifactPublication{}, databaseError("postgres.MarkArtifactPublicationUploading.failure", err)
	}
	if publication.State != "reserved" && publication.State != "reconciliation_required" &&
		!(publication.State == "failed" && failureClass != nil && *failureClass == "retryable_provider") {
		return persistence.ArtifactPublication{}, fault.New(fault.CodeConflict, "postgres.MarkArtifactPublicationUploading", "artifact publication is not retryable")
	}
	now := postgresTimestamp(s.clock.Now())
	publication.State = "uploading"
	publication.Version++
	publication.UpdatedAt = now
	if _, err := tx.Exec(ctx, `
		UPDATE forja.artifact_operations
		SET state='uploading', version=$4, failure_class=NULL, updated_at=$5
		WHERE tenant_id=$1 AND repository_id=$2 AND operation_id=$3`,
		s.tenantID, s.repositoryID, intent.OperationID, publication.Version, now,
	); err != nil {
		return persistence.ArtifactPublication{}, databaseError("postgres.MarkArtifactPublicationUploading.operation", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE forja.artifact_objects
		SET state=CASE WHEN verified_at IS NULL THEN 'uploading' ELSE 'verified' END,
			failure_class=NULL, updated_at=$4
		WHERE tenant_id=$1 AND repository_id=$2 AND content_sha256=$3
		  AND state IN ('reserved', 'failed')`,
		s.tenantID, s.repositoryID, digest, now,
	); err != nil {
		return persistence.ArtifactPublication{}, databaseError("postgres.MarkArtifactPublicationUploading.object", err)
	}
	if err := s.appendArtifactPublicationEvent(ctx, tx, "artifact.publication_uploading", publication, metadata); err != nil {
		return persistence.ArtifactPublication{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return persistence.ArtifactPublication{}, databaseError("postgres.MarkArtifactPublicationUploading.commit", err)
	}
	return publication, nil
}

func (s *Store) CompleteArtifactPublication(
	ctx context.Context,
	intent persistence.ArtifactPublicationIntent,
	evidence persistence.ArtifactEvidence,
	metadata runstate.CommandMetadata,
) (contracts.Artifact, error) {
	requestHash, digest, err := validateArtifactIntent(intent, metadata)
	if err != nil {
		return contracts.Artifact{}, err
	}
	expectedKey := artifactObjectKey(s.tenantID, s.repositoryID, digest)
	if evidence.ObjectKey != expectedKey || strings.TrimSpace(evidence.ETag) == "" {
		return contracts.Artifact{}, fault.New(fault.CodeInvalidArgument, "postgres.CompleteArtifactPublication", "object verification evidence is invalid")
	}
	scope := artifactPublicationScope(s.repositoryID, intent.ArtifactID)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contracts.Artifact{}, databaseError("postgres.CompleteArtifactPublication.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return contracts.Artifact{}, err
	}
	if replay, found, err := loadControlReplay[contracts.Artifact](ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return contracts.Artifact{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return contracts.Artifact{}, databaseError("postgres.CompleteArtifactPublication.replay", err)
		}
		return replay, nil
	}
	publication, storedHash, found, err := loadArtifactPublicationByArtifact(ctx, tx, s.tenantID, s.repositoryID, intent.ArtifactID, true)
	if err != nil {
		return contracts.Artifact{}, err
	}
	if !found || !bytes.Equal(storedHash, requestHash) || publication.Intent.OperationID != intent.OperationID {
		return contracts.Artifact{}, fault.New(fault.CodeConflict, "postgres.CompleteArtifactPublication", "artifact publication does not match its reservation")
	}
	if publication.State == "active" {
		artifact, err := loadArtifact(ctx, tx, s.tenantID, s.repositoryID, intent.ArtifactID)
		if err != nil {
			return contracts.Artifact{}, err
		}
		if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 201, artifact); err != nil {
			return contracts.Artifact{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return contracts.Artifact{}, databaseError("postgres.CompleteArtifactPublication.recoverReceipt", err)
		}
		return artifact, nil
	}
	if publication.State != "uploading" && publication.State != "reserved" && publication.State != "reconciliation_required" {
		return contracts.Artifact{}, fault.New(fault.CodeConflict, "postgres.CompleteArtifactPublication", "artifact publication cannot activate from its current state")
	}
	now := postgresTimestamp(s.clock.Now())
	if _, err := tx.Exec(ctx, `
		UPDATE forja.artifact_objects
		SET state='active', provider_checksum_sha256=NULLIF($4, ''),
			provider_etag=$5, provider_version=NULLIF($6, ''), failure_class=NULL,
			verified_at=COALESCE(verified_at, $7), activated_at=$7, updated_at=$7
		WHERE tenant_id=$1 AND repository_id=$2 AND content_sha256=$3
		  AND state IN ('reserved', 'uploading', 'verified', 'failed', 'active')`,
		s.tenantID, s.repositoryID, digest, evidence.ProviderChecksumSHA256,
		evidence.ETag, evidence.VersionID, now,
	); err != nil {
		return contracts.Artifact{}, databaseError("postgres.CompleteArtifactPublication.object", err)
	}
	publication.State = "active"
	publication.Version++
	publication.UpdatedAt = now
	if _, err := tx.Exec(ctx, `
		UPDATE forja.artifact_operations
		SET state='active', version=$4, failure_class=NULL, updated_at=$5
		WHERE tenant_id=$1 AND repository_id=$2 AND operation_id=$3`,
		s.tenantID, s.repositoryID, intent.OperationID, publication.Version, now,
	); err != nil {
		return contracts.Artifact{}, databaseError("postgres.CompleteArtifactPublication.operation", err)
	}
	provenance, _ := json.Marshal(intent.Provenance)
	metadataValue := intent.Metadata
	if metadataValue == nil {
		metadataValue = map[string]any{}
	}
	metadataJSON, _ := json.Marshal(metadataValue)
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.artifacts (
			tenant_id, repository_id, artifact_id, operation_id, run_id, kind,
			status, version, content_sha256, media_type, size_bytes, created_by,
			provenance, metadata, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'active', 1, $7, $8, $9, $10, $11, $12, $13, $13)`,
		s.tenantID, s.repositoryID, intent.ArtifactID, intent.OperationID,
		intent.RunID, intent.Kind, digest, intent.MediaType, intent.SizeBytes,
		intent.CreatedBy, provenance, metadataJSON, publication.CreatedAt,
	); err != nil {
		return contracts.Artifact{}, databaseError("postgres.CompleteArtifactPublication.artifact", err)
	}
	artifact := artifactFromIntent(s.tenantID, s.repositoryID, intent, publication.CreatedAt)
	if err := s.appendArtifactPublicationEvent(ctx, tx, "artifact.publication_activated", publication, metadata); err != nil {
		return contracts.Artifact{}, err
	}
	encodedArtifact, _ := json.Marshal(artifact)
	if err := s.appendEvent(ctx, tx, "artifact", artifact.ArtifactID, 1, "artifact.activated", now, encodedArtifact, metadata); err != nil {
		return contracts.Artifact{}, err
	}
	if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 201, artifact); err != nil {
		return contracts.Artifact{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.Artifact{}, databaseError("postgres.CompleteArtifactPublication.commit", err)
	}
	return artifact, nil
}

func (s *Store) FailArtifactPublication(
	ctx context.Context,
	intent persistence.ArtifactPublicationIntent,
	failureClass string,
	metadata runstate.CommandMetadata,
) (persistence.ArtifactPublication, error) {
	requestHash, digest, err := validateArtifactIntent(intent, metadata)
	if err != nil {
		return persistence.ArtifactPublication{}, err
	}
	if !slices.Contains([]string{"retryable_provider", "integrity", "canonical_conflict", "interrupted"}, failureClass) {
		return persistence.ArtifactPublication{}, fault.New(fault.CodeInvalidArgument, "postgres.FailArtifactPublication", "failure class is invalid")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return persistence.ArtifactPublication{}, databaseError("postgres.FailArtifactPublication.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, artifactPublicationScope(s.repositoryID, intent.ArtifactID), metadata.IdempotencyKey); err != nil {
		return persistence.ArtifactPublication{}, err
	}
	publication, storedHash, found, err := loadArtifactPublicationByArtifact(ctx, tx, s.tenantID, s.repositoryID, intent.ArtifactID, true)
	if err != nil {
		return persistence.ArtifactPublication{}, err
	}
	if !found || !bytes.Equal(storedHash, requestHash) || publication.Intent.OperationID != intent.OperationID {
		return persistence.ArtifactPublication{}, fault.New(fault.CodeConflict, "postgres.FailArtifactPublication", "artifact publication does not match its reservation")
	}
	if publication.State == "active" {
		if err := tx.Commit(ctx); err != nil {
			return persistence.ArtifactPublication{}, databaseError("postgres.FailArtifactPublication.active", err)
		}
		return publication, nil
	}
	nextState := "failed"
	if failureClass == "retryable_provider" || failureClass == "interrupted" {
		nextState = "reconciliation_required"
	}
	now := postgresTimestamp(s.clock.Now())
	publication.State = nextState
	publication.Version++
	publication.UpdatedAt = now
	if _, err := tx.Exec(ctx, `
		UPDATE forja.artifact_operations
		SET state=$4, version=$5, failure_class=$6, updated_at=$7
		WHERE tenant_id=$1 AND repository_id=$2 AND operation_id=$3`,
		s.tenantID, s.repositoryID, intent.OperationID, nextState,
		publication.Version, failureClass, now,
	); err != nil {
		return persistence.ArtifactPublication{}, databaseError("postgres.FailArtifactPublication.operation", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE forja.artifact_objects
		SET state='failed', failure_class=$4, updated_at=$5
		WHERE tenant_id=$1 AND repository_id=$2 AND content_sha256=$3 AND state<>'active'`,
		s.tenantID, s.repositoryID, digest, failureClass, now,
	); err != nil {
		return persistence.ArtifactPublication{}, databaseError("postgres.FailArtifactPublication.object", err)
	}
	if err := s.appendArtifactPublicationEvent(ctx, tx, "artifact.publication_failed", publication, metadata); err != nil {
		return persistence.ArtifactPublication{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return persistence.ArtifactPublication{}, databaseError("postgres.FailArtifactPublication.commit", err)
	}
	return publication, nil
}

func (s *Store) appendArtifactPublicationEvent(
	ctx context.Context,
	tx pgx.Tx,
	eventType string,
	publication persistence.ArtifactPublication,
	metadata runstate.CommandMetadata,
) error {
	payload, err := json.Marshal(publication)
	if err != nil {
		return fault.Wrap(fault.CodeInternal, "postgres.appendArtifactPublicationEvent", "encode event", err)
	}
	return s.appendEvent(ctx, tx, "artifact_operation", publication.Intent.OperationID, publication.Version, eventType, publication.UpdatedAt, payload, metadata)
}

func loadArtifactPublicationByArtifact(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, repositoryID, artifactID string,
	forUpdate bool,
) (persistence.ArtifactPublication, []byte, bool, error) {
	query := `
		SELECT operation_id, artifact_id, encode(content_sha256, 'hex'),
		       expected_size_bytes, expected_media_type, created_by,
		       state, version, created_at, updated_at, request_sha256, intent
		FROM forja.artifact_operations
		WHERE tenant_id=$1 AND repository_id=$2 AND artifact_id=$3`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var publication persistence.ArtifactPublication
	var digestHex string
	var requestHash []byte
	var intentJSON []byte
	err := tx.QueryRow(ctx, query, tenantID, repositoryID, artifactID).Scan(
		&publication.Intent.OperationID, &publication.Intent.ArtifactID, &digestHex,
		&publication.Intent.SizeBytes, &publication.Intent.MediaType, &publication.Intent.CreatedBy,
		&publication.State, &publication.Version, &publication.CreatedAt, &publication.UpdatedAt,
		&requestHash, &intentJSON,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistence.ArtifactPublication{}, nil, false, nil
	}
	if err != nil {
		return persistence.ArtifactPublication{}, nil, false, databaseError("postgres.loadArtifactPublication", err)
	}
	storedOperationID := publication.Intent.OperationID
	storedArtifactID := publication.Intent.ArtifactID
	storedSize := publication.Intent.SizeBytes
	storedMediaType := publication.Intent.MediaType
	storedCreatedBy := publication.Intent.CreatedBy
	if len(intentJSON) > 2 {
		if err := json.Unmarshal(intentJSON, &publication.Intent); err != nil {
			return persistence.ArtifactPublication{}, nil, false, databaseError("postgres.loadArtifactPublication.intent", err)
		}
	}
	publication.Intent.OperationID = storedOperationID
	publication.Intent.ArtifactID = storedArtifactID
	publication.Intent.ContentHash = "sha256:" + digestHex
	publication.Intent.SizeBytes = storedSize
	publication.Intent.MediaType = storedMediaType
	publication.Intent.CreatedBy = storedCreatedBy
	publication.CreatedAt = publication.CreatedAt.UTC()
	publication.UpdatedAt = publication.UpdatedAt.UTC()
	return publication, requestHash, true, nil
}

func loadArtifact(ctx context.Context, tx pgx.Tx, tenantID, repositoryID, artifactID string) (contracts.Artifact, error) {
	var artifact contracts.Artifact
	var digestHex string
	var provenanceJSON, metadataJSON []byte
	err := tx.QueryRow(ctx, `
		SELECT artifact_id, run_id, kind, status, encode(content_sha256, 'hex'),
		       media_type, size_bytes, created_at, created_by, provenance, metadata
		FROM forja.artifacts
		WHERE tenant_id=$1 AND repository_id=$2 AND artifact_id=$3`,
		tenantID, repositoryID, artifactID,
	).Scan(&artifact.ArtifactID, &artifact.RunID, &artifact.Kind, &artifact.Status,
		&digestHex, &artifact.MediaType, &artifact.SizeBytes, &artifact.CreatedAt,
		&artifact.CreatedBy, &provenanceJSON, &metadataJSON)
	if err != nil {
		return contracts.Artifact{}, databaseError("postgres.loadArtifact", err)
	}
	artifact.SchemaVersion = "1.0"
	artifact.TenantID = "tenant_" + tenantID
	artifact.RepositoryID = "repo_" + repositoryID
	artifact.ContentHash = "sha256:" + digestHex
	artifact.CreatedAt = artifact.CreatedAt.UTC()
	if err := json.Unmarshal(provenanceJSON, &artifact.Provenance); err != nil {
		return contracts.Artifact{}, fault.Wrap(fault.CodeInternal, "postgres.loadArtifact", "decode provenance", err)
	}
	if err := json.Unmarshal(metadataJSON, &artifact.Metadata); err != nil {
		return contracts.Artifact{}, fault.Wrap(fault.CodeInternal, "postgres.loadArtifact", "decode metadata", err)
	}
	return artifact, nil
}

func validateArtifactIntent(
	intent persistence.ArtifactPublicationIntent,
	metadata runstate.CommandMetadata,
) ([]byte, []byte, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return nil, nil, err
	}
	if !artifactOperationIDPattern.MatchString(intent.OperationID) ||
		!artifactPublicIDPattern.MatchString(intent.ArtifactID) ||
		intent.CreatedBy != metadata.ActorID || intent.SizeBytes < 0 || intent.SizeBytes > 4<<30 ||
		len(intent.MediaType) < 3 || len(intent.MediaType) > 120 || strings.TrimSpace(intent.MediaType) != intent.MediaType {
		return nil, nil, fault.New(fault.CodeInvalidArgument, "postgres.validateArtifactIntent", "artifact publication intent is invalid")
	}
	if !slices.Contains([]string{
		"sprint_plan", "context_pack", "patch", "test_report", "validation_report",
		"evidence_bundle", "decision", "conversation", "memory", "index_snapshot", "runtime_receipt",
	}, intent.Kind) {
		return nil, nil, fault.New(fault.CodeInvalidArgument, "postgres.validateArtifactIntent", "artifact kind is invalid")
	}
	if intent.RunID != nil && !artifactRunIDPattern.MatchString(*intent.RunID) {
		return nil, nil, fault.New(fault.CodeInvalidArgument, "postgres.validateArtifactIntent", "artifact run ID is invalid")
	}
	if intent.Provenance.SourceCommit != nil && !artifactCommitPattern.MatchString(*intent.Provenance.SourceCommit) {
		return nil, nil, fault.New(fault.CodeInvalidArgument, "postgres.validateArtifactIntent", "artifact source commit is invalid")
	}
	if !slices.Contains([]string{"human", "agent", "compiler", "test", "runtime", "generated"}, intent.Provenance.SourceType) ||
		len(intent.Provenance.SourceRefs) == 0 || hasBlankOrDuplicate(intent.Provenance.SourceRefs) ||
		slices.ContainsFunc(intent.Provenance.SourceRefs, func(value string) bool { return len(value) > 500 }) {
		return nil, nil, fault.New(fault.CodeInvalidArgument, "postgres.validateArtifactIntent", "artifact provenance is invalid")
	}
	digestHex := strings.TrimPrefix(intent.ContentHash, "sha256:")
	digest, err := hex.DecodeString(digestHex)
	if err != nil || len(digest) != 32 || intent.ContentHash != "sha256:"+digestHex || strings.ToLower(digestHex) != digestHex {
		return nil, nil, fault.New(fault.CodeInvalidArgument, "postgres.validateArtifactIntent", "artifact content hash is invalid")
	}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return nil, nil, fault.Wrap(fault.CodeInvalidArgument, "postgres.validateArtifactIntent", "encode artifact intent", err)
	}
	metadata.AuditToolName = ""
	return hashCommand(metadata, "artifact_publication", string(encoded)), digest, nil
}

func hasBlankOrDuplicate(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func artifactPublicationScope(repositoryID, artifactID string) string {
	return "artifact_publication:" + repositoryID + ":" + artifactID
}

func artifactObjectKey(tenantID, repositoryID string, digest []byte) string {
	hexDigest := hex.EncodeToString(digest)
	return "tenants/" + tenantID + "/repositories/" + repositoryID + "/sha256/" + hexDigest[:2] + "/" + hexDigest[2:]
}

func artifactFromIntent(tenantID, repositoryID string, intent persistence.ArtifactPublicationIntent, createdAt time.Time) contracts.Artifact {
	size := intent.SizeBytes
	return contracts.Artifact{
		ArtifactID: intent.ArtifactID, SchemaVersion: "1.0",
		TenantID: "tenant_" + tenantID, RepositoryID: "repo_" + repositoryID,
		RunID: intent.RunID, Kind: intent.Kind, Status: "active",
		ContentHash: intent.ContentHash, MediaType: intent.MediaType, SizeBytes: &size,
		CreatedAt: createdAt, CreatedBy: intent.CreatedBy,
		Provenance: intent.Provenance, Metadata: intent.Metadata,
	}
}

var _ persistence.ArtifactPublicationRepository = (*Store)(nil)
