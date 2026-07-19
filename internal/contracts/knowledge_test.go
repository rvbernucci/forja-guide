package contracts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const (
	testTenantID       = "tenant_00000000-0000-4000-8000-000000000001"
	testRepositoryID   = "repo_00000000-0000-4000-8000-000000000002"
	testConversationID = "conversation_00000000-0000-4000-8000-000000000003"
	testMessageID      = "message_00000000-0000-4000-8000-000000000004"
	testArtifactID     = "artifact_00000000-0000-4000-8000-000000000005"
	testDigest         = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestRegistryValidatesKnowledgeContracts(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	message := validKnowledgeMessage(t, now)
	candidate := MemoryCandidate{
		CandidateID:   "memory_candidate_00000000-0000-4000-8000-000000000008",
		SchemaVersion: KnowledgeSchemaVersion, TenantID: testTenantID, RepositoryID: testRepositoryID,
		ConversationID: testConversationID, SourceMessageIDs: []string{testMessageID}, Kind: "lesson",
		ProposedArtifactID: testArtifactID, ProposedContentHash: testDigest, Status: "proposed", Version: 1,
		ProposedBy: "co-architect", ProposedAt: now,
	}
	memory := MemoryRecord{
		MemoryID: "memory_00000000-0000-4000-8000-000000000009", SchemaVersion: KnowledgeSchemaVersion,
		TenantID: testTenantID, RepositoryID: testRepositoryID, SourceCandidateID: candidate.CandidateID,
		Kind: "lesson", Status: "active", Version: 1, ContentArtifactID: testArtifactID,
		ContentHash: testDigest, AuthorityClass: "human_approved", PromotedBy: "reviewer",
		PromotionReason: "Evidence supports durable reuse", PromotedAt: now, Supersedes: []string{},
	}
	manifest := ArtifactBundleManifest{
		ManifestID: "manifest_00000000-0000-4000-8000-000000000010", SchemaVersion: KnowledgeSchemaVersion,
		TenantID: testTenantID, RepositoryID: testRepositoryID, Family: "evidence",
		Entries: []ArtifactBundleEntry{{
			LogicalPath: "reports/validation.json", ArtifactID: testArtifactID,
			ContentHash: testDigest, SizeBytes: 32, MediaType: "application/json",
		}},
		TotalSizeBytes: 32, SourceRefs: []string{"delivery_fixture"}, CreatedBy: "validator", CreatedAt: now,
	}
	conversation := Conversation{
		ConversationID: testConversationID, SchemaVersion: KnowledgeSchemaVersion,
		TenantID: testTenantID, RepositoryID: testRepositoryID, Status: "active", Version: 1,
		RetentionClass: "project", CreatedBy: "co-architect", CreatedAt: now, UpdatedAt: now,
	}
	for name, value := range map[string]any{
		"conversation.schema.json":             conversation,
		"message.schema.json":                  message,
		"memory-candidate.schema.json":         candidate,
		"memory-record.schema.json":            memory,
		"artifact-bundle-manifest.schema.json": manifest,
	} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if validateErr := registry.ValidateJSON(name, encoded); validateErr != nil {
			t.Fatalf("%s rejected canonical value: %v", name, validateErr)
		}
	}
	for name, validate := range map[string]func() error{
		"conversation": func() error { return ValidateConversation(conversation) },
		"message":      func() error { return ValidateMessage(message) },
		"candidate":    func() error { return ValidateMemoryCandidate(candidate) },
		"memory":       func() error { return ValidateMemoryRecord(memory) },
		"manifest":     func() error { return ValidateArtifactBundleManifest(manifest) },
	} {
		if err := validate(); err != nil {
			t.Fatalf("%s rejected canonical value: %v", name, err)
		}
	}
}

