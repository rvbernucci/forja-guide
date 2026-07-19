package postgres

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/fault"
)

func TestArtifactRetentionTombstonePrecedesPurge(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	artifact := publishKnowledgeArtifact(
		t, store, "artifact_retention_fixture",
		"70000000-0000-4000-8000-000000000001",
		"retention-body", "test_report",
	)
	if _, err := store.TombstoneArtifact(
		t.Context(), artifact.ArtifactID, 1,
		knowledgeMetadata("retention-agent-denied", "agent", "agent"),
	); !fault.IsCode(err, fault.CodePermissionDenied) {
		t.Fatalf("agent tombstone error=%v", err)
	}
	tombstoned, err := store.TombstoneArtifact(
		t.Context(), artifact.ArtifactID, 1,
		knowledgeMetadata("retention-human-tombstone", "human", "reviewer"),
	)
	if err != nil || tombstoned.Status != "archived" {
		t.Fatalf("tombstoned artifact=%#v err=%v", tombstoned, err)
	}
	candidates, err := store.ListArtifactRetentionCandidates(t.Context(), time.Now().Add(time.Hour), 10)
	if err != nil || len(candidates) != 1 || candidates[0].ContentHash != artifact.ContentHash {
		t.Fatalf("retention candidates=%#v err=%v", candidates, err)
	}
	republish := artifactPublicationIntentFixture(
		"artifact_retention_republish",
		"70000000-0000-4000-8000-000000000002",
		"retention-body",
	)
	if _, _, err := store.PrepareArtifactPublication(
		t.Context(), republish, testMetadata("retention-republish-rejected"),
	); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("republish tombstoned content error=%v", err)
	}
	if err := store.MarkArtifactObjectPurged(
		t.Context(), candidates[0].ContentHash, candidates[0].ETag, "wrong-version",
		knowledgeMetadata("retention-wrong-version", "system", "retention-worker"),
	); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("wrong-version purge error=%v", err)
	}
	if err := store.MarkArtifactObjectPurged(
		t.Context(), candidates[0].ContentHash, candidates[0].ETag, candidates[0].VersionID,
		knowledgeMetadata("retention-system-purge", "system", "retention-worker"),
	); err != nil {
		t.Fatal(err)
	}
	var state string
	var tombstoneEventBeforePurge bool
	if err := pool.QueryRow(t.Context(), `
		SELECT object.state,
		       (SELECT tombstone.occurred_at <= purged.occurred_at
		        FROM forja.events AS tombstone, forja.events AS purged
		        WHERE tombstone.event_type='artifact.tombstoned'
		          AND purged.event_type='artifact.object_purged'
		        LIMIT 1)
		FROM forja.artifact_objects AS object
		WHERE object.tenant_id=$1 AND object.repository_id=$2
		  AND encode(object.content_sha256, 'hex')=$3`,
		DefaultTenantID, DefaultRepositoryID, artifact.ContentHash[len("sha256:"):],
	).Scan(&state, &tombstoneEventBeforePurge); err != nil {
		t.Fatal(err)
	}
	if state != "purged" || !tombstoneEventBeforePurge {
		t.Fatalf("state=%s tombstone_before_purge=%v", state, tombstoneEventBeforePurge)
	}
}

func TestArtifactReferenceCreationSerializesAgainstTombstone(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	artifact := publishKnowledgeArtifact(
		t, store, "artifact_retention_race",
		"70000000-0000-4000-8000-000000000010",
		"retention-race-body", "test_report",
	)
	digest, err := decodeContentHash(artifact.ContentHash)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if err := verifyExactArtifact(
		t.Context(), tx, DefaultTenantID, DefaultRepositoryID,
		artifact.ArtifactID, artifact.ContentHash, *artifact.SizeBytes, artifact.MediaType,
	); err != nil {
		t.Fatal(err)
	}
	tombstoneResult := make(chan error, 1)
	go func() {
		_, tombstoneErr := store.TombstoneArtifact(
			t.Context(), artifact.ArtifactID, 1,
			knowledgeMetadata("retention-race-tombstone", "human", "reviewer"),
		)
		tombstoneResult <- tombstoneErr
	}()
	select {
	case tombstoneErr := <-tombstoneResult:
		t.Fatalf("tombstone bypassed reference lock: %v", tombstoneErr)
	case <-time.After(100 * time.Millisecond):
	}
	sourceRefs, _ := json.Marshal([]string{"retention-race"})
	now := time.Now().UTC()
	manifestID := "manifest_70000000-0000-4000-8000-000000000011"
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO forja.artifact_bundle_manifests (
			tenant_id, repository_id, manifest_id, family, total_size_bytes,
			entry_count, source_refs, created_by, created_at
		) VALUES ($1, $2, $3, 'evidence', $4, 1, $5, 'race-test', $6)`,
		DefaultTenantID, DefaultRepositoryID, manifestID, *artifact.SizeBytes, sourceRefs, now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO forja.artifact_bundle_entries (
			tenant_id, repository_id, manifest_id, ordinal, logical_path,
			artifact_id, content_sha256, size_bytes, media_type
		) VALUES ($1, $2, $3, 0, 'race/evidence.txt', $4, $5, $6, $7)`,
		DefaultTenantID, DefaultRepositoryID, manifestID, artifact.ArtifactID,
		digest, *artifact.SizeBytes, artifact.MediaType,
	); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-tombstoneResult; !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("tombstone after concurrent reference error=%v", err)
	}
}
