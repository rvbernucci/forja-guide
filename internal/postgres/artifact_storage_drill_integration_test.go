package postgres

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/knowledge"
	"github.com/rvbernucci/forja-guide/internal/objectstore"
)

var realS3DrillObjects = []struct {
	artifactID    string
	operationUUID string
	logicalPath   string
	body          string
}{
	{
		artifactID:    "artifact_s3_restore_contract",
		operationUUID: "70000000-0000-4000-8000-000000000001",
		logicalPath:   "evidence/contract.json",
		body:          `{"contract":"governed-artifact","version":"1.0"}`,
	},
	{
		artifactID:    "artifact_s3_restore_report",
		operationUUID: "70000000-0000-4000-8000-000000000002",
		logicalPath:   "evidence/report.txt",
		body:          "independently restored evidence report\n",
	},
	{
		artifactID:    "artifact_s3_restore_receipt",
		operationUUID: "70000000-0000-4000-8000-000000000003",
		logicalPath:   "evidence/receipt.txt",
		body:          "canonical receipt survives both storage planes\n",
	},
}

func TestRealS3ArtifactBundleRestoreDrill(t *testing.T) {
	endpoint := os.Getenv("FORJA_TEST_S3_ENDPOINT")
	bucket := os.Getenv("FORJA_TEST_S3_BUCKET")
	phase := os.Getenv("FORJA_TEST_S3_DRILL_PHASE")
	if endpoint == "" || bucket == "" || phase == "" {
		t.Skip("real S3 restore drill is not configured")
	}
	if phase != "seed" && phase != "verify" {
		t.Fatal("FORJA_TEST_S3_DRILL_PHASE must be seed or verify")
	}

	pool := integrationPool(t)
	if phase == "seed" {
		resetDatabase(t, pool)
		if err := Migrate(t.Context(), pool); err != nil {
			t.Fatalf("migrate restore drill database: %v", err)
		}
	}
	store := newIntegrationStore(t, pool)
	bodies, err := objectstore.New(t.Context(), objectstore.Config{
		Bucket: bucket, Region: "us-east-1", BaseEndpoint: endpoint, UsePathStyle: true,
	})
	if err != nil {
		t.Fatalf("open real S3 adapter: %v", err)
	}

	if phase == "seed" {
		seedRealS3ArtifactBundle(t, store, bodies)
	}
	verifyRealS3ArtifactBundle(t, pool, bodies)
}

func seedRealS3ArtifactBundle(t *testing.T, store *Store, bodies *objectstore.Store) {
	t.Helper()
	service, err := knowledge.NewService(store, bodies)
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]contracts.ArtifactBundleEntry, 0, len(realS3DrillObjects))
	var total int64
	for index, item := range realS3DrillObjects {
		intent := artifactPublicationIntentFixture(item.artifactID, item.operationUUID, item.body)
		intent.Metadata = map[string]any{"drill": "postgres-s3-restore"}
		metadata := testMetadata(fmt.Sprintf("real-s3-publish-%d", index))
		artifact, err := service.PublishArtifact(t.Context(), knowledge.PublishArtifactCommand{
			Intent: intent, Metadata: metadata, Body: bytes.NewReader([]byte(item.body)),
		})
		if err != nil {
			t.Fatalf("publish %s: %v", item.artifactID, err)
		}
		entries = append(entries, contracts.ArtifactBundleEntry{
			LogicalPath: item.logicalPath, ArtifactID: artifact.ArtifactID,
			ContentHash: artifact.ContentHash, SizeBytes: *artifact.SizeBytes,
			MediaType: artifact.MediaType,
		})
		total += *artifact.SizeBytes
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].LogicalPath < entries[right].LogicalPath
	})
	if _, err := store.CreateArtifactBundleManifest(t.Context(), contracts.ArtifactBundleManifest{
		ManifestID: "manifest_70000000-0000-4000-8000-000000000004",
		Family:     "evidence", Entries: entries, TotalSizeBytes: total,
		SourceRefs: []string{"sprint-07-real-s3-restore-drill"},
		CreatedBy:  "integration-suite",
	}, testMetadata("real-s3-create-manifest")); err != nil {
		t.Fatalf("create restored evidence manifest: %v", err)
	}
}

func verifyRealS3ArtifactBundle(t *testing.T, pool *pgxpool.Pool, bodies *objectstore.Store) {
	t.Helper()
	var entries, receipts, events int
	if err := pool.QueryRow(t.Context(), `
		SELECT
			(SELECT count(*) FROM forja.artifact_bundle_entries WHERE manifest_id='manifest_70000000-0000-4000-8000-000000000004'),
			(SELECT count(*) FROM forja.idempotency_keys WHERE scope LIKE 'artifact_publication:%' OR scope LIKE 'artifact_manifest_create:%'),
			(SELECT count(*) FROM forja.events WHERE aggregate_type IN ('artifact', 'artifact_operation', 'artifact_manifest'))
	`).Scan(&entries, &receipts, &events); err != nil {
		t.Fatalf("inspect restored canonical bundle: %v", err)
	}
	if entries != len(realS3DrillObjects) || receipts != len(realS3DrillObjects)+1 || events != len(realS3DrillObjects)*4+1 {
		t.Fatalf("restored bundle counts entries=%d receipts=%d events=%d", entries, receipts, events)
	}

	authority := objectstore.Authority{TenantID: DefaultTenantID, RepositoryID: DefaultRepositoryID}
	for _, item := range realS3DrillObjects {
		digest := sha256.Sum256([]byte(item.body))
		evidence, err := bodies.Verify(t.Context(), authority, objectstore.Descriptor{
			SHA256: digest, SizeBytes: int64(len(item.body)), MediaType: "text/plain",
		})
		if err != nil {
			t.Fatalf("verify restored body %s: %v", item.artifactID, err)
		}
		if evidence.ETag == "" || evidence.ObjectKey == "" {
			t.Fatalf("restored body %s lacks provider evidence", item.artifactID)
		}
	}
}
