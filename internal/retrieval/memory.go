package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/objectstore"
)

// MemoryRetrievalRecord binds one active canonical memory to the exact
// immutable object the database has authorized for derived retrieval.
type MemoryRetrievalRecord struct {
	Memory     contracts.MemoryRecord
	Authority  objectstore.Authority
	Descriptor objectstore.Descriptor
	Evidence   objectstore.Evidence
}

// ActiveMemorySource exposes a canonical memory lookup. The outbox event and
// any vector-store payload are only delivery hints, never source material.
type ActiveMemorySource interface {
	GetActiveMemory(context.Context, string) (MemoryRetrievalRecord, bool, error)
}

// MemoryBodyReader is purposefully narrower than a generic object-store
// client: retrieval can request only a pre-authorized, verified body.
type MemoryBodyReader interface {
	ReadVerified(context.Context, objectstore.Authority, objectstore.Descriptor, int64) ([]byte, objectstore.Evidence, error)
}

// BuildMemorySource derives a redacted, bounded memory card from canonical
// metadata and an integrity-verified object body. It never accepts raw event
// content or a body supplied by a vector-store payload.
func BuildMemorySource(ctx context.Context, record MemoryRetrievalRecord, reader MemoryBodyReader) (CardSource, error) {
	if reader == nil {
		return CardSource{}, fmt.Errorf("memory body reader is required")
	}
	if err := contracts.ValidateMemoryRecord(record.Memory); err != nil || record.Memory.Status != "active" {
		return CardSource{}, fmt.Errorf("memory is not an active canonical retrieval source")
	}
	if record.Memory.TenantID != "tenant_"+record.Authority.TenantID ||
		record.Memory.RepositoryID != "repo_"+record.Authority.RepositoryID ||
		record.Memory.ContentHash != descriptorHash(record.Descriptor) ||
		record.Descriptor.SizeBytes < 1 || record.Descriptor.SizeBytes > MaxCardTextBytes {
		return CardSource{}, fmt.Errorf("memory artifact binding is invalid")
	}
	body, evidence, err := reader.ReadVerified(ctx, record.Authority, record.Descriptor, MaxCardTextBytes)
	if err != nil {
		return CardSource{}, fmt.Errorf("read verified memory body: %w", err)
	}
	if !matchesCanonicalMemoryEvidence(record.Evidence, evidence) {
		return CardSource{}, fmt.Errorf("verified memory object evidence does not match canonical record")
	}
	derivedBody, err := PrepareMemoryBody(record.Descriptor.MediaType, body)
	if err != nil {
		return CardSource{}, err
	}
	derivedDigest := sha256.Sum256([]byte(derivedBody))
	sourceHash, err := memorySourceHash(record.Memory, derivedBody)
	if err != nil {
		return CardSource{}, err
	}
	return CardSource{
		TenantID:       record.Memory.TenantID,
		RepositoryID:   record.Memory.RepositoryID,
		EntityID:       record.Memory.MemoryID,
		ArtifactFamily: "memory",
		SourceHash:     sourceHash,
		// The memory record is read from PostgreSQL, so the retrieval authority
		// is canonical. Its human/policy promotion class remains provenance,
		// not a retrieval authorization category.
		AuthorityClass: "canonical",
		Status:         "active",
		Title:          "memory: " + record.Memory.Kind,
		Body:           derivedBody,
		ProofRefs: []string{
			"artifact:" + record.Memory.ContentArtifactID,
			"memory:" + record.Memory.MemoryID,
			"memory_body_hash:sha256:" + hex.EncodeToString(derivedDigest[:]),
			"memory_body_policy:" + MemoryBodyPolicyVersion,
			"memory_candidate:" + record.Memory.SourceCandidateID,
			"memory_content_hash:" + record.Memory.ContentHash,
			"memory_promotion_authority:" + record.Memory.AuthorityClass,
		},
	}, nil
}

func memorySourceHash(memory contracts.MemoryRecord, derivedBody string) (string, error) {
	bodyDigest := sha256.Sum256([]byte(derivedBody))
	payload := struct {
		MemoryID          string   `json:"memory_id"`
		SourceCandidateID string   `json:"source_candidate_id"`
		Kind              string   `json:"kind"`
		Status            string   `json:"status"`
		Version           int      `json:"version"`
		ContentArtifactID string   `json:"content_artifact_id"`
		ContentHash       string   `json:"content_hash"`
		AuthorityClass    string   `json:"authority_class"`
		PromotedBy        string   `json:"promoted_by"`
		PromotionReason   string   `json:"promotion_reason"`
		PromotedAt        string   `json:"promoted_at"`
		ExpiresAt         *string  `json:"expires_at,omitempty"`
		Supersedes        []string `json:"supersedes"`
		BodyPolicy        string   `json:"body_policy"`
		BodyHash          string   `json:"body_hash"`
	}{
		MemoryID: memory.MemoryID, SourceCandidateID: memory.SourceCandidateID,
		Kind: memory.Kind, Status: memory.Status, Version: memory.Version,
		ContentArtifactID: memory.ContentArtifactID, ContentHash: memory.ContentHash,
		AuthorityClass: memory.AuthorityClass, PromotedBy: memory.PromotedBy,
		PromotionReason: memory.PromotionReason,
		PromotedAt:      memory.PromotedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		Supersedes:      append([]string(nil), memory.Supersedes...),
		BodyPolicy:      MemoryBodyPolicyVersion,
		BodyHash:        "sha256:" + hex.EncodeToString(bodyDigest[:]),
	}
	if memory.ExpiresAt != nil {
		value := memory.ExpiresAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
		payload.ExpiresAt = &value
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode canonical memory source: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func descriptorHash(descriptor objectstore.Descriptor) string {
	return "sha256:" + strings.ToLower(hex.EncodeToString(descriptor.SHA256[:]))
}

func matchesCanonicalMemoryEvidence(expected, actual objectstore.Evidence) bool {
	if strings.TrimSpace(expected.ETag) == "" || expected.ETag != actual.ETag {
		return false
	}
	if expected.VersionID != "" && expected.VersionID != actual.VersionID {
		return false
	}
	return expected.ProviderChecksumSHA256 == "" || expected.ProviderChecksumSHA256 == actual.ProviderChecksumSHA256
}
