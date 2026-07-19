package postgres

import (
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
	if err := store.MarkArtifactObjectPurged(
		t.Context(), candidates[0].ContentHash, candidates[0].ETag,
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