func TestKnowledgeSemanticValidationFailsClosed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	t.Run("message hash mismatch", func(t *testing.T) {
		message := validKnowledgeMessage(t, now)
		message.ContentHash = testDigest
		if err := ValidateMessage(message); err == nil {
			t.Fatal("message with mismatched content hash passed")
		}
	})
	t.Run("message part order", func(t *testing.T) {
		message := validKnowledgeMessage(t, now)
		message.ContentParts[0].Ordinal = 1
		if err := ValidateMessage(message); err == nil {
			t.Fatal("message with noncanonical part order passed")
		}
	})
	t.Run("message citations must be an array", func(t *testing.T) {
		message := validKnowledgeMessage(t, now)
		message.Citations = nil
		message.ContentHash, _ = ComputeMessageContentHash(message.ContentParts, message.Citations)
		if err := ValidateMessage(message); err == nil {
			t.Fatal("message with null citations passed")
		}
	})
	t.Run("message part kind", func(t *testing.T) {
		message := validKnowledgeMessage(t, now)
		message.ContentParts[0].Kind = "executable_instruction"
		message.ContentHash, _ = ComputeMessageContentHash(message.ContentParts, message.Citations)
		if err := ValidateMessage(message); err == nil {
			t.Fatal("message with unknown content-part kind passed")
		}
	})
	t.Run("citation locator kind", func(t *testing.T) {
		message := validKnowledgeMessage(t, now)
		message.Citations[0].Locator.Kind = "sql"
		message.ContentHash, _ = ComputeMessageContentHash(message.ContentParts, message.Citations)
		if err := ValidateMessage(message); err == nil {
			t.Fatal("message with unknown citation locator passed")
		}
	})
	t.Run("candidate self-authorizes", func(t *testing.T) {
		memoryID := "memory_00000000-0000-4000-8000-000000000009"
		candidate := MemoryCandidate{
			CandidateID:   "memory_candidate_00000000-0000-4000-8000-000000000008",
			SchemaVersion: KnowledgeSchemaVersion, TenantID: testTenantID, RepositoryID: testRepositoryID,
			ConversationID: testConversationID, SourceMessageIDs: []string{testMessageID}, Kind: "lesson",
			ProposedArtifactID: testArtifactID, ProposedContentHash: testDigest, Status: "proposed", Version: 1,
			ProposedBy: "agent", ProposedAt: now, MemoryID: &memoryID,
		}
		if err := ValidateMemoryCandidate(candidate); err == nil {
			t.Fatal("proposed candidate carrying a promoted memory passed")
		}
	})
	t.Run("memory kind", func(t *testing.T) {
		memory := MemoryRecord{
			MemoryID: "memory_00000000-0000-4000-8000-000000000009", SchemaVersion: KnowledgeSchemaVersion,
			TenantID: testTenantID, RepositoryID: testRepositoryID,
			SourceCandidateID: "memory_candidate_00000000-0000-4000-8000-000000000008",
			Kind:              "instruction", Status: "active", Version: 1, ContentArtifactID: testArtifactID,
			ContentHash: testDigest, AuthorityClass: "human_approved", PromotedBy: "reviewer",
			PromotionReason: "verified", PromotedAt: now, Supersedes: []string{},
		}
		if err := ValidateMemoryRecord(memory); err == nil {
			t.Fatal("memory with unknown kind passed")
		}
	})
	t.Run("memory supersedes must be an array", func(t *testing.T) {
		memory := MemoryRecord{
			MemoryID: "memory_00000000-0000-4000-8000-000000000009", SchemaVersion: KnowledgeSchemaVersion,
			TenantID: testTenantID, RepositoryID: testRepositoryID,
			SourceCandidateID: "memory_candidate_00000000-0000-4000-8000-000000000008",
			Kind:              "lesson", Status: "active", Version: 1, ContentArtifactID: testArtifactID,
			ContentHash: testDigest, AuthorityClass: "human_approved", PromotedBy: "reviewer",
			PromotionReason: "verified", PromotedAt: now,
		}
		if err := ValidateMemoryRecord(memory); err == nil {
			t.Fatal("memory with null supersedes passed")
		}
	})
	t.Run("candidate expiry precedes proposal", func(t *testing.T) {
		expires := now.Add(-time.Minute)
		candidate := MemoryCandidate{
			CandidateID:   "memory_candidate_00000000-0000-4000-8000-000000000008",
			SchemaVersion: KnowledgeSchemaVersion, TenantID: testTenantID, RepositoryID: testRepositoryID,
			ConversationID: testConversationID, SourceMessageIDs: []string{testMessageID}, Kind: "lesson",
			ProposedArtifactID: testArtifactID, ProposedContentHash: testDigest, Status: "proposed", Version: 1,
			ProposedBy: "agent", ProposedAt: now, ExpiresAt: &expires,
		}
		if err := ValidateMemoryCandidate(candidate); err == nil {
			t.Fatal("candidate expiring before proposal passed")
		}
	})
	t.Run("bundle traversal", func(t *testing.T) {
		manifest := ArtifactBundleManifest{
			ManifestID: "manifest_00000000-0000-4000-8000-000000000010", SchemaVersion: KnowledgeSchemaVersion,
			TenantID: testTenantID, RepositoryID: testRepositoryID, Family: "evidence",
			Entries: []ArtifactBundleEntry{{
				LogicalPath: "../secret", ArtifactID: testArtifactID,
				ContentHash: testDigest, SizeBytes: 1, MediaType: "text/plain",
			}},
			TotalSizeBytes: 1, SourceRefs: []string{"fixture"}, CreatedBy: "validator", CreatedAt: now,
		}
		if err := ValidateArtifactBundleManifest(manifest); err == nil {
			t.Fatal("bundle traversal passed")
		}
	})
	t.Run("bundle bare parent traversal", func(t *testing.T) {
		manifest := ArtifactBundleManifest{
			ManifestID: "manifest_00000000-0000-4000-8000-000000000010", SchemaVersion: KnowledgeSchemaVersion,
			TenantID: testTenantID, RepositoryID: testRepositoryID, Family: "evidence",
			Entries: []ArtifactBundleEntry{{
				LogicalPath: "..", ArtifactID: testArtifactID,
				ContentHash: testDigest, SizeBytes: 1, MediaType: "text/plain",
			}},
			TotalSizeBytes: 1, SourceRefs: []string{"fixture"}, CreatedBy: "validator", CreatedAt: now,
		}
		if err := ValidateArtifactBundleManifest(manifest); err == nil {
			t.Fatal("bundle bare parent traversal passed")
		}
	})
	t.Run("bundle source reference bounds", func(t *testing.T) {
		manifest := ArtifactBundleManifest{
			ManifestID: "manifest_00000000-0000-4000-8000-000000000010", SchemaVersion: KnowledgeSchemaVersion,
			TenantID: testTenantID, RepositoryID: testRepositoryID, Family: "evidence",
			Entries: []ArtifactBundleEntry{{
				LogicalPath: "report.txt", ArtifactID: testArtifactID,
				ContentHash: testDigest, SizeBytes: 1, MediaType: "text/plain",
			}},
			TotalSizeBytes: 1, SourceRefs: []string{strings.Repeat("x", 501)},
			CreatedBy: "validator", CreatedAt: now,
		}
		if err := ValidateArtifactBundleManifest(manifest); err == nil {
			t.Fatal("bundle with oversized source reference passed")
		}
	})
}

