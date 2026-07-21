package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	knowledgeConversationID = "conversation_10000000-0000-4000-8000-000000000001"
	knowledgeMessageID      = "message_10000000-0000-4000-8000-000000000002"
	knowledgeArtifactID     = "artifact_knowledge_fixture"
	knowledgeOperationID    = "artifact_operation_10000000-0000-4000-8000-000000000003"
	knowledgeCandidateID    = "memory_candidate_10000000-0000-4000-8000-000000000004"
	knowledgeMemoryID       = "memory_10000000-0000-4000-8000-000000000005"
	knowledgeManifestID     = "manifest_10000000-0000-4000-8000-000000000006"
)

func TestGovernedKnowledgePersistenceAndRollbackBoundary(t *testing.T) {
	pool := migratedPool(t)
	if err := VerifySchema(t.Context(), pool, DefaultTenantID, DefaultRepositoryID); err != nil {
		t.Fatalf("verify Sprint 07 schema: %v", err)
	}
	seedKnowledgeFixture(t, pool)

	if _, err := pool.Exec(t.Context(), `
		UPDATE forja.messages SET author_id='mutated'
		WHERE tenant_id=$1 AND repository_id=$2 AND message_id=$3`,
		DefaultTenantID, DefaultRepositoryID, knowledgeMessageID,
	); err == nil {
		t.Fatal("append-only message accepted an update")
	}

	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.message_parts (
			tenant_id, repository_id, message_id, part_id, ordinal, kind,
			artifact_id, content_sha256, media_type, size_bytes
		) VALUES (
			'20000000-0000-4000-8000-000000000001',
			'20000000-0000-4000-8000-000000000002',
			$1, 'part_20000000-0000-4000-8000-000000000003', 1, 'text',
			$2, decode(repeat('ab', 32), 'hex'), 'text/plain', 12
		)`, knowledgeMessageID, knowledgeArtifactID,
	); err == nil {
		t.Fatal("cross-repository message-to-artifact reference passed")
	}

	if err := RollbackLast(t.Context(), pool); err != nil {
		t.Fatalf("rollback unused migration 012: %v", err)
	}
	if err := RollbackLast(t.Context(), pool); err != nil {
		t.Fatalf("rollback unused migration 011: %v", err)
	}
	if err := RollbackLast(t.Context(), pool); err != nil {
		t.Fatalf("rollback unused migration 010: %v", err)
	}
	if err := RollbackLast(t.Context(), pool); err != nil {
		t.Fatalf("rollback unused migration 009: %v", err)
	}
	if err := RollbackLast(t.Context(), pool); err != nil {
		t.Fatalf("rollback unused migration 008: %v", err)
	}
	if err := RollbackLast(t.Context(), pool); err == nil {
		t.Fatal("migration 007 rollback discarded canonical knowledge history")
	}
	var version int
	if err := pool.QueryRow(t.Context(), `
		SELECT max(version) FROM forja.schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 7 {
		t.Fatalf("schema version after refused rollback = %d, want 7", version)
	}
}

