package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/objectstore"
	"github.com/rvbernucci/forja-guide/internal/retrieval"
)

// GetActiveMemory returns only a memory record whose lifecycle, referenced
// artifact, and content-addressed object are all active in one repeatable-read
// canonical view. It deliberately returns no object body or object key.
func (s *Store) GetActiveMemory(ctx context.Context, memoryID string) (retrieval.MemoryRetrievalRecord, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return retrieval.MemoryRetrievalRecord{}, false, databaseError("postgres.GetActiveMemory.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var now time.Time
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return retrieval.MemoryRetrievalRecord{}, false, databaseError("postgres.GetActiveMemory.clock", err)
	}
	memory, err := loadMemoryRecord(ctx, tx, s.tenantID, s.repositoryID, memoryID, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return retrieval.MemoryRetrievalRecord{}, false, nil
	}
	if err != nil {
		return retrieval.MemoryRetrievalRecord{}, false, databaseError("postgres.GetActiveMemory.memory", err)
	}
	if err := contracts.ValidateMemoryRecord(memory); err != nil || memory.Status != "active" ||
		memory.ExpiresAt != nil && !memory.ExpiresAt.After(now.UTC()) {
		return retrieval.MemoryRetrievalRecord{}, false, nil
	}
	expectedDigest, err := decodeContentHash(memory.ContentHash)
	if err != nil {
		return retrieval.MemoryRetrievalRecord{}, false, nil
	}
	var digest []byte
	var sizeBytes int64
	var mediaType, providerETag string
	var providerVersion, providerChecksum *string
	err = tx.QueryRow(ctx, `
		SELECT artifact.content_sha256, artifact.size_bytes, artifact.media_type,
		       object.provider_etag, object.provider_version, object.provider_checksum_sha256
		FROM forja.artifacts AS artifact
		JOIN forja.artifact_objects AS object
		  ON object.tenant_id=artifact.tenant_id
		 AND object.repository_id=artifact.repository_id
		 AND object.content_sha256=artifact.content_sha256
		WHERE artifact.tenant_id=$1 AND artifact.repository_id=$2
		  AND artifact.artifact_id=$3 AND artifact.content_sha256=$4
		  AND artifact.status IN ('active','validated') AND artifact.tombstoned_at IS NULL
		  AND object.state='active' AND object.size_bytes=artifact.size_bytes
		  AND object.media_type=artifact.media_type`,
		s.tenantID, s.repositoryID, memory.ContentArtifactID, expectedDigest,
	).Scan(&digest, &sizeBytes, &mediaType, &providerETag, &providerVersion, &providerChecksum)
	if errors.Is(err, pgx.ErrNoRows) {
		return retrieval.MemoryRetrievalRecord{}, false, nil
	}
	if err != nil {
		return retrieval.MemoryRetrievalRecord{}, false, databaseError("postgres.GetActiveMemory.artifact", err)
	}
	if len(digest) != sha256.Size || providerETag == "" {
		return retrieval.MemoryRetrievalRecord{}, false, nil
	}
	var checksum [sha256.Size]byte
	copy(checksum[:], digest)
	if err := tx.Commit(ctx); err != nil {
		return retrieval.MemoryRetrievalRecord{}, false, databaseError("postgres.GetActiveMemory.commit", err)
	}
	return retrieval.MemoryRetrievalRecord{
		Memory: memory,
		Authority: objectstore.Authority{
			TenantID: s.tenantID, RepositoryID: s.repositoryID,
		},
		Descriptor: objectstore.Descriptor{SHA256: checksum, SizeBytes: sizeBytes, MediaType: mediaType},
		Evidence: objectstore.Evidence{
			ETag: providerETag, VersionID: nullableString(providerVersion), ProviderChecksumSHA256: nullableString(providerChecksum),
		},
	}, true, nil
}

func nullableString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

var _ retrieval.ActiveMemorySource = (*Store)(nil)
