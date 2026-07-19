package postgres

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/persistence"
)

func TestArtifactPublicationSagaIsAtomicReplaySafeAndRecoverable(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	intent := artifactPublicationIntentFixture("artifact_saga_fixture", "30000000-0000-4000-8000-000000000001", "evidence")
	metadata := testMetadata("artifact-saga-idempotency")

	prepared, artifact, err := store.PrepareArtifactPublication(t.Context(), intent, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.State != "reserved" || artifact != nil {
		t.Fatalf("prepared publication = %#v artifact=%#v", prepared, artifact)
	}
	replayed, artifact, err := store.PrepareArtifactPublication(t.Context(), intent, metadata)
	if err != nil || replayed.State != "reserved" || artifact != nil {
		t.Fatalf("prepared replay = %#v artifact=%#v err=%v", replayed, artifact, err)
	}
	uploading, err := store.MarkArtifactPublicationUploading(t.Context(), intent, metadata)
	if err != nil || uploading.State != "uploading" || uploading.Version != 2 {
		t.Fatalf("uploading publication = %#v err=%v", uploading, err)
	}
	digest, _ := hexDigest(intent.ContentHash)
	evidence := persistence.ArtifactEvidence{
		ObjectKey: artifactObjectKey(DefaultTenantID, DefaultRepositoryID, digest),
		ETag:      `"artifact-etag"`, VersionID: "artifact-version",
		ProviderChecksumSHA256: base64.StdEncoding.EncodeToString(digest),
	}
	created, err := store.CompleteArtifactPublication(t.Context(), intent, evidence, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if created.ArtifactID != intent.ArtifactID || created.Status != "active" ||
		created.ContentHash != intent.ContentHash || created.SizeBytes == nil || *created.SizeBytes != intent.SizeBytes {
		t.Fatalf("created artifact = %#v", created)
	}
	replayedArtifact, err := store.CompleteArtifactPublication(t.Context(), intent, evidence, metadata)
	if err != nil || replayedArtifact.ArtifactID != created.ArtifactID || !replayedArtifact.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("artifact replay = %#v err=%v", replayedArtifact, err)
	}
	var operationEvents, artifactEvents, outboxRows, receipts int
	if err := pool.QueryRow(t.Context(), `
		SELECT
			count(*) FILTER (WHERE aggregate_type='artifact_operation'),
			count(*) FILTER (WHERE aggregate_type='artifact'),
			(SELECT count(*) FROM forja.outbox),
			(SELECT count(*) FROM forja.idempotency_keys WHERE scope LIKE 'artifact_publication:%')
		FROM forja.events`).Scan(&operationEvents, &artifactEvents, &outboxRows, &receipts); err != nil {
		t.Fatal(err)
	}
	if operationEvents != 3 || artifactEvents != 1 || outboxRows != 4 || receipts != 1 {
		t.Fatalf("saga evidence operation=%d artifact=%d outbox=%d receipts=%d", operationEvents, artifactEvents, outboxRows, receipts)
	}

	conflict := intent
	conflict.MediaType = "application/json"
	if _, _, err := store.PrepareArtifactPublication(t.Context(), conflict, metadata); err == nil {
		t.Fatal("same idempotency key accepted a changed artifact intent")
	}
}

func TestArtifactPublicationRetryAndConcurrentReservation(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	intent := artifactPublicationIntentFixture("artifact_retry_fixture", "30000000-0000-4000-8000-000000000002", "retry")
	metadata := testMetadata("artifact-retry-idempotency")

	const competitors = 8
	results := make(chan error, competitors)
	var group sync.WaitGroup
	for range competitors {
		group.Add(1)
		go func() {
			defer group.Done()
			publication, artifact, err := store.PrepareArtifactPublication(t.Context(), intent, metadata)
			if err == nil && (publication.State != "reserved" || artifact != nil) {
				err = fmt.Errorf("unexpected concurrent result: %#v %#v", publication, artifact)
			}
			results <- err
		}()
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var operations int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM forja.artifact_operations`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 1 {
		t.Fatalf("concurrent reservation created %d operations", operations)
	}
	if _, err := store.MarkArtifactPublicationUploading(t.Context(), intent, metadata); err != nil {
		t.Fatal(err)
	}
	failed, err := store.FailArtifactPublication(t.Context(), intent, "retryable_provider", metadata)
	if err != nil || failed.State != "reconciliation_required" {
		t.Fatalf("retryable failure = %#v err=%v", failed, err)
	}
	retrying, err := store.MarkArtifactPublicationUploading(t.Context(), intent, metadata)
	if err != nil || retrying.State != "uploading" {
		t.Fatalf("retrying publication = %#v err=%v", retrying, err)
	}
}

func artifactPublicationIntentFixture(artifactID, operationSuffix, body string) persistence.ArtifactPublicationIntent {
	digest := sha256.Sum256([]byte(body))
	return persistence.ArtifactPublicationIntent{
		OperationID: "artifact_operation_" + operationSuffix,
		ArtifactID:  artifactID,
		Kind:        "test_report",
		ContentHash: fmt.Sprintf("sha256:%x", digest),
		SizeBytes:   int64(len(body)),
		MediaType:   "text/plain",
		CreatedBy:   "integration-suite",
		Provenance: contracts.Provenance{
			SourceType: "test", SourceRefs: []string{"artifact-publication-integration"},
		},
		Metadata: map[string]any{"suite": "postgres"},
	}
}

func hexDigest(contentHash string) ([]byte, error) {
	return hex.DecodeString(strings.TrimPrefix(contentHash, "sha256:"))
}
