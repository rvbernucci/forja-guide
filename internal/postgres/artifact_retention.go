package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

func (s *Store) TombstoneArtifact(
	ctx context.Context,
	artifactID string,
	expectedVersion int,
	metadata runstate.CommandMetadata,
) (contracts.Artifact, error) {
	if err := validateRetentionAuthority(metadata); err != nil {
		return contracts.Artifact{}, err
	}
	if !artifactPublicIDPattern.MatchString(artifactID) || expectedVersion < 1 {
		return contracts.Artifact{}, fault.New(fault.CodeInvalidArgument, "postgres.TombstoneArtifact", "artifact tombstone command is invalid")
	}
	scope := "artifact_tombstone:" + s.repositoryID + ":" + artifactID
	requestHash := hashKnowledgeCommand(metadata, artifactID, fmt.Sprint(expectedVersion))
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contracts.Artifact{}, databaseError("postgres.TombstoneArtifact.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return contracts.Artifact{}, err
	}
	if replay, found, err := loadControlReplay[contracts.Artifact](ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return contracts.Artifact{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return contracts.Artifact{}, databaseError("postgres.TombstoneArtifact.replay", err)
		}
		return replay, nil
	}
	var version int
	var digest []byte
	var tombstonedAt *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT version, content_sha256, tombstoned_at
		FROM forja.artifacts
		WHERE tenant_id=$1 AND repository_id=$2 AND artifact_id=$3
		FOR UPDATE`, s.tenantID, s.repositoryID, artifactID,
	).Scan(&version, &digest, &tombstonedAt); err != nil {
		return contracts.Artifact{}, databaseError("postgres.TombstoneArtifact.load", err)
	}
	if version != expectedVersion || tombstonedAt != nil {
		return contracts.Artifact{}, fault.New(fault.CodeConflict, "postgres.TombstoneArtifact", "artifact is not a matching live version")
	}
	if err := ensureNoLiveArtifactReferences(ctx, tx, s.tenantID, s.repositoryID, artifactID, digest); err != nil {
		return contracts.Artifact{}, err
	}
	now := postgresTimestamp(s.clock.Now())
	version++
	if _, err := tx.Exec(ctx, `
		UPDATE forja.artifacts
		SET status='archived', version=$4, tombstoned_at=$5, updated_at=$5
		WHERE tenant_id=$1 AND repository_id=$2 AND artifact_id=$3`,
		s.tenantID, s.repositoryID, artifactID, version, now,
	); err != nil {
		return contracts.Artifact{}, databaseError("postgres.TombstoneArtifact.update", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE forja.artifact_objects AS object
		SET state='tombstoned', tombstoned_at=$4, updated_at=$4
		WHERE object.tenant_id=$1 AND object.repository_id=$2
		  AND object.content_sha256=$3 AND object.state='active'
		  AND NOT EXISTS (
			SELECT 1 FROM forja.artifacts AS alias
			WHERE alias.tenant_id=object.tenant_id
			  AND alias.repository_id=object.repository_id
			  AND alias.content_sha256=object.content_sha256
			  AND alias.tombstoned_at IS NULL
		  )`, s.tenantID, s.repositoryID, digest, now,
	); err != nil {
		return contracts.Artifact{}, databaseError("postgres.TombstoneArtifact.object", err)
	}
	artifact, err := loadArtifact(ctx, tx, s.tenantID, s.repositoryID, artifactID)
	if err != nil {
		return contracts.Artifact{}, err
	}
	payload, _ := json.Marshal(struct {
		Artifact contracts.Artifact `json:"artifact"`
		Deleted  bool               `json:"deleted"`
	}{Artifact: artifact, Deleted: false})
	if err := s.appendEvent(ctx, tx, "artifact", artifactID, version, "artifact.tombstoned", now, payload, metadata); err != nil {
		return contracts.Artifact{}, err
	}
	if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 200, artifact); err != nil {
		return contracts.Artifact{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.Artifact{}, databaseError("postgres.TombstoneArtifact.commit", err)
	}
	return artifact, nil
}

