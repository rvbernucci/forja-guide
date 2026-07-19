package postgres

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/persistence"
)

func TestArtifactReconciliationFinalizesStoredIntentAndFailsIntegrity(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)

	intent := artifactPublicationIntentFixture(
		"artifact_reconciliation_success",
		"50000000-0000-4000-8000-000000000001",
		"reconciled-body",
	)
	metadata := testMetadata("artifact-reconciliation-original")
	if _, _, err := store.PrepareArtifactPublication(t.Context(), intent, metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkArtifactPublicationUploading(t.Context(), intent, metadata); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.ListArtifactReconciliationCandidates(t.Context(), time.Now().Add(time.Hour), 10)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("reconciliation candidates=%#v err=%v", candidates, err)
	}
	if candidates[0].Publication.Intent.Kind != intent.Kind ||
		candidates[0].Publication.Intent.Provenance.SourceType != intent.Provenance.SourceType {
		t.Fatalf("stored intent=%#v", candidates[0].Publication.Intent)
	}
	digest, _ := hexDigest(intent.ContentHash)
	reconciliationMetadata := knowledgeMetadata("artifact-reconciliation-complete", "system", "artifact-reconciler")
	artifact, err := store.CompleteArtifactReconciliation(t.Context(), intent.OperationID, persistence.ArtifactEvidence{
		ObjectKey:              artifactObjectKey(DefaultTenantID, DefaultRepositoryID, digest),
		ETag:                   "\"reconciled-etag\"",
		VersionID:              "reconciled-version",
		ProviderChecksumSHA256: base64.StdEncoding.EncodeToString(digest),
	}, reconciliationMetadata)
	if err != nil || artifact.ArtifactID != intent.ArtifactID || artifact.Kind != intent.Kind {
		t.Fatalf("reconciled artifact=%#v err=%v", artifact, err)
	}
	replayed, err := store.CompleteArtifactReconciliation(t.Context(), intent.OperationID, persistence.ArtifactEvidence{
		ObjectKey:              artifactObjectKey(DefaultTenantID, DefaultRepositoryID, digest),
		ETag:                   "\"reconciled-etag\"",
		VersionID:              "reconciled-version",
		ProviderChecksumSHA256: base64.StdEncoding.EncodeToString(digest),
	}, reconciliationMetadata)
	if err != nil || replayed.ArtifactID != artifact.ArtifactID {
		t.Fatalf("reconciliation replay=%#v err=%v", replayed, err)
	}

	brokenIntent := artifactPublicationIntentFixture(
		"artifact_reconciliation_broken",
		"50000000-0000-4000-8000-000000000002",
		"broken-body",
	)
	brokenMetadata := testMetadata("artifact-reconciliation-broken-original")
	if _, _, err := store.PrepareArtifactPublication(t.Context(), brokenIntent, brokenMetadata); err != nil {
		t.Fatal(err)
	}
	failed, err := store.FailArtifactReconciliation(
		t.Context(), brokenIntent.OperationID, "integrity",
		knowledgeMetadata("artifact-reconciliation-integrity", "system", "artifact-reconciler"),
	)
	if err != nil || failed.State != "failed" {
		t.Fatalf("integrity reconciliation=%#v err=%v", failed, err)
	}

	var reconciledEvents, failedEvents, receipts int
	if err := pool.QueryRow(t.Context(), `
		SELECT
			count(*) FILTER (WHERE event_type='artifact.publication_reconciled'),
			count(*) FILTER (WHERE event_type='artifact.publication_reconciliation_failed'),
			(SELECT count(*) FROM forja.idempotency_keys WHERE scope LIKE 'artifact_reconcile_%')
		FROM forja.events`,
	).Scan(&reconciledEvents, &failedEvents, &receipts); err != nil {
		t.Fatal(err)
	}
	if reconciledEvents != 1 || failedEvents != 1 || receipts != 2 {
		t.Fatalf("reconciliation evidence complete=%d failed=%d receipts=%d", reconciledEvents, failedEvents, receipts)
	}
}
