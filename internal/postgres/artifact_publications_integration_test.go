package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/knowledge"
	"github.com/rvbernucci/forja-guide/internal/objectstore"
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

func TestConcurrentKnowledgeServicePublicationActivatesOneCanonicalArtifact(t *testing.T) {
	pool := migratedPool(t)
	repository := newIntegrationStore(t, pool)
	body := []byte("concurrent end-to-end evidence")
	intent := artifactPublicationIntentFixture(
		"artifact_concurrent_service",
		"30000000-0000-4000-8000-000000000020",
		string(body),
	)
	metadata := testMetadata("artifact-concurrent-service")
	bodies := &concurrentBodyStore{}
	service, err := knowledge.NewService(repository, bodies)
	if err != nil {
		t.Fatal(err)
	}
	const competitors = 8
	results := make(chan error, competitors)
	var group sync.WaitGroup
	for range competitors {
		group.Add(1)
		go func() {
			defer group.Done()
			artifact, publishErr := service.PublishArtifact(t.Context(), knowledge.PublishArtifactCommand{
				Intent: intent, Metadata: metadata, Body: bytes.NewReader(body),
			})
			if publishErr == nil && (artifact.ArtifactID != intent.ArtifactID || artifact.ContentHash != intent.ContentHash) {
				publishErr = fmt.Errorf("unexpected artifact: %#v", artifact)
			}
			results <- publishErr
		}()
	}
	group.Wait()
	close(results)
	for resultErr := range results {
		if resultErr != nil {
			t.Fatal(resultErr)
		}
	}
	var operations, artifacts, objects int
	if err := pool.QueryRow(t.Context(), `
		SELECT
			(SELECT count(*) FROM forja.artifact_operations),
			(SELECT count(*) FROM forja.artifacts),
			(SELECT count(*) FROM forja.artifact_objects)`,
	).Scan(&operations, &artifacts, &objects); err != nil {
		t.Fatal(err)
	}
	if operations != 1 || artifacts != 1 || objects != 1 || bodies.created != 1 || bodies.calls < 1 {
		t.Fatalf("operations=%d artifacts=%d objects=%d body_calls=%d created=%d", operations, artifacts, objects, bodies.calls, bodies.created)
	}
}

type concurrentBodyStore struct {
	mutex   sync.Mutex
	body    []byte
	calls   int
	created int
}

func (s *concurrentBodyStore) Publish(
	_ context.Context,
	authority objectstore.Authority,
	descriptor objectstore.Descriptor,
	body io.ReadSeeker,
) (objectstore.Evidence, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.calls++
	value := make([]byte, descriptor.SizeBytes)
	if _, err := io.ReadFull(body, value); err != nil {
		return objectstore.Evidence{}, err
	}
	if digest := sha256.Sum256(value); digest != descriptor.SHA256 {
		return objectstore.Evidence{}, fmt.Errorf("body digest mismatch")
	}
	created := false
	if s.body == nil {
		s.body = append([]byte(nil), value...)
		s.created++
		created = true
	} else if !bytes.Equal(s.body, value) {
		return objectstore.Evidence{}, fmt.Errorf("conditional body mismatch")
	}
	return objectstore.Evidence{
		ObjectKey: artifactObjectKey(authority.TenantID, authority.RepositoryID, descriptor.SHA256[:]),
		ETag:      `"concurrent-etag"`, VersionID: "concurrent-version", Created: created,
	}, nil
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