func TestKnowledgeSchemasRejectLifecycleContradictions(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]map[string]any{
		"conversation.schema.json": {
			"conversation_id": testConversationID, "schema_version": "1.0",
			"tenant_id": testTenantID, "repository_id": testRepositoryID,
			"status": "active", "version": 1, "retention_class": "project",
			"created_by": "co-architect", "created_at": "2026-07-19T12:00:00Z",
			"updated_at": "2026-07-19T12:00:00Z", "closed_at": "2026-07-19T12:00:00Z",
		},
		"memory-candidate.schema.json": {
			"candidate_id":   "memory_candidate_00000000-0000-4000-8000-000000000008",
			"schema_version": "1.0", "tenant_id": testTenantID, "repository_id": testRepositoryID,
			"conversation_id": testConversationID, "source_message_ids": []string{testMessageID},
			"kind": "lesson", "proposed_artifact_id": testArtifactID, "proposed_content_hash": testDigest,
			"status": "proposed", "version": 1, "proposed_by": "agent",
			"proposed_at": "2026-07-19T12:00:00Z", "resolved_by": "agent",
		},
	} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err := registry.ValidateJSON(name, encoded); err == nil {
			t.Fatalf("%s accepted a lifecycle contradiction", name)
		}
	}
}

func validKnowledgeMessage(t *testing.T, now time.Time) Message {
	t.Helper()
	parts := []ContentPart{{
		PartID: "part_00000000-0000-4000-8000-000000000006", Ordinal: 0,
		Kind: "text", ArtifactID: testArtifactID, ContentHash: testDigest,
		MediaType: "text/plain; charset=utf-8", SizeBytes: 32,
	}}
	citations := []Citation{{
		CitationID: "citation_00000000-0000-4000-8000-000000000007", Ordinal: 0,
		SourceArtifactID: testArtifactID, SourceContentHash: testDigest,
		Locator: CitationLocator{Kind: "whole", Value: "whole artifact"},
	}}
	digest, err := ComputeMessageContentHash(parts, citations)
	if err != nil {
		t.Fatal(err)
	}
	return Message{
		MessageID: testMessageID, SchemaVersion: KnowledgeSchemaVersion,
		TenantID: testTenantID, RepositoryID: testRepositoryID, ConversationID: testConversationID,
		SequenceNumber: 1, Role: "human", AuthorID: "co-architect", ContentHash: digest,
		ContentParts: parts, Citations: citations, CreatedAt: now,
	}
}