func TestGovernedKnowledgeDeferredIntegrityFailsClosed(t *testing.T) {
	pool := migratedPool(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	tx, err := pool.BeginTx(t.Context(), pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO forja.artifact_objects (
			tenant_id, repository_id, content_sha256, object_key, size_bytes,
			media_type, state, created_at, updated_at
		) VALUES (
			$1::uuid, $2::uuid, decode(repeat('cd', 32), 'hex'),
			'tenants/' || $1::uuid::text || '/repositories/' || $2::uuid::text ||
			'/sha256/cd/' || repeat('cd', 31),
			4, 'text/plain', 'reserved', $3, $3
		)`, DefaultTenantID, DefaultRepositoryID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO forja.artifact_operations (
			tenant_id, repository_id, operation_id, artifact_id, content_sha256,
			expected_size_bytes, expected_media_type, request_sha256, state, version, created_by,
			created_at, updated_at
		) VALUES (
			$1, $2, 'artifact_operation_20000000-0000-4000-8000-000000000004',
			'artifact_inactive_fixture', decode(repeat('cd', 32), 'hex'),
			4, 'text/plain', decode(repeat('11', 32), 'hex'),
			'reserved', 1, 'integration-suite', $3, $3
		)`, DefaultTenantID, DefaultRepositoryID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO forja.artifacts (
			tenant_id, repository_id, artifact_id, operation_id, kind, status,
			version, content_sha256, media_type, size_bytes, created_by,
			provenance, created_at, updated_at
		) VALUES (
			$1, $2, 'artifact_inactive_fixture',
			'artifact_operation_20000000-0000-4000-8000-000000000004',
			'test_report', 'active', 1, decode(repeat('cd', 32), 'hex'),
			'text/plain', 4, 'integration-suite',
			'{"source_type":"test","source_refs":["fixture"]}'::jsonb, $3, $3
		)`, DefaultTenantID, DefaultRepositoryID, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err == nil {
		t.Fatal("artifact committed without an active verified object operation")
	}
}

func seedKnowledgeFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	tx, err := pool.BeginTx(t.Context(), pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	statements := []string{
		`INSERT INTO forja.artifact_objects (
			tenant_id, repository_id, content_sha256, object_key, size_bytes,
			media_type, state, provider_checksum_sha256, provider_etag,
			created_at, updated_at, verified_at, activated_at
		) VALUES (
			$1::uuid, $2::uuid, decode(repeat('ab', 32), 'hex'),
			'tenants/' || $1::uuid::text || '/repositories/' || $2::uuid::text ||
			'/sha256/ab/' || repeat('ab', 31),
			12, 'text/plain', 'active', repeat('q', 43) || '=', 'fixture-etag',
			$3, $3, $3, $3
		)`,
		`INSERT INTO forja.artifact_operations (
			tenant_id, repository_id, operation_id, artifact_id, content_sha256,
			expected_size_bytes, expected_media_type, request_sha256, state, version, created_by,
			created_at, updated_at
		) VALUES (
			$1, $2, '` + knowledgeOperationID + `', '` + knowledgeArtifactID + `',
			decode(repeat('ab', 32), 'hex'), 12, 'text/plain',
			decode(repeat('11', 32), 'hex'), 'active', 1,
			'integration-suite', $3, $3
		)`,
		`INSERT INTO forja.artifacts (
			tenant_id, repository_id, artifact_id, operation_id, kind, status,
			version, content_sha256, media_type, size_bytes, created_by,
			provenance, created_at, updated_at
		) VALUES (
			$1, $2, '` + knowledgeArtifactID + `', '` + knowledgeOperationID + `',
			'test_report', 'active', 1, decode(repeat('ab', 32), 'hex'),
			'text/plain', 12, 'integration-suite',
			'{"source_type":"test","source_refs":["fixture"]}'::jsonb, $3, $3
		)`,
		`INSERT INTO forja.conversations (
			tenant_id, repository_id, conversation_id, status, version,
			retention_class, created_by, created_at, updated_at
		) VALUES ($1, $2, '` + knowledgeConversationID + `', 'active', 1,
			'project', 'integration-suite', $3, $3)`,
		`INSERT INTO forja.messages (
			tenant_id, repository_id, message_id, conversation_id,
			sequence_number, role, author_id, content_sha256, created_at
		) VALUES ($1, $2, '` + knowledgeMessageID + `', '` + knowledgeConversationID + `',
			1, 'human', 'integration-suite', decode(repeat('ef', 32), 'hex'), $3)`,
		`INSERT INTO forja.message_parts (
			tenant_id, repository_id, message_id, part_id, ordinal, kind,
			artifact_id, content_sha256, media_type, size_bytes
		) VALUES ($1, $2, '` + knowledgeMessageID + `',
			'part_10000000-0000-4000-8000-000000000007', 0, 'text',
			'` + knowledgeArtifactID + `', decode(repeat('ab', 32), 'hex'),
			'text/plain', 12)`,
		`INSERT INTO forja.message_citations (
			tenant_id, repository_id, message_id, citation_id, ordinal,
			source_artifact_id, source_content_sha256, locator_kind, locator_value
		) VALUES ($1, $2, '` + knowledgeMessageID + `',
			'citation_10000000-0000-4000-8000-000000000008', 0,
			'` + knowledgeArtifactID + `', decode(repeat('ab', 32), 'hex'),
			'whole', 'whole artifact')`,
		`INSERT INTO forja.artifact_bundle_manifests (
			tenant_id, repository_id, manifest_id, family, total_size_bytes,
			entry_count, source_refs, created_by, created_at
		) VALUES ($1, $2, '` + knowledgeManifestID + `', 'evidence', 12, 1,
			'["fixture"]'::jsonb, 'integration-suite', $3)`,
		`INSERT INTO forja.artifact_bundle_entries (
			tenant_id, repository_id, manifest_id, ordinal, logical_path,
			artifact_id, content_sha256, size_bytes, media_type
		) VALUES ($1, $2, '` + knowledgeManifestID + `', 0, 'reports/result.txt',
			'` + knowledgeArtifactID + `', decode(repeat('ab', 32), 'hex'),
			12, 'text/plain')`,
		`INSERT INTO forja.memory_candidates (
			tenant_id, repository_id, candidate_id, conversation_id, kind,
			proposed_artifact_id, proposed_content_sha256, status, version,
			proposed_by, proposed_at
		) VALUES ($1, $2, '` + knowledgeCandidateID + `', '` + knowledgeConversationID + `',
			'lesson', '` + knowledgeArtifactID + `', decode(repeat('ab', 32), 'hex'),
			'proposed', 1, 'assistant', $3)`,
		`INSERT INTO forja.memory_candidate_sources (
			tenant_id, repository_id, candidate_id, ordinal, message_id
		) VALUES ($1, $2, '` + knowledgeCandidateID + `', 0, '` + knowledgeMessageID + `')`,
	}
	for _, statement := range statements {
		arguments := []any{DefaultTenantID, DefaultRepositoryID}
		if strings.Contains(statement, "$3") {
			arguments = append(arguments, now)
		}
		if _, err := tx.Exec(t.Context(), statement, arguments...); err != nil {
			t.Fatalf("seed knowledge fixture: %v", err)
		}
	}
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO forja.memory_records (
			tenant_id, repository_id, memory_id, source_candidate_id, kind,
			status, version, content_artifact_id, content_sha256, authority_class,
			promoted_by, promotion_reason, promoted_at
		) VALUES ($1, $2, $3, $4, 'lesson', 'active', 1, $5,
			decode(repeat('ab', 32), 'hex'), 'human_approved',
			'integration-reviewer', 'verified durable lesson', $6)`,
		DefaultTenantID, DefaultRepositoryID, knowledgeMemoryID,
		knowledgeCandidateID, knowledgeArtifactID, now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `
		UPDATE forja.memory_candidates
		SET status='promoted', version=2, memory_id=$3, resolved_by='integration-reviewer',
			resolution_reason='verified durable lesson', resolved_at=$4
		WHERE tenant_id=$1 AND repository_id=$2 AND candidate_id=$5`,
		DefaultTenantID, DefaultRepositoryID, knowledgeMemoryID, now, knowledgeCandidateID,
	); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit governed knowledge fixture: %v", err)
	}
}
