package contracts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	KnowledgeSchemaVersion                = "1.0"
	ConversationSchemaRef                 = "https://forja.dev/schemas/conversation.schema.json"
	MessageSchemaRef                      = "https://forja.dev/schemas/message.schema.json"
	MemoryCandidateSchemaRef              = "https://forja.dev/schemas/memory-candidate.schema.json"
	MemoryRecordSchemaRef                 = "https://forja.dev/schemas/memory-record.schema.json"
	ArtifactBundleManifestSchemaRef       = "https://forja.dev/schemas/artifact-bundle-manifest.schema.json"
	MaximumMessageParts                   = 64
	MaximumMessageCitations               = 128
	MaximumBundleEntries                  = 4096
	MaximumBundleSourceRefs               = 1024
	MaximumMemorySupersessions            = 128
	MaximumArtifactObjectBytes      int64 = 4 << 30
	MaximumBundleBytes              int64 = 16 << 30
)

var (
	conversationIDPattern = regexp.MustCompile(`^conversation_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	messageIDPattern      = regexp.MustCompile(`^message_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	partIDPattern         = regexp.MustCompile(`^part_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	citationIDPattern     = regexp.MustCompile(`^citation_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	candidateIDPattern    = regexp.MustCompile(`^memory_candidate_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	memoryIDPattern       = regexp.MustCompile(`^memory_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	manifestIDPattern     = regexp.MustCompile(`^manifest_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	artifactIDPattern     = regexp.MustCompile(`^artifact_[A-Za-z0-9_-]+$`)
	authorityIDPattern    = regexp.MustCompile(`^(?:tenant|repo)_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	contentHashPattern    = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

type Conversation struct {
	ConversationID       string     `json:"conversation_id"`
	SchemaVersion        string     `json:"schema_version"`
	TenantID             string     `json:"tenant_id"`
	RepositoryID         string     `json:"repository_id"`
	Status               string     `json:"status"`
	Version              int        `json:"version"`
	RetentionClass       string     `json:"retention_class"`
	CreatedBy            string     `json:"created_by"`
	TranscriptArtifactID *string    `json:"transcript_artifact_id,omitempty"`
	TranscriptManifestID *string    `json:"transcript_manifest_id,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	ClosedAt             *time.Time `json:"closed_at,omitempty"`
	TombstonedAt         *time.Time `json:"tombstoned_at,omitempty"`
}

type ContentPart struct {
	PartID      string `json:"part_id"`
	Ordinal     int    `json:"ordinal"`
	Kind        string `json:"kind"`
	ArtifactID  string `json:"artifact_id"`
	ContentHash string `json:"content_hash"`
	MediaType   string `json:"media_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

type CitationLocator struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type Citation struct {
	CitationID        string          `json:"citation_id"`
	Ordinal           int             `json:"ordinal"`
	SourceArtifactID  string          `json:"source_artifact_id"`
	SourceContentHash string          `json:"source_content_hash"`
	Locator           CitationLocator `json:"locator"`
}

type Message struct {
	MessageID           string        `json:"message_id"`
	SchemaVersion       string        `json:"schema_version"`
	TenantID            string        `json:"tenant_id"`
	RepositoryID        string        `json:"repository_id"`
	ConversationID      string        `json:"conversation_id"`
	SequenceNumber      int           `json:"sequence_number"`
	Role                string        `json:"role"`
	AuthorID            string        `json:"author_id"`
	ContentHash         string        `json:"content_hash"`
	SupersedesMessageID *string       `json:"supersedes_message_id,omitempty"`
	ContentParts        []ContentPart `json:"content_parts"`
	Citations           []Citation    `json:"citations"`
	CreatedAt           time.Time     `json:"created_at"`
}

type MemoryCandidate struct {
	CandidateID         string     `json:"candidate_id"`
	SchemaVersion       string     `json:"schema_version"`
	TenantID            string     `json:"tenant_id"`
	RepositoryID        string     `json:"repository_id"`
	ConversationID      string     `json:"conversation_id"`
	SourceMessageIDs    []string   `json:"source_message_ids"`
	Kind                string     `json:"kind"`
	ProposedArtifactID  string     `json:"proposed_artifact_id"`
	ProposedContentHash string     `json:"proposed_content_hash"`
	Status              string     `json:"status"`
	Version             int        `json:"version"`
	ProposedBy          string     `json:"proposed_by"`
	ProposedAt          time.Time  `json:"proposed_at"`
	ExpiresAt           *time.Time `json:"expires_at,omitempty"`
	MemoryID            *string    `json:"memory_id,omitempty"`
	ResolvedBy          *string    `json:"resolved_by,omitempty"`
	ResolutionReason    *string    `json:"resolution_reason,omitempty"`
	ResolvedAt          *time.Time `json:"resolved_at,omitempty"`
}

type MemoryRecord struct {
	MemoryID          string     `json:"memory_id"`
	SchemaVersion     string     `json:"schema_version"`
	TenantID          string     `json:"tenant_id"`
	RepositoryID      string     `json:"repository_id"`
	SourceCandidateID string     `json:"source_candidate_id"`
	Kind              string     `json:"kind"`
	Status            string     `json:"status"`
	Version           int        `json:"version"`
	ContentArtifactID string     `json:"content_artifact_id"`
	ContentHash       string     `json:"content_hash"`
	AuthorityClass    string     `json:"authority_class"`
	PromotedBy        string     `json:"promoted_by"`
	PromotionReason   string     `json:"promotion_reason"`
	PromotedAt        time.Time  `json:"promoted_at"`
	ExpiresAt         *time.Time `json:"expires_at"`
	Supersedes        []string   `json:"supersedes"`
	SupersededBy      *string    `json:"superseded_by,omitempty"`
	SupersededAt      *time.Time `json:"superseded_at,omitempty"`
	ExpiredAt         *time.Time `json:"expired_at,omitempty"`
	TombstonedAt      *time.Time `json:"tombstoned_at,omitempty"`
}

type ArtifactBundleEntry struct {
	LogicalPath string `json:"logical_path"`
	ArtifactID  string `json:"artifact_id"`
	ContentHash string `json:"content_hash"`
	SizeBytes   int64  `json:"size_bytes"`
	MediaType   string `json:"media_type"`
}

type ArtifactBundleManifest struct {
	ManifestID     string                `json:"manifest_id"`
	SchemaVersion  string                `json:"schema_version"`
	TenantID       string                `json:"tenant_id"`
	RepositoryID   string                `json:"repository_id"`
	Family         string                `json:"family"`
	Entries        []ArtifactBundleEntry `json:"entries"`
	TotalSizeBytes int64                 `json:"total_size_bytes"`
	SourceRefs     []string              `json:"source_refs"`
	CreatedBy      string                `json:"created_by"`
	CreatedAt      time.Time             `json:"created_at"`
}

func ComputeMessageContentHash(parts []ContentPart, citations []Citation) (string, error) {
	canonical := struct {
		ContentParts []ContentPart `json:"content_parts"`
		Citations    []Citation    `json:"citations"`
	}{parts, citations}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal message hash projection: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func ValidateConversation(value Conversation) error {
	if value.SchemaVersion != KnowledgeSchemaVersion ||
		!conversationIDPattern.MatchString(value.ConversationID) ||
		!validAuthority(value.TenantID, "tenant_") ||
		!validAuthority(value.RepositoryID, "repo_") {
		return fmt.Errorf("conversation identity or schema version is invalid")
	}
	if value.Version < 1 || strings.TrimSpace(value.CreatedBy) == "" ||
		value.CreatedAt.IsZero() || value.UpdatedAt.Before(value.CreatedAt) {
		return fmt.Errorf("conversation lifecycle metadata is invalid")
	}
	if value.ClosedAt != nil && value.ClosedAt.Before(value.CreatedAt) ||
		value.TombstonedAt != nil && value.TombstonedAt.Before(value.CreatedAt) {
		return fmt.Errorf("conversation terminal timestamp precedes creation")
	}
	switch value.RetentionClass {
	case "ephemeral", "project", "regulated", "indefinite":
	default:
		return fmt.Errorf("conversation retention class is invalid")
	}
	switch value.Status {
	case "active":
		if value.TranscriptArtifactID != nil || value.TranscriptManifestID != nil ||
			value.ClosedAt != nil || value.TombstonedAt != nil {
			return fmt.Errorf("active conversation carries terminal fields")
		}
	case "closed":
		if value.TranscriptArtifactID == nil || !artifactIDPattern.MatchString(*value.TranscriptArtifactID) ||
			value.TranscriptManifestID == nil || !manifestIDPattern.MatchString(*value.TranscriptManifestID) ||
			value.ClosedAt == nil || value.TombstonedAt != nil {
			return fmt.Errorf("closed conversation lacks an exact transcript binding")
		}
	case "tombstoned":
		if value.TombstonedAt == nil {
			return fmt.Errorf("tombstoned conversation lacks its timestamp")
		}
		if (value.ClosedAt == nil) != (value.TranscriptArtifactID == nil) ||
			(value.TranscriptArtifactID == nil) != (value.TranscriptManifestID == nil) ||
			value.TranscriptArtifactID != nil && !artifactIDPattern.MatchString(*value.TranscriptArtifactID) ||
			value.TranscriptManifestID != nil && !manifestIDPattern.MatchString(*value.TranscriptManifestID) ||
			value.ClosedAt != nil && value.TombstonedAt.Before(*value.ClosedAt) {
			return fmt.Errorf("tombstoned conversation carries an invalid prior closure")
		}
	default:
		return fmt.Errorf("conversation status is invalid")
	}
	return nil
}

func ValidateMessage(value Message) error {
	if value.SchemaVersion != KnowledgeSchemaVersion ||
		!messageIDPattern.MatchString(value.MessageID) ||
		!conversationIDPattern.MatchString(value.ConversationID) ||
		!validAuthority(value.TenantID, "tenant_") ||
		!validAuthority(value.RepositoryID, "repo_") ||
		value.SequenceNumber < 1 || value.CreatedAt.IsZero() ||
		strings.TrimSpace(value.AuthorID) == "" || len(value.AuthorID) > 160 {
		return fmt.Errorf("message identity or lifecycle metadata is invalid")
	}
	if value.SupersedesMessageID != nil &&
		(!messageIDPattern.MatchString(*value.SupersedesMessageID) || *value.SupersedesMessageID == value.MessageID) {
		return fmt.Errorf("message supersession is invalid")
	}
	switch value.Role {
	case "human", "assistant", "system", "tool":
	default:
		return fmt.Errorf("message role is invalid")
	}
	if value.Citations == nil || len(value.ContentParts) < 1 || len(value.ContentParts) > MaximumMessageParts ||
		len(value.Citations) > MaximumMessageCitations {
		return fmt.Errorf("message part or citation count is invalid")
	}
	partIDs := make(map[string]struct{}, len(value.ContentParts))
	for index, part := range value.ContentParts {
		if part.Ordinal != index || !partIDPattern.MatchString(part.PartID) ||
			!validContentPartKind(part.Kind) ||
			!artifactIDPattern.MatchString(part.ArtifactID) ||
			!contentHashPattern.MatchString(part.ContentHash) ||
			part.SizeBytes < 0 || part.SizeBytes > MaximumArtifactObjectBytes ||
			len(part.MediaType) < 3 || len(part.MediaType) > 120 || strings.TrimSpace(part.MediaType) != part.MediaType {
			return fmt.Errorf("message content part %d is invalid", index)
		}
		if _, exists := partIDs[part.PartID]; exists {
			return fmt.Errorf("message repeats content part %q", part.PartID)
		}
		partIDs[part.PartID] = struct{}{}
	}
	citationIDs := make(map[string]struct{}, len(value.Citations))
	for index, citation := range value.Citations {
		if citation.Ordinal != index || !citationIDPattern.MatchString(citation.CitationID) ||
			!artifactIDPattern.MatchString(citation.SourceArtifactID) ||
			!contentHashPattern.MatchString(citation.SourceContentHash) ||
			!validCitationLocatorKind(citation.Locator.Kind) ||
			strings.TrimSpace(citation.Locator.Value) == "" || len(citation.Locator.Value) > 500 {
			return fmt.Errorf("message citation %d is invalid", index)
		}
		if _, exists := citationIDs[citation.CitationID]; exists {
			return fmt.Errorf("message repeats citation %q", citation.CitationID)
		}
		citationIDs[citation.CitationID] = struct{}{}
	}
	wantHash, err := ComputeMessageContentHash(value.ContentParts, value.Citations)
	if err != nil || value.ContentHash != wantHash {
		return fmt.Errorf("message content hash does not match its ordered references")
	}
	return nil
}

func ValidateMemoryCandidate(value MemoryCandidate) error {
	if value.SchemaVersion != KnowledgeSchemaVersion || !candidateIDPattern.MatchString(value.CandidateID) ||
		!conversationIDPattern.MatchString(value.ConversationID) || !validAuthority(value.TenantID, "tenant_") ||
		!validAuthority(value.RepositoryID, "repo_") || !artifactIDPattern.MatchString(value.ProposedArtifactID) ||
		!contentHashPattern.MatchString(value.ProposedContentHash) || value.Version < 1 ||
		value.ProposedAt.IsZero() || strings.TrimSpace(value.ProposedBy) == "" || len(value.ProposedBy) > 160 {
		return fmt.Errorf("memory candidate identity or metadata is invalid")
	}
	if len(value.SourceMessageIDs) < 1 || len(value.SourceMessageIDs) > MaximumMessageCitations ||
		!uniqueMatching(value.SourceMessageIDs, messageIDPattern) {
		return fmt.Errorf("memory candidate source messages are invalid")
	}
	if value.ExpiresAt != nil && !value.ExpiresAt.After(value.ProposedAt) ||
		value.ResolvedAt != nil && value.ResolvedAt.Before(value.ProposedAt) {
		return fmt.Errorf("memory candidate lifecycle timestamps are invalid")
	}
	switch value.Kind {
	case "fact", "preference", "decision", "lesson":
	default:
		return fmt.Errorf("memory candidate kind is invalid")
	}
	resolved := value.ResolvedBy != nil && value.ResolutionReason != nil && value.ResolvedAt != nil
	switch value.Status {
	case "proposed":
		if value.MemoryID != nil || value.ResolvedBy != nil || value.ResolutionReason != nil || value.ResolvedAt != nil {
			return fmt.Errorf("proposed memory candidate carries resolution fields")
		}
	case "promoted":
		if value.MemoryID == nil || !memoryIDPattern.MatchString(*value.MemoryID) || !resolved {
			return fmt.Errorf("promoted memory candidate lacks its resolution")
		}
	case "rejected", "expired":
		if value.MemoryID != nil || !resolved {
			return fmt.Errorf("resolved memory candidate has invalid fields")
		}
	default:
		return fmt.Errorf("memory candidate status is invalid")
	}
	return nil
}

func ValidateMemoryRecord(value MemoryRecord) error {
	if value.SchemaVersion != KnowledgeSchemaVersion || !memoryIDPattern.MatchString(value.MemoryID) ||
		!candidateIDPattern.MatchString(value.SourceCandidateID) || !validAuthority(value.TenantID, "tenant_") ||
		!validAuthority(value.RepositoryID, "repo_") || !artifactIDPattern.MatchString(value.ContentArtifactID) ||
		!contentHashPattern.MatchString(value.ContentHash) || value.Version < 1 || value.PromotedAt.IsZero() ||
		strings.TrimSpace(value.PromotedBy) == "" || len(value.PromotedBy) > 160 ||
		strings.TrimSpace(value.PromotionReason) == "" || len(value.PromotionReason) > 2000 {
		return fmt.Errorf("memory record identity or promotion metadata is invalid")
	}
	if value.AuthorityClass != "human_approved" && value.AuthorityClass != "policy_approved" {
		return fmt.Errorf("memory authority class is invalid")
	}
	switch value.Kind {
	case "fact", "preference", "decision", "lesson":
	default:
		return fmt.Errorf("memory kind is invalid")
	}
	if value.ExpiresAt != nil && !value.ExpiresAt.After(value.PromotedAt) ||
		value.SupersededAt != nil && value.SupersededAt.Before(value.PromotedAt) ||
		value.ExpiredAt != nil && value.ExpiredAt.Before(value.PromotedAt) ||
		value.TombstonedAt != nil && value.TombstonedAt.Before(value.PromotedAt) {
		return fmt.Errorf("memory lifecycle timestamps are invalid")
	}
	if value.Supersedes == nil || len(value.Supersedes) > MaximumMemorySupersessions ||
		!uniqueMatching(value.Supersedes, memoryIDPattern) || slices.Contains(value.Supersedes, value.MemoryID) {
		return fmt.Errorf("memory supersession references are invalid")
	}
	switch value.Status {
	case "active":
		if value.SupersededBy != nil || value.SupersededAt != nil || value.ExpiredAt != nil || value.TombstonedAt != nil {
			return fmt.Errorf("active memory carries terminal fields")
		}
	case "superseded":
		if value.SupersededBy == nil || !memoryIDPattern.MatchString(*value.SupersededBy) ||
			value.SupersededAt == nil || value.ExpiredAt != nil || value.TombstonedAt != nil {
			return fmt.Errorf("superseded memory lacks its successor")
		}
	case "expired":
		if value.ExpiredAt == nil || value.SupersededBy != nil || value.SupersededAt != nil || value.TombstonedAt != nil {
			return fmt.Errorf("expired memory lifecycle is invalid")
		}
	case "tombstoned":
		if value.TombstonedAt == nil {
			return fmt.Errorf("tombstoned memory lacks its timestamp")
		}
	default:
		return fmt.Errorf("memory status is invalid")
	}
	return nil
}

func ValidateArtifactBundleManifest(value ArtifactBundleManifest) error {
	if value.SchemaVersion != KnowledgeSchemaVersion || !manifestIDPattern.MatchString(value.ManifestID) ||
		!validAuthority(value.TenantID, "tenant_") || !validAuthority(value.RepositoryID, "repo_") ||
		strings.TrimSpace(value.CreatedBy) == "" || len(value.CreatedBy) > 160 || value.CreatedAt.IsZero() ||
		len(value.Entries) < 1 || len(value.Entries) > MaximumBundleEntries ||
		len(value.SourceRefs) < 1 || len(value.SourceRefs) > MaximumBundleSourceRefs ||
		!uniqueNonBlank(value.SourceRefs) || slices.ContainsFunc(value.SourceRefs, func(reference string) bool { return len(reference) > 500 }) {
		return fmt.Errorf("artifact bundle identity or metadata is invalid")
	}
	switch value.Family {
	case "evidence", "conversation_transcript", "dataset", "report", "snapshot":
	default:
		return fmt.Errorf("artifact bundle family is invalid")
	}
	var total int64
	previousPath := ""
	for index, entry := range value.Entries {
		cleaned := path.Clean(entry.LogicalPath)
		if entry.LogicalPath == "" || len(entry.LogicalPath) > 4096 || cleaned != entry.LogicalPath || path.IsAbs(entry.LogicalPath) ||
			entry.LogicalPath == "." || entry.LogicalPath == ".." || strings.HasPrefix(entry.LogicalPath, "../") ||
			(index > 0 && strings.Compare(previousPath, entry.LogicalPath) >= 0) ||
			!artifactIDPattern.MatchString(entry.ArtifactID) || !contentHashPattern.MatchString(entry.ContentHash) ||
			entry.SizeBytes < 0 || entry.SizeBytes > MaximumArtifactObjectBytes ||
			len(entry.MediaType) < 3 || len(entry.MediaType) > 120 || strings.TrimSpace(entry.MediaType) != entry.MediaType {
			return fmt.Errorf("artifact bundle entry %d is invalid or not canonically ordered", index)
		}
		if total > MaximumBundleBytes-entry.SizeBytes {
			return fmt.Errorf("artifact bundle exceeds its aggregate byte budget")
		}
		total += entry.SizeBytes
		previousPath = entry.LogicalPath
	}
	if value.TotalSizeBytes != total {
		return fmt.Errorf("artifact bundle total does not match its entries")
	}
	return nil
}

func validAuthority(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && authorityIDPattern.MatchString(value)
}

func validContentPartKind(value string) bool {
	switch value {
	case "text", "json", "code", "image", "audio", "video", "file", "tool_call", "tool_result":
		return true
	default:
		return false
	}
}

func validCitationLocatorKind(value string) bool {
	switch value {
	case "whole", "line_range", "page_range", "time_range", "json_pointer", "uri_fragment":
		return true
	default:
		return false
	}
}

func uniqueMatching(values []string, pattern *regexp.Regexp) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !pattern.MatchString(value) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func uniqueNonBlank(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}
