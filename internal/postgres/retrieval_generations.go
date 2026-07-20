package postgres

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/persistence"
)

var retrievalCollectionNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,119}$`)

// RegisterRetrievalGeneration stores an immutable derived-store contract in
// PostgreSQL before a physical collection is built. Replays are accepted only
// when every vector-contract field agrees; a reused generation ID can never
// silently change embedding dimensions or a collection target.
func (s *Store) RegisterRetrievalGeneration(ctx context.Context, config persistence.RetrievalGenerationConfig) error {
	if err := validateRetrievalGenerationConfig(config); err != nil {
		return err
	}
	command, err := s.pool.Exec(ctx, `
		INSERT INTO forja.retrieval_generations (
			tenant_id, repository_id, generation_id, collection_alias, collection_name,
			embedding_model, embedding_version, dimensions, sparse_encoder_version, status
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'building')
		ON CONFLICT (tenant_id, repository_id, generation_id) DO NOTHING`,
		s.tenantID, s.repositoryID, config.GenerationID, config.CollectionAlias, config.CollectionName,
		config.EmbeddingModel, config.EmbeddingVersion, config.Dimensions, config.SparseEncoderVersion,
	)
	if err != nil {
		return databaseError("postgres.RegisterRetrievalGeneration", err)
	}
	if command.RowsAffected() == 1 {
		return nil
	}
	existing, found, err := s.GetRetrievalGeneration(ctx, config.GenerationID)
	if err != nil {
		return err
	}
	if !found || !sameRetrievalGenerationConfig(existing.RetrievalGenerationConfig, config) {
		return fmt.Errorf("retrieval generation registration conflicts with immutable vector contract")
	}
	return nil
}

// GetRetrievalGeneration reads the lifecycle receipt without inferring that a
// collection exists or that a mutable Qdrant alias points at it.
func (s *Store) GetRetrievalGeneration(ctx context.Context, generationID string) (persistence.RetrievalGeneration, bool, error) {
	if !contracts.IsRetrievalGenerationID(generationID) {
		return persistence.RetrievalGeneration{}, false, fmt.Errorf("retrieval generation ID is invalid")
	}
	generation, err := scanRetrievalGeneration(s.pool.QueryRow(ctx, `
		SELECT generation_id, collection_alias, collection_name, embedding_model,
		       embedding_version, dimensions, sparse_encoder_version, status,
		       created_at, activated_at, retired_at
		FROM forja.retrieval_generations
		WHERE tenant_id=$1 AND repository_id=$2 AND generation_id=$3`,
		s.tenantID, s.repositoryID, generationID,
	))
	if err == pgx.ErrNoRows {
		return persistence.RetrievalGeneration{}, false, nil
	}
	if err != nil {
		return persistence.RetrievalGeneration{}, false, databaseError("postgres.GetRetrievalGeneration", err)
	}
	return generation, true, nil
}

// ActivateRetrievalGeneration serializes the canonical side of a completed
// Qdrant cutover. Call it only after CutoverQdrantCollection has verified the
// physical contract and read the alias back. It drains at most one previous
// generation for the same alias and returns that pre-transition receipt for
// the bounded observation/rollback window.
func (s *Store) ActivateRetrievalGeneration(ctx context.Context, generationID string) (*persistence.RetrievalGeneration, error) {
	if !contracts.IsRetrievalGenerationID(generationID) {
		return nil, fmt.Errorf("retrieval generation ID is invalid")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError("postgres.ActivateRetrievalGeneration.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var alias string
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT collection_alias, status
		FROM forja.retrieval_generations
		WHERE tenant_id=$1 AND repository_id=$2 AND generation_id=$3
		`, s.tenantID, s.repositoryID, generationID).Scan(&alias, &status); err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("retrieval generation is not registered")
		}
		return nil, databaseError("postgres.ActivateRetrievalGeneration.select", err)
	}
	if err := s.lockRetrievalAlias(ctx, tx, alias); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `
		SELECT status
		FROM forja.retrieval_generations
		WHERE tenant_id=$1 AND repository_id=$2 AND generation_id=$3
		FOR UPDATE`, s.tenantID, s.repositoryID, generationID).Scan(&status); err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("retrieval generation is not registered")
		}
		return nil, databaseError("postgres.ActivateRetrievalGeneration.lock_target", err)
	}
	if status == "retired" || status == "failed" {
		return nil, fmt.Errorf("retrieval generation status cannot be activated")
	}

	rows, err := tx.Query(ctx, `
		SELECT generation_id, collection_alias, collection_name, embedding_model,
		       embedding_version, dimensions, sparse_encoder_version, status,
		       created_at, activated_at, retired_at
		FROM forja.retrieval_generations
		WHERE tenant_id=$1 AND repository_id=$2 AND collection_alias=$3
		ORDER BY generation_id
		FOR UPDATE`, s.tenantID, s.repositoryID, alias)
	if err != nil {
		return nil, databaseError("postgres.ActivateRetrievalGeneration.lock", err)
	}
	var previous *persistence.RetrievalGeneration
	for rows.Next() {
		candidate, scanErr := scanRetrievalGeneration(rows)
		if scanErr != nil {
			rows.Close()
			return nil, databaseError("postgres.ActivateRetrievalGeneration.scan", scanErr)
		}
		if candidate.Status == "active" && candidate.GenerationID != generationID {
			if previous != nil {
				rows.Close()
				return nil, fmt.Errorf("retrieval generation lifecycle has multiple active alias targets")
			}
			copy := candidate
			previous = &copy
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, databaseError("postgres.ActivateRetrievalGeneration.rows", err)
	}
	rows.Close()
	if previous != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE forja.retrieval_generations
			SET status='draining'
			WHERE tenant_id=$1 AND repository_id=$2 AND generation_id=$3 AND status='active'`,
			s.tenantID, s.repositoryID, previous.GenerationID); err != nil {
			return nil, databaseError("postgres.ActivateRetrievalGeneration.drain", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE forja.retrieval_generations
		SET status='active', activated_at=COALESCE(activated_at, clock_timestamp()), retired_at=NULL
		WHERE tenant_id=$1 AND repository_id=$2 AND generation_id=$3`,
		s.tenantID, s.repositoryID, generationID); err != nil {
		return nil, databaseError("postgres.ActivateRetrievalGeneration.activate", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, databaseError("postgres.ActivateRetrievalGeneration.commit", err)
	}
	return previous, nil
}

