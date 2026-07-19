package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/indexing"
	"github.com/rvbernucci/forja-guide/internal/persistence"
)

func TestIndexPublicationIsAtomicReplaySafeAndSupersedes(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store := newIntegrationStore(t, pool)
	first := indexPublicationFixture(t, pool, "first", strings.Repeat("a", 40))
	metadata := testMetadata("index-publish-first")
	published, err := store.PublishIndexSnapshot(t.Context(), first, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if published.Status != "active" {
		t.Fatalf("status=%s", published.Status)
	}
	replayed, err := store.PublishIndexSnapshot(t.Context(), first, metadata)
	if err != nil || replayed.SnapshotID != published.SnapshotID {
		t.Fatalf("replay=%#v err=%v", replayed, err)
	}
	conflict := first
	conflict.Bundle.Files = append([]contracts.FileCard(nil), first.Bundle.Files...)
	conflict.Bundle.Files[0].Generated = !conflict.Bundle.Files[0].Generated
	if _, err := store.PublishIndexSnapshot(t.Context(), conflict, testMetadata("index-publish-conflict")); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("conflicting snapshot error=%v", err)
	}
	second := indexPublicationFixture(t, pool, "second", strings.Repeat("b", 40))
	if _, err := store.PublishIndexSnapshot(t.Context(), second, testMetadata("index-publish-second")); err != nil {
		t.Fatal(err)
	}
	active, found, err := store.GetActiveIndexSnapshot(t.Context())
	if err != nil || !found || active.SnapshotID != second.Bundle.Snapshot.SnapshotID {
		t.Fatalf("active=%#v found=%v err=%v", active, found, err)
	}
	var activeCount, supersededCount, eventCount, outboxCount int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*) FILTER (WHERE status='active'),
		       count(*) FILTER (WHERE status='superseded')
		FROM forja.index_snapshots`).Scan(&activeCount, &supersededCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM forja.events WHERE aggregate_type='index_snapshot'`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*) FROM forja.outbox AS o
		JOIN forja.events AS e ON e.event_id=o.event_id
		WHERE e.aggregate_type='index_snapshot'`).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if activeCount != 1 || supersededCount != 1 || eventCount != 2 || outboxCount != 2 {
		t.Fatalf("active=%d superseded=%d events=%d outbox=%d", activeCount, supersededCount, eventCount, outboxCount)
	}
	if _, err := pool.Exec(t.Context(), `
		UPDATE forja.artifacts SET status='archived', tombstoned_at=clock_timestamp(), updated_at=clock_timestamp()
		WHERE artifact_id=$1`, *second.Bundle.Snapshot.ArtifactID); err == nil {
		t.Fatal("live snapshot artifact was not protected")
	}
	if err := RollbackLast(t.Context(), pool); err == nil {
		t.Fatal("migration rollback accepted canonical index history")
	}
	if _, found, err := store.GetActiveIndexSnapshot(t.Context()); err != nil || !found {
		t.Fatalf("failed rollback damaged active snapshot: found=%v err=%v", found, err)
	}
}

func TestConcurrentEquivalentIndexPublicationCreatesOneAuthority(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store := newIntegrationStore(t, pool)
	publication := indexPublicationFixture(t, pool, "concurrent", strings.Repeat("f", 40))
	type result struct {
		snapshot contracts.RepositorySnapshot
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, key := range []string{"index-concurrent-one", "index-concurrent-two"} {
		key := key
		go func() {
			<-start
			value, err := store.PublishIndexSnapshot(context.Background(), publication, testMetadata(key))
			results <- result{snapshot: value, err: err}
		}()
	}
	close(start)
	for range 2 {
		result := <-results
		if result.err != nil || result.snapshot.SnapshotID != publication.Bundle.Snapshot.SnapshotID {
			t.Fatalf("publication=%#v err=%v", result.snapshot, result.err)
		}
	}
	var snapshots, events, receipts int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM forja.index_snapshots`).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM forja.events WHERE aggregate_type='index_snapshot'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM forja.idempotency_keys WHERE scope LIKE 'index_publish:%'`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if snapshots != 1 || events != 1 || receipts != 2 {
		t.Fatalf("snapshots=%d events=%d receipts=%d", snapshots, events, receipts)
	}
}

func TestIndexPublicationRollsBackAllRowsOnInvalidArtifact(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store := newIntegrationStore(t, pool)
	publication := indexPublicationFixture(t, pool, "invalid-kind", strings.Repeat("c", 40))
	if _, err := pool.Exec(t.Context(), `UPDATE forja.artifacts SET kind='test_report' WHERE artifact_id=$1`, *publication.Bundle.Snapshot.ArtifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishIndexSnapshot(t.Context(), publication, testMetadata("index-invalid-artifact")); !fault.IsCode(err, fault.CodeInvalidArgument) {
		t.Fatalf("error=%v", err)
	}
	var snapshots, files, events int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM forja.index_snapshots`).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM forja.index_files`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM forja.events WHERE aggregate_type='index_snapshot'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if snapshots != 0 || files != 0 || events != 0 {
		t.Fatalf("partial publication snapshots=%d files=%d events=%d", snapshots, files, events)
	}
}

func TestIndexRelationClosureAndArtifactActivationAreSerialized(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	publication := indexPublicationFixture(t, pool, "race", strings.Repeat("7", 40))
	snapshot := publication.Bundle.Snapshot
	configurationHash, _ := decodeContentHash(snapshot.ConfigurationHash)
	adapterSetHash, _ := decodeContentHash(snapshot.AdapterSetHash)
	artifactHash, _ := decodeContentHash(*snapshot.ArtifactContentHash)
	adapters, _ := json.Marshal(snapshot.Adapters)
	requestHash := sha256.Sum256([]byte("race-request"))
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO forja.index_snapshots (
			tenant_id, repository_id, snapshot_id, source_commit, source_tree,
			configuration_sha256, adapter_set_sha256, adapters, request_sha256,
			status, version, file_count, symbol_count, relation_count,
			diagnostic_count, artifact_id, artifact_content_sha256, created_by,
			created_at, validated_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'active',1,0,0,0,0,$10,$11,$12,$13,$13,$13)`,
		DefaultTenantID, DefaultRepositoryID, snapshot.SnapshotID, snapshot.SourceCommit,
		snapshot.SourceTree, configurationHash, adapterSetHash, adapters, requestHash[:],
		*snapshot.ArtifactID, artifactHash, snapshot.CreatedBy, snapshot.CreatedAt,
	); err != nil {
		t.Fatal(err)
	}
	blockedContext, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	blocked := make(chan error, 1)
	go func() {
		_, updateErr := pool.Exec(blockedContext, `
			UPDATE forja.artifacts SET status='archived', tombstoned_at=clock_timestamp(), updated_at=clock_timestamp()
			WHERE artifact_id=$1`, *snapshot.ArtifactID)
		blocked <- updateErr
	}()
	if updateErr := <-blocked; updateErr == nil || !errors.Is(updateErr, context.DeadlineExceeded) {
		t.Fatalf("concurrent artifact mutation error=%v", updateErr)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		UPDATE forja.artifacts SET status='archived', tombstoned_at=clock_timestamp(), updated_at=clock_timestamp()
		WHERE artifact_id=$1`, *snapshot.ArtifactID); err == nil {
		t.Fatal("committed snapshot did not protect its artifact")
	}

	store := newIntegrationStore(t, pool)
	second := indexPublicationFixture(t, pool, "closure", strings.Repeat("6", 40))
	if _, err := store.PublishIndexSnapshot(t.Context(), second, testMetadata("index-closure")); err != nil {
		t.Fatal(err)
	}
	closureTx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	file := second.Bundle.Files[0]
	relationID := "relation_" + strings.Repeat("1", 64)
	fakeSource := "symbol_" + strings.Repeat("2", 64)
	target := "external_" + strings.Repeat("3", 64)
	evidence := sha256.Sum256([]byte("forged-relation"))
	adapter, _ := json.Marshal(second.AdapterRuns[0].Adapter)
	if _, err := closureTx.Exec(t.Context(), `
		INSERT INTO forja.index_relations (
			tenant_id, repository_id, snapshot_id, relation_id, source_entity_id,
			kind, resolution, target_entity_id, evidence_class, source_file_id,
			start_line, start_column, start_offset, end_line, end_column, end_offset,
			evidence_sha256, adapter
		) VALUES ($1,$2,$3,$4,$5,'calls','resolved',$6,'confirmed_static',$7,1,1,0,1,1,0,$8,$9)`,
		DefaultTenantID, DefaultRepositoryID, second.Bundle.Snapshot.SnapshotID,
		relationID, fakeSource, target, file.FileID, evidence[:], adapter,
	); err != nil {
		t.Fatal(err)
	}
	if err := closureTx.Commit(t.Context()); err == nil {
		t.Fatal("deferred relation closure accepted an unknown source entity")
	}
}

func indexPublicationFixture(t *testing.T, pool *pgxpool.Pool, suffix, commit string) persistence.IndexPublication {
	t.Helper()
	artifactHash := indexHash("artifact-" + suffix)
	digest, err := decodeContentHash(artifactHash)
	if err != nil {
		t.Fatal(err)
	}
	hexDigest := hex.EncodeToString(digest)
	operationID := fmt.Sprintf(
		"artifact_operation_%s-%s-4%s-8%s-%s",
		hexDigest[:8], hexDigest[8:12], hexDigest[13:16], hexDigest[17:20], hexDigest[20:32],
	)
	artifactID := "artifact_index_" + suffix
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	objectKey := fmt.Sprintf(
		"tenants/%s/repositories/%s/sha256/%s/%s",
		DefaultTenantID, DefaultRepositoryID, hexDigest[:2], hexDigest[2:],
	)
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.artifact_objects (
			tenant_id, repository_id, content_sha256, object_key, size_bytes,
			media_type, state, created_at, updated_at, verified_at, activated_at
		) VALUES ($1,$2,$3,$4,1,'application/json','active',$5,$5,$5,$5)`,
		DefaultTenantID, DefaultRepositoryID, digest, objectKey, now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.artifact_operations (
			tenant_id, repository_id, operation_id, artifact_id, content_sha256,
			expected_size_bytes, expected_media_type, request_sha256, intent,
			state, version, created_by, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,1,'application/json',$5,'{}','active',1,'integration-suite',$6,$6)`,
		DefaultTenantID, DefaultRepositoryID, operationID, artifactID, digest, now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.artifacts (
			tenant_id, repository_id, artifact_id, operation_id, kind, status,
			version, content_sha256, media_type, size_bytes, created_by,
			provenance, metadata, created_at, updated_at
		) VALUES ($1,$2,$3,$4,'index_snapshot','active',1,$5,'application/json',1,
			'integration-suite','{}','{}',$6,$6)`,
		DefaultTenantID, DefaultRepositoryID, artifactID, operationID, digest, now,
	); err != nil {
		t.Fatal(err)
	}
	descriptor := contracts.AdapterDescriptor{
		Name: "python", Version: "3.14",
		ConfigurationHash: indexHash("adapter-configuration"),
		CapabilityHash:    indexHash("adapter-capability"),
	}
	adapterSetHash, err := contracts.ComputeAdapterSetHash([]contracts.AdapterDescriptor{descriptor})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := contracts.RepositorySnapshot{
		SchemaVersion:       contracts.IndexSchemaVersion,
		TenantID:            "tenant_" + DefaultTenantID,
		RepositoryID:        "repo_" + DefaultRepositoryID,
		SourceCommit:        commit,
		SourceTree:          strings.Repeat("d", 40),
		ConfigurationHash:   indexHash("snapshot-configuration"),
		AdapterSetHash:      adapterSetHash,
		Adapters:            []contracts.AdapterDescriptor{descriptor},
		Status:              "active",
		Version:             1,
		Counts:              contracts.SnapshotCounts{Files: 1},
		ArtifactID:          &artifactID,
		ArtifactContentHash: &artifactHash,
		CreatedBy:           "integration-suite",
		CreatedAt:           now,
		ValidatedAt:         &now,
	}
	snapshot.SnapshotID = contracts.ComputeSnapshotID(snapshot)
	file := contracts.FileCard{
		SchemaVersion: contracts.IndexSchemaVersion,
		SnapshotID:    snapshot.SnapshotID, RepositoryID: snapshot.RepositoryID,
		SourceCommit: commit, Path: "app/main.py", GitBlobID: strings.Repeat("e", 40),
		SourceHash: indexHash("source-" + suffix), SizeBytes: 12, Language: "python",
		SymbolIDs: []string{}, Diagnostics: []contracts.DiagnosticSummary{},
	}
	file.FileID = contracts.ComputeFileID(file)
	file.LineageID = contracts.ComputeFileLineageID(file)
	return persistence.IndexPublication{
		Bundle: indexing.IndexBundle{
			Snapshot: snapshot, Files: []contracts.FileCard{file},
			Symbols: []contracts.SymbolCard{}, Relations: []contracts.RelationEvidence{},
		},
		AdapterRuns:   []persistence.IndexAdapterRun{{Adapter: descriptor, Status: "passed"}},
		Deltas:        []persistence.IndexDelta{{Ordinal: 0, ChangeKind: "added", EntityKind: "file", EntityID: file.FileID}},
		Invalidations: []persistence.IndexInvalidation{},
	}
}

func indexHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}
