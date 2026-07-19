package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rvbernucci/forja-guide/internal/contracts"
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
