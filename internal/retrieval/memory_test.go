package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/objectstore"
)

func TestBuildMemorySourceUsesOnlyVerifiedRedactedBody(t *testing.T) {
	t.Parallel()
	body := []byte("Use the governed route. Authorization: Bearer unsafe-token-value")
	record := validMemoryRetrievalRecord(body)
	reader := staticMemoryBodyReader{body: body}
	first, err := BuildMemorySource(t.Context(), record, reader)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildMemorySource(t.Context(), record, reader)
	if err != nil || first.SourceHash != second.SourceHash || strings.Contains(first.Body, "unsafe-token-value") || !strings.Contains(first.Body, "[REDACTED]") {
		t.Fatalf("first=%#v second=%#v err=%v", first, second, err)
	}
	if _, err := BuildCardText(first); err != nil {
		t.Fatal(err)
	}
}

func TestBuildMemorySourceFailsClosedForInvalidBindingOrRead(t *testing.T) {
	t.Parallel()
	body := []byte("safe memory")
	record := validMemoryRetrievalRecord(body)
	if _, err := BuildMemorySource(t.Context(), record, staticMemoryBodyReader{err: errors.New("unavailable")}); err == nil {
		t.Fatal("failed verified read was accepted")
	}
	record.Memory.Status = "expired"
	if _, err := BuildMemorySource(t.Context(), record, staticMemoryBodyReader{body: body}); err == nil {
		t.Fatal("non-active memory was accepted")
	}
	record = validMemoryRetrievalRecord(body)
	record.Memory.ContentHash = "sha256:" + strings.Repeat("a", 64)
	if _, err := BuildMemorySource(t.Context(), record, staticMemoryBodyReader{body: body}); err == nil {
		t.Fatal("mismatched object hash was accepted")
	}
	record = validMemoryRetrievalRecord(body)
	if _, err := BuildMemorySource(t.Context(), record, staticMemoryBodyReader{body: body, evidence: objectstore.Evidence{ETag: "wrong-version"}}); err == nil {
		t.Fatal("mismatched object evidence was accepted")
	}
}

func validMemoryRetrievalRecord(body []byte) MemoryRetrievalRecord {
	digest := sha256.Sum256(body)
	promotedAt := time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC)
	return MemoryRetrievalRecord{
		Memory: contracts.MemoryRecord{
			MemoryID: "memory_00000000-0000-4000-8000-000000000009", SchemaVersion: contracts.KnowledgeSchemaVersion,
			TenantID: retrievalTenantID, RepositoryID: retrievalRepositoryID,
			SourceCandidateID: "memory_candidate_00000000-0000-4000-8000-000000000008",
			Kind:              "lesson", Status: "active", Version: 1,
			ContentArtifactID: "artifact_memory_body", ContentHash: "sha256:" + hex.EncodeToString(digest[:]),
			AuthorityClass: "human_approved", PromotedBy: "reviewer", PromotionReason: "verified for retrieval",
			PromotedAt: promotedAt, Supersedes: []string{},
		},
		Authority: objectstore.Authority{
			TenantID: strings.TrimPrefix(retrievalTenantID, "tenant_"), RepositoryID: strings.TrimPrefix(retrievalRepositoryID, "repo_"),
		},
		Descriptor: objectstore.Descriptor{SHA256: digest, SizeBytes: int64(len(body)), MediaType: "text/plain"},
		Evidence:   objectstore.Evidence{ETag: "verified"},
	}
}

type staticMemoryBodyReader struct {
	body     []byte
	err      error
	evidence objectstore.Evidence
}

func (reader staticMemoryBodyReader) ReadVerified(_ context.Context, _ objectstore.Authority, _ objectstore.Descriptor, _ int64) ([]byte, objectstore.Evidence, error) {
	if reader.err != nil {
		return nil, objectstore.Evidence{}, reader.err
	}
	evidence := reader.evidence
	if evidence.ETag == "" {
		evidence.ETag = "verified"
	}
	return reader.body, evidence, nil
}