func (s *Store) ListArtifactRetentionCandidates(
	ctx context.Context,
	tombstonedBefore time.Time,
	limit int,
) ([]persistence.RetentionCandidate, error) {
	if tombstonedBefore.IsZero() || limit < 1 || limit > 500 {
		return nil, fault.New(fault.CodeInvalidArgument, "postgres.ListArtifactRetentionCandidates", "retention query is invalid")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT encode(object.content_sha256, 'hex'), object.object_key,
		       object.provider_etag, object.size_bytes, object.media_type,
		       object.tombstoned_at
		FROM forja.artifact_objects AS object
		WHERE object.tenant_id=$1 AND object.repository_id=$2
		  AND object.state='tombstoned' AND object.tombstoned_at <= $3
		  AND object.provider_etag IS NOT NULL
		  AND NOT EXISTS (
			SELECT 1 FROM forja.artifacts AS artifact
			WHERE artifact.tenant_id=object.tenant_id
			  AND artifact.repository_id=object.repository_id
			  AND artifact.content_sha256=object.content_sha256
			  AND artifact.tombstoned_at IS NULL
		  )
		ORDER BY object.tombstoned_at, object.object_key
		LIMIT $4`, s.tenantID, s.repositoryID, tombstonedBefore.UTC(), limit)
	if err != nil {
		return nil, databaseError("postgres.ListArtifactRetentionCandidates", err)
	}
	defer rows.Close()
	result := make([]persistence.RetentionCandidate, 0, limit)
	for rows.Next() {
		var item persistence.RetentionCandidate
		var digestHex string
		if err := rows.Scan(&digestHex, &item.ObjectKey, &item.ETag, &item.SizeBytes, &item.MediaType, &item.TombstonedAt); err != nil {
			return nil, databaseError("postgres.ListArtifactRetentionCandidates.scan", err)
		}
		item.TenantID = s.tenantID
		item.RepositoryID = s.repositoryID
		item.ContentHash = "sha256:" + digestHex
		item.TombstonedAt = item.TombstonedAt.UTC()
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError("postgres.ListArtifactRetentionCandidates.rows", err)
	}
	return result, nil
}

func (s *Store) MarkArtifactObjectPurged(
	ctx context.Context,
	contentHash string,
	expectedETag string,
	metadata runstate.CommandMetadata,
) error {
	if err := validateRetentionAuthority(metadata); err != nil {
		return err
	}
	digest, err := decodeContentHash(contentHash)
	if err != nil || strings.TrimSpace(expectedETag) == "" {
		return fault.New(fault.CodeInvalidArgument, "postgres.MarkArtifactObjectPurged", "purge evidence is invalid")
	}
	scope := "artifact_object_purge:" + s.repositoryID + ":" + contentHash
	requestHash := hashKnowledgeCommand(metadata, contentHash)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return databaseError("postgres.MarkArtifactObjectPurged.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return err
	}
	if _, found, err := loadControlReplay[struct{}](ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return databaseError("postgres.MarkArtifactObjectPurged.replay", err)
		}
		return nil
	}
	var state, storedETag string
	if err := tx.QueryRow(ctx, `
		SELECT state, provider_etag FROM forja.artifact_objects
		WHERE tenant_id=$1 AND repository_id=$2 AND content_sha256=$3
		FOR UPDATE`, s.tenantID, s.repositoryID, digest,
	).Scan(&state, &storedETag); err != nil {
		return databaseError("postgres.MarkArtifactObjectPurged.load", err)
	}
	if state != "tombstoned" || storedETag != expectedETag {
		return fault.New(fault.CodeConflict, "postgres.MarkArtifactObjectPurged", "object is not the exact tombstoned purge candidate")
	}
	var liveAliases bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM forja.artifacts
			WHERE tenant_id=$1 AND repository_id=$2 AND content_sha256=$3
			  AND tombstoned_at IS NULL
		)`, s.tenantID, s.repositoryID, digest).Scan(&liveAliases); err != nil {
		return databaseError("postgres.MarkArtifactObjectPurged.references", err)
	}
	if liveAliases {
		return fault.New(fault.CodeConflict, "postgres.MarkArtifactObjectPurged", "live artifact aliases prevent purge")
	}
	now := postgresTimestamp(s.clock.Now())
	if _, err := tx.Exec(ctx, `
		UPDATE forja.artifact_objects
		SET state='purged', purged_at=$4, updated_at=$4
		WHERE tenant_id=$1 AND repository_id=$2 AND content_sha256=$3`,
		s.tenantID, s.repositoryID, digest, now,
	); err != nil {
		return databaseError("postgres.MarkArtifactObjectPurged.update", err)
	}
	payload, _ := json.Marshal(struct {
		ContentHash string `json:"content_hash"`
		Deleted     bool   `json:"deleted"`
	}{ContentHash: contentHash, Deleted: true})
	if err := s.appendEvent(ctx, tx, "artifact", "object:"+contentHash, 1, "artifact.object_purged", now, payload, metadata); err != nil {
		return err
	}
	if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 200, struct{}{}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return databaseError("postgres.MarkArtifactObjectPurged.commit", err)
	}
	return nil
}

