package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
)

// RecordRetrievalProjectionPoint writes the canonical receipt for an already
// acknowledged derived-store upsert. Until this record exists, Qdrant data is
// intentionally unresolvable and therefore cannot enter a governed context.
func (s *Store) RecordRetrievalProjectionPoint(ctx context.Context, point contracts.RetrievalPoint, sourceOutboxID int64) error {
	if err := contracts.ValidateRetrievalPoint(point); err != nil {
		return fmt.Errorf("validate retrieval projection point: %w", err)
	}
	if sourceOutboxID < 1 {
		return fmt.Errorf("source outbox ID is invalid")
	}
	sourceHash, err := decodeContentHash(point.SourceHash)
	if err != nil {
		return err
	}
	cardHash, err := decodeContentHash(point.CardTextHash)
	if err != nil {
		return err
	}
	proofRefs, err := json.Marshal(point.ProofRefs)
	if err != nil {
		return fmt.Errorf("encode retrieval proof references: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO forja.retrieval_projection_points (
			tenant_id, repository_id, generation_id, point_id, entity_id, artifact_family,
			source_commit, source_sha256, card_sha256, status, authority_class, stale,
			language, symbol_kind, repository_path, proof_refs, source_outbox_id
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (tenant_id, repository_id, generation_id, point_id) DO UPDATE
		SET entity_id=EXCLUDED.entity_id, artifact_family=EXCLUDED.artifact_family,
			source_commit=EXCLUDED.source_commit, source_sha256=EXCLUDED.source_sha256,
			card_sha256=EXCLUDED.card_sha256, status=EXCLUDED.status,
			authority_class=EXCLUDED.authority_class, stale=EXCLUDED.stale,
			language=EXCLUDED.language, symbol_kind=EXCLUDED.symbol_kind,
			repository_path=EXCLUDED.repository_path, proof_refs=EXCLUDED.proof_refs,
			source_outbox_id=EXCLUDED.source_outbox_id, indexed_at=clock_timestamp(),
			deleted_at=NULL`,
		strings.TrimPrefix(point.TenantID, "tenant_"), strings.TrimPrefix(point.RepositoryID, "repo_"), point.CollectionGeneration,
		point.PointID, point.EntityID, point.ArtifactFamily, point.SourceCommit, sourceHash, cardHash,
		point.Status, point.AuthorityClass, point.Stale, point.Language, point.SymbolKind,
		point.RepositoryPath, proofRefs, sourceOutboxID,
	)
	if err != nil {
		return databaseError("postgres.RecordRetrievalProjectionPoint", err)
	}
	return nil
}

// ResetRetrievalProjection reopens an entire independent retrieval delivery
// ledger after the matching physical Qdrant collection was deleted or replaced
// by an operator. It first deletes canonical point provenance, making any
// leftover vectors fail closed, then resets every delivery/checkpoint under a
// watermark lock. Existing dead-letter records remain immutable evidence.
func (s *Store) ResetRetrievalProjection(ctx context.Context, projectorName string, configurationHash [32]byte, generationID string) error {
	if !validProjectorName(projectorName) || configurationHash == ([32]byte{}) || !contracts.IsRetrievalGenerationID(generationID) {
		return fault.New(fault.CodeInvalidArgument, "postgres.ResetRetrievalProjection", "projector, configuration hash, or generation is invalid")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return databaseError("postgres.ResetRetrievalProjection.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", advisoryLockKey(outboxWatermarkLock)); err != nil {
		return databaseError("postgres.ResetRetrievalProjection.lockWatermark", err)
	}
	var registeredHash []byte
	var consumerStatus string
	err = tx.QueryRow(ctx, `
		SELECT configuration_sha256, status
		FROM forja.projection_consumers
		WHERE tenant_id=$1 AND repository_id=$2 AND projector_name=$3
		FOR UPDATE`, s.tenantID, s.repositoryID, projectorName).Scan(&registeredHash, &consumerStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return fault.New(fault.CodeNotFound, "postgres.ResetRetrievalProjection", "projection consumer is not registered")
	}
	if err != nil {
		return databaseError("postgres.ResetRetrievalProjection.consumer", err)
	}
	if consumerStatus != "active" || string(registeredHash) != string(configurationHash[:]) {
		return fault.New(fault.CodeConflict, "postgres.ResetRetrievalProjection", "projection consumer is not the registered active configuration")
	}
	var generationStatus string
	err = tx.QueryRow(ctx, `
		SELECT status FROM forja.retrieval_generations
		WHERE tenant_id=$1 AND repository_id=$2 AND generation_id=$3
		FOR UPDATE`, s.tenantID, s.repositoryID, generationID).Scan(&generationStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return fault.New(fault.CodeNotFound, "postgres.ResetRetrievalProjection", "retrieval generation is not registered")
	}
	if err != nil {
		return databaseError("postgres.ResetRetrievalProjection.generation", err)
	}
	if generationStatus == "retired" || generationStatus == "failed" {
		return fault.New(fault.CodeConflict, "postgres.ResetRetrievalProjection", "retrieval generation cannot be rebuilt")
	}
	var inflight int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM forja.projection_deliveries
		WHERE tenant_id=$1 AND repository_id=$2 AND projector_name=$3 AND state='inflight'`,
		s.tenantID, s.repositoryID, projectorName).Scan(&inflight); err != nil {
		return databaseError("postgres.ResetRetrievalProjection.inflight", err)
	}
	if inflight != 0 {
		return fault.New(fault.CodeConflict, "postgres.ResetRetrievalProjection", "projection deliveries are still inflight")
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM forja.retrieval_projection_points
		WHERE tenant_id=$1 AND repository_id=$2 AND generation_id=$3`,
		s.tenantID, s.repositoryID, generationID); err != nil {
		return databaseError("postgres.ResetRetrievalProjection.clearPoints", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.projection_deliveries (
			tenant_id, repository_id, projector_name, outbox_id, state, available_at
		)
		SELECT outbox.tenant_id, outbox.repository_id, $3, outbox.outbox_id, 'pending', outbox.available_at
		FROM forja.outbox AS outbox
		WHERE outbox.tenant_id=$1 AND outbox.repository_id=$2
		ON CONFLICT (tenant_id, repository_id, projector_name, outbox_id) DO NOTHING`,
		s.tenantID, s.repositoryID, projectorName); err != nil {
		return databaseError("postgres.ResetRetrievalProjection.backfill", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE forja.projection_deliveries
		SET state='pending', available_at=clock_timestamp(), locked_by=NULL,
			locked_until=NULL, fencing_token=fencing_token+1, attempts=0,
			last_error='rebuild_requested', published_at=NULL
		WHERE tenant_id=$1 AND repository_id=$2 AND projector_name=$3`,
		s.tenantID, s.repositoryID, projectorName); err != nil {
		return databaseError("postgres.ResetRetrievalProjection.resetDeliveries", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE forja.projection_checkpoints
		SET last_outbox_id=0, updated_at=clock_timestamp()
		WHERE tenant_id=$1 AND repository_id=$2 AND projector_name=$3`,
		s.tenantID, s.repositoryID, projectorName); err != nil {
		return databaseError("postgres.ResetRetrievalProjection.resetCheckpoint", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return databaseError("postgres.ResetRetrievalProjection.commit", err)
	}
	return nil
}

// TombstoneRetrievalProjectionPoints durably removes retrieval authority for
// one retired source commit before a derived-store delete is attempted. It
// returns every matching stable ID, including prior tombstones, so a retry can
// safely repair a Qdrant delete that failed after this canonical receipt.
func (s *Store) TombstoneRetrievalProjectionPoints(ctx context.Context, generationID, sourceCommit string, sourceOutboxID int64) ([]string, error) {
	if generationID == "" || sourceOutboxID < 1 || len(sourceCommit) < 40 || len(sourceCommit) > 64 {
		return nil, fmt.Errorf("retrieval tombstone arguments are invalid")
	}
	rows, err := s.pool.Query(ctx, `
		WITH affected AS (
			UPDATE forja.retrieval_projection_points
			SET status='tombstoned', stale=true, deleted_at=clock_timestamp(),
				source_outbox_id=$4
			WHERE tenant_id=$1 AND repository_id=$2 AND generation_id=$3
			  AND source_commit=$5 AND status <> 'tombstoned'
			RETURNING point_id
		)
		SELECT point_id FROM affected
		UNION
		SELECT point_id
		FROM forja.retrieval_projection_points
		WHERE tenant_id=$1 AND repository_id=$2 AND generation_id=$3
		  AND source_commit=$5 AND status='tombstoned'
		ORDER BY point_id`,
		s.tenantID, s.repositoryID, generationID, sourceOutboxID, sourceCommit,
	)
	if err != nil {
		return nil, databaseError("postgres.TombstoneRetrievalProjectionPoints", err)
	}
	defer rows.Close()
	pointIDs := []string{}
	for rows.Next() {
		var pointID string
		if err := rows.Scan(&pointID); err != nil {
			return nil, databaseError("postgres.TombstoneRetrievalProjectionPoints.scan", err)
		}
		pointIDs = append(pointIDs, pointID)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError("postgres.TombstoneRetrievalProjectionPoints.rows", err)
	}
	return pointIDs, nil
}
