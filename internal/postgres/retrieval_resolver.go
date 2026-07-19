package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/retrieval"
)

// ResolveRetrievalPoint reauthorizes a Qdrant candidate against the active
// canonical index. Symbol and test cards resolve only through the indexed
// symbol source; decisions are re-derived from their current canonical row.
// Unsupported families return no match rather than being trusted from derived
// projection metadata.
func (s *Store) ResolveRetrievalPoint(ctx context.Context, pointID string) ([]retrieval.CanonicalCandidate, error) {
	if candidates, found, err := s.resolveDecisionRetrievalPoint(ctx, pointID); err != nil || found {
		return candidates, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT point.point_id, point.generation_id,
		       'tenant_' || point.tenant_id::text, 'repo_' || point.repository_id::text,
		       point.entity_id, point.artifact_family, point.source_commit,
		       'sha256:' || encode(point.source_sha256, 'hex'), point.status,
		       point.authority_class, point.stale, point.language, point.symbol_kind,
		       point.repository_path, point.proof_refs
		FROM forja.retrieval_projection_points AS point
		JOIN forja.index_symbols AS symbol
		  ON point.artifact_family IN ('symbol', 'test') AND point.entity_id=symbol.symbol_id
		JOIN forja.index_files AS file
		  ON file.tenant_id=symbol.tenant_id AND file.repository_id=symbol.repository_id
		 AND file.snapshot_id=symbol.snapshot_id AND file.file_id=symbol.file_id
		JOIN forja.index_snapshots AS snapshot
		  ON snapshot.tenant_id=symbol.tenant_id AND snapshot.repository_id=symbol.repository_id
		 AND snapshot.snapshot_id=symbol.snapshot_id
		WHERE point.tenant_id=$1 AND point.repository_id=$2 AND point.point_id=$3
		  AND point.status='active' AND point.stale=false
		  AND (point.artifact_family='symbol' OR (point.artifact_family='test' AND symbol.is_test=true))
		  AND snapshot.status='active' AND snapshot.source_commit=point.source_commit
		  AND file.source_sha256=point.source_sha256
		ORDER BY point.generation_id`, s.tenantID, s.repositoryID, pointID)
	if err != nil {
		return nil, databaseError("postgres.ResolveRetrievalPoint", err)
	}
	defer rows.Close()
	result := []retrieval.CanonicalCandidate{}
	for rows.Next() {
		var candidate retrieval.CanonicalCandidate
		var proofRefs []byte
		if err := rows.Scan(
			&candidate.PointID, &candidate.CollectionGeneration, &candidate.TenantID,
			&candidate.RepositoryID, &candidate.EntityID, &candidate.ArtifactFamily,
			&candidate.SourceCommit, &candidate.SourceHash, &candidate.Status,
			&candidate.AuthorityClass, &candidate.Stale, &candidate.Language,
			&candidate.SymbolKind, &candidate.RepositoryPath, &proofRefs,
		); err != nil {
			return nil, databaseError("postgres.ResolveRetrievalPoint.scan", err)
		}
		if err := json.Unmarshal(proofRefs, &candidate.ProofRefs); err != nil {
			return nil, fmt.Errorf("decode canonical retrieval proof references: %w", err)
		}
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError("postgres.ResolveRetrievalPoint.rows", err)
	}
	return result, nil
}

func (s *Store) resolveDecisionRetrievalPoint(ctx context.Context, pointID string) ([]retrieval.CanonicalCandidate, bool, error) {
	var generation, entityID, storedHash, status, authorityClass string
	var stale bool
	err := s.pool.QueryRow(ctx, `
		SELECT generation_id, entity_id, 'sha256:' || encode(source_sha256, 'hex'), status, authority_class, stale
		FROM forja.retrieval_projection_points
		WHERE tenant_id=$1 AND repository_id=$2 AND point_id=$3 AND artifact_family='decision'`,
		s.tenantID, s.repositoryID, pointID,
	).Scan(&generation, &entityID, &storedHash, &status, &authorityClass, &stale)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, databaseError("postgres.ResolveRetrievalPoint.decisionPoint", err)
	}
	decision, found, err := s.GetDecision(ctx, entityID)
	if err != nil {
		return nil, true, err
	}
	if !found || status != "active" || authorityClass != "canonical" || stale {
		return []retrieval.CanonicalCandidate{}, true, nil
	}
	source, err := retrieval.BuildDecisionSource("tenant_"+s.tenantID, "repo_"+s.repositoryID, decision)
	if err != nil || storedHash != source.SourceHash || pointID != contracts.RetrievalPointID(generation, decision.DecisionID, source.SourceHash) {
		return []retrieval.CanonicalCandidate{}, true, nil
	}
	return []retrieval.CanonicalCandidate{{
		PointID: pointID, CollectionGeneration: generation,
		TenantID: "tenant_" + s.tenantID, RepositoryID: "repo_" + s.repositoryID,
		EntityID: decision.DecisionID, ArtifactFamily: "decision", SourceHash: source.SourceHash,
		Status: "active", AuthorityClass: "canonical", ProofRefs: source.ProofRefs,
	}}, true, nil
}