func ensureNoLiveArtifactReferences(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, repositoryID, artifactID string,
	digest []byte,
) error {
	var live bool
	err := tx.QueryRow(ctx, `
		SELECT
			EXISTS (
				SELECT 1 FROM forja.message_parts AS part
				JOIN forja.messages AS message USING (tenant_id, repository_id, message_id)
				JOIN forja.conversations AS conversation USING (tenant_id, repository_id, conversation_id)
				WHERE part.tenant_id=$1 AND part.repository_id=$2
				  AND part.artifact_id=$3 AND part.content_sha256=$4
				  AND conversation.status<>'tombstoned'
			) OR EXISTS (
				SELECT 1 FROM forja.message_citations AS citation
				JOIN forja.messages AS message USING (tenant_id, repository_id, message_id)
				JOIN forja.conversations AS conversation USING (tenant_id, repository_id, conversation_id)
				WHERE citation.tenant_id=$1 AND citation.repository_id=$2
				  AND citation.source_artifact_id=$3 AND citation.source_content_sha256=$4
				  AND conversation.status<>'tombstoned'
			) OR EXISTS (
				SELECT 1 FROM forja.memory_candidates AS candidate
				WHERE candidate.tenant_id=$1 AND candidate.repository_id=$2
				  AND candidate.proposed_artifact_id=$3 AND candidate.proposed_content_sha256=$4
				  AND candidate.status='proposed'
			) OR EXISTS (
				SELECT 1 FROM forja.memory_records AS memory
				WHERE memory.tenant_id=$1 AND memory.repository_id=$2
				  AND memory.content_artifact_id=$3 AND memory.content_sha256=$4
				  AND memory.status IN ('active', 'superseded')
			) OR EXISTS (
				SELECT 1 FROM forja.conversations AS conversation
				WHERE conversation.tenant_id=$1 AND conversation.repository_id=$2
				  AND conversation.transcript_artifact_id=$3
				  AND conversation.status<>'tombstoned'
			) OR EXISTS (
				SELECT 1
				FROM forja.artifact_bundle_entries AS entry
				JOIN forja.artifact_bundle_manifests AS manifest USING (tenant_id, repository_id, manifest_id)
				WHERE entry.tenant_id=$1 AND entry.repository_id=$2
				  AND entry.artifact_id=$3 AND entry.content_sha256=$4
				  AND (
					manifest.family<>'conversation_transcript'
					OR NOT EXISTS (
						SELECT 1 FROM forja.conversations AS owner
						WHERE owner.tenant_id=manifest.tenant_id
						  AND owner.repository_id=manifest.repository_id
						  AND owner.transcript_manifest_id=manifest.manifest_id
						  AND owner.status='tombstoned'
					)
				  )
			)`, tenantID, repositoryID, artifactID, digest).Scan(&live)
	if err != nil {
		return databaseError("postgres.ensureNoLiveArtifactReferences", err)
	}
	if live {
		return fault.New(fault.CodeConflict, "postgres.ensureNoLiveArtifactReferences", "live canonical references prevent artifact tombstone")
	}
	return nil
}

func validateRetentionAuthority(metadata runstate.CommandMetadata) error {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return err
	}
	if metadata.ActorType != "human" && metadata.ActorType != "system" {
		return fault.New(fault.CodePermissionDenied, "postgres.validateRetentionAuthority", "artifact retention requires human or system authority")
	}
	return nil
}

var _ persistence.ArtifactRetentionRepository = (*Store)(nil)