// RetireRetrievalGeneration is intentionally unable to retire the serving
// generation. An operator must activate another verified generation first,
// then retire the now-draining one after the observation window.
func (s *Store) RetireRetrievalGeneration(ctx context.Context, generationID string) error {
	if !contracts.IsRetrievalGenerationID(generationID) {
		return fmt.Errorf("retrieval generation ID is invalid")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return databaseError("postgres.RetireRetrievalGeneration.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var alias string
	if err := tx.QueryRow(ctx, `
		SELECT collection_alias
		FROM forja.retrieval_generations
		WHERE tenant_id=$1 AND repository_id=$2 AND generation_id=$3`,
		s.tenantID, s.repositoryID, generationID).Scan(&alias); err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("retrieval generation cannot be retired")
		}
		return databaseError("postgres.RetireRetrievalGeneration.select", err)
	}
	if err := s.lockRetrievalAlias(ctx, tx, alias); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `
		UPDATE forja.retrieval_generations
		SET status='retired', retired_at=COALESCE(retired_at, clock_timestamp())
		WHERE tenant_id=$1 AND repository_id=$2 AND generation_id=$3
		  AND status IN ('building', 'draining', 'retired')`,
		s.tenantID, s.repositoryID, generationID)
	if err != nil {
		return databaseError("postgres.RetireRetrievalGeneration", err)
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("retrieval generation cannot be retired")
	}
	if err := tx.Commit(ctx); err != nil {
		return databaseError("postgres.RetireRetrievalGeneration.commit", err)
	}
	return nil
}

// WithRetrievalAliasMutation holds the canonical PostgreSQL advisory lock for
// one scoped alias while an operator observes, updates, and verifies Qdrant.
// The transaction-scoped lock is released automatically on every error path.
func (s *Store) WithRetrievalAliasMutation(ctx context.Context, alias string, operation func(context.Context) error) error {
	if !retrievalCollectionNamePattern.MatchString(alias) || operation == nil {
		return fmt.Errorf("retrieval alias mutation guard is invalid")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return databaseError("postgres.WithRetrievalAliasMutation.begin", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := s.lockRetrievalAlias(ctx, tx, alias); err != nil {
		return err
	}
	if err := operation(ctx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return databaseError("postgres.WithRetrievalAliasMutation.commit", err)
	}
	return nil
}

// lockRetrievalAlias serializes lifecycle transitions for one scoped alias.
// A hash collision only serializes unrelated transitions; it cannot weaken
// correctness. The lock is released automatically with the transaction.
func (s *Store) lockRetrievalAlias(ctx context.Context, tx pgx.Tx, alias string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, s.tenantID+"|"+s.repositoryID+"|"+alias); err != nil {
		return databaseError("postgres.lockRetrievalAlias", err)
	}
	return nil
}

func validateRetrievalGenerationConfig(config persistence.RetrievalGenerationConfig) error {
	if config.GenerationID != strings.TrimSpace(config.GenerationID) ||
		config.EmbeddingModel != strings.TrimSpace(config.EmbeddingModel) ||
		config.EmbeddingVersion != strings.TrimSpace(config.EmbeddingVersion) ||
		config.SparseEncoderVersion != strings.TrimSpace(config.SparseEncoderVersion) ||
		!contracts.IsRetrievalGenerationID(config.GenerationID) ||
		!retrievalCollectionNamePattern.MatchString(config.CollectionAlias) ||
		!retrievalCollectionNamePattern.MatchString(config.CollectionName) ||
		strings.TrimSpace(config.EmbeddingModel) == "" || len(config.EmbeddingModel) > 200 ||
		strings.TrimSpace(config.EmbeddingVersion) == "" || len(config.EmbeddingVersion) > 160 ||
		config.Dimensions < 1 || config.Dimensions > 4096 ||
		strings.TrimSpace(config.SparseEncoderVersion) == "" || len(config.SparseEncoderVersion) > 160 ||
		config.GenerationID != contracts.RetrievalGenerationID(config.EmbeddingModel, config.EmbeddingVersion, config.Dimensions, config.SparseEncoderVersion) {
		return fmt.Errorf("retrieval generation configuration is invalid")
	}
	return nil
}

func sameRetrievalGenerationConfig(left, right persistence.RetrievalGenerationConfig) bool {
	return left.GenerationID == right.GenerationID && left.CollectionAlias == right.CollectionAlias &&
		left.CollectionName == right.CollectionName && left.EmbeddingModel == right.EmbeddingModel &&
		left.EmbeddingVersion == right.EmbeddingVersion && left.Dimensions == right.Dimensions &&
		left.SparseEncoderVersion == right.SparseEncoderVersion
}

type retrievalGenerationScanner interface {
	Scan(...any) error
}

func scanRetrievalGeneration(row retrievalGenerationScanner) (persistence.RetrievalGeneration, error) {
	var generation persistence.RetrievalGeneration
	var activatedAt, retiredAt *time.Time
	err := row.Scan(
		&generation.GenerationID, &generation.CollectionAlias, &generation.CollectionName,
		&generation.EmbeddingModel, &generation.EmbeddingVersion, &generation.Dimensions,
		&generation.SparseEncoderVersion, &generation.Status, &generation.CreatedAt,
		&activatedAt, &retiredAt,
	)
	if err != nil {
		return persistence.RetrievalGeneration{}, err
	}
	generation.ActivatedAt = activatedAt
	generation.RetiredAt = retiredAt
	return generation, nil
}
