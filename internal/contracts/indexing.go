package contracts

import (
	"crypto/sha256"
	"encoding/binary"
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
	IndexSchemaVersion                = "1.0"
	RepositorySnapshotSchemaRef       = "https://forja.dev/schemas/repository-snapshot.schema.json"
	FileCardSchemaRef                 = "https://forja.dev/schemas/file-card.schema.json"
	SymbolCardSchemaRef               = "https://forja.dev/schemas/symbol-card.schema.json"
	RelationEvidenceSchemaRef         = "https://forja.dev/schemas/relation-evidence.schema.json"
	MaximumSnapshotAdapters           = 16
	MaximumFileSymbols                = 100000
	MaximumFileDiagnostics            = 4096
	MaximumIndexedFileBytes     int64 = 16 << 20
)

var (
	snapshotIDPattern = regexp.MustCompile(`^snapshot_[a-f0-9]{64}$`)
	fileIDPattern     = regexp.MustCompile(`^file_[a-f0-9]{64}$`)
	symbolIDPattern   = regexp.MustCompile(`^symbol_[a-f0-9]{64}$`)
	relationIDPattern = regexp.MustCompile(`^relation_[a-f0-9]{64}$`)
	entityIDPattern   = regexp.MustCompile(`^(?:file|symbol|external)_[a-f0-9]{64}$`)
	gitObjectPattern  = regexp.MustCompile(`^[a-f0-9]{40,64}$`)
)

type AdapterDescriptor struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	ConfigurationHash string `json:"configuration_hash"`
	CapabilityHash    string `json:"capability_hash"`
}

type SnapshotCounts struct {
	Files       int `json:"files"`
	Symbols     int `json:"symbols"`
	Relations   int `json:"relations"`
	Diagnostics int `json:"diagnostics"`
}

type RepositorySnapshot struct {
	SnapshotID          string              `json:"snapshot_id"`
	SchemaVersion       string              `json:"schema_version"`
	TenantID            string              `json:"tenant_id"`
	RepositoryID        string              `json:"repository_id"`
	SourceCommit        string              `json:"source_commit"`
	SourceTree          string              `json:"source_tree"`
	ConfigurationHash   string              `json:"configuration_hash"`
	AdapterSetHash      string              `json:"adapter_set_hash"`
	Adapters            []AdapterDescriptor `json:"adapters"`
	Status              string              `json:"status"`
	Version             int                 `json:"version"`
	Counts              SnapshotCounts      `json:"counts"`
	ArtifactID          *string             `json:"artifact_id,omitempty"`
	ArtifactContentHash *string             `json:"artifact_content_hash,omitempty"`
	CreatedBy           string              `json:"created_by"`
	CreatedAt           time.Time           `json:"created_at"`
	ValidatedAt         *time.Time          `json:"validated_at,omitempty"`
}

type DiagnosticSummary struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Count    int    `json:"count"`
}

type FileCard struct {
	FileID        string              `json:"file_id"`
	SchemaVersion string              `json:"schema_version"`
	SnapshotID    string              `json:"snapshot_id"`
	RepositoryID  string              `json:"repository_id"`
	SourceCommit  string              `json:"source_commit"`
	Path          string              `json:"path"`
	GitBlobID     string              `json:"git_blob_id"`
	SourceHash    string              `json:"source_hash"`
	SizeBytes     int64               `json:"size_bytes"`
	Language      string              `json:"language"`
	Generated     bool                `json:"generated"`
	SymbolIDs     []string            `json:"symbol_ids"`
	Diagnostics   []DiagnosticSummary `json:"diagnostics"`
}

type SourcePosition struct {
	Line   int `json:"line"`
	Column int `json:"column"`
	Offset int `json:"offset"`
}

type SourceRange struct {
	Start SourcePosition `json:"start"`
	End   SourcePosition `json:"end"`
}

type SymbolCard struct {
	SymbolID          string      `json:"symbol_id"`
	SchemaVersion     string      `json:"schema_version"`
	SnapshotID        string      `json:"snapshot_id"`
	FileID            string      `json:"file_id"`
	Language          string      `json:"language"`
	Kind              string      `json:"kind"`
	Name              string      `json:"name"`
	QualifiedName     string      `json:"qualified_name"`
	Signature         string      `json:"signature"`
	Declaration       SourceRange `json:"declaration"`
	Exported          bool        `json:"exported"`
	Test              bool        `json:"test"`
	Route             bool        `json:"route"`
	Schema            bool        `json:"schema"`
	DocumentationHash *string     `json:"documentation_hash,omitempty"`
}

type RelationEvidence struct {
	RelationID     string            `json:"relation_id"`
	SchemaVersion  string            `json:"schema_version"`
	SnapshotID     string            `json:"snapshot_id"`
	SourceEntityID string            `json:"source_entity_id"`
	Kind           string            `json:"kind"`
	Resolution     string            `json:"resolution"`
	TargetEntityID *string           `json:"target_entity_id,omitempty"`
	UnresolvedName *string           `json:"unresolved_name,omitempty"`
	EvidenceClass  string            `json:"evidence_class"`
	SourceFileID   string            `json:"source_file_id"`
	Locator        SourceRange       `json:"locator"`
	EvidenceHash   string            `json:"evidence_hash"`
	Adapter        AdapterDescriptor `json:"adapter"`
}

func StableIndexID(prefix string, components ...string) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("forja-index-id-v1\x00"))
	_, _ = digest.Write([]byte(prefix))
	for _, component := range components {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(component)))
		_, _ = digest.Write(size[:])
		_, _ = digest.Write([]byte(component))
	}
	return prefix + "_" + hex.EncodeToString(digest.Sum(nil))
}

func ComputeAdapterSetHash(adapters []AdapterDescriptor) (string, error) {
	if err := validateAdapters(adapters); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(adapters)
	if err != nil {
		return "", fmt.Errorf("marshal adapter set: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func ComputeSnapshotID(value RepositorySnapshot) string {
	return StableIndexID(
		"snapshot", value.TenantID, value.RepositoryID, value.SourceCommit,
		value.SourceTree, value.ConfigurationHash, value.AdapterSetHash,
	)
}

func ComputeFileID(value FileCard) string {
	return StableIndexID(
		"file", value.RepositoryID, value.SourceCommit, value.Path,
		value.GitBlobID, value.SourceHash,
	)
}

func ComputeSymbolID(value SymbolCard) string {
	return StableIndexID(
		"symbol", value.FileID, value.Language, value.Kind, value.QualifiedName,
		value.Signature, sourceRangeKey(value.Declaration),
	)
}

func ComputeRelationID(value RelationEvidence) string {
	target := ""
	if value.TargetEntityID != nil {
		target = *value.TargetEntityID
	}
	unresolved := ""
	if value.UnresolvedName != nil {
		unresolved = *value.UnresolvedName
	}
	return StableIndexID(
		"relation", value.SnapshotID, value.SourceEntityID, value.Kind,
		value.Resolution, target, unresolved, value.EvidenceClass,
		value.SourceFileID, sourceRangeKey(value.Locator), value.EvidenceHash,
		value.Adapter.Name, value.Adapter.Version,
		value.Adapter.ConfigurationHash, value.Adapter.CapabilityHash,
	)
}

func NormalizeRepositoryPath(value string) (string, error) {
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\\\x00") ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") {
		return "", fmt.Errorf("repository path is invalid")
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != value {
		return "", fmt.Errorf("repository path is not canonical")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("repository path has an invalid segment")
		}
	}
	return value, nil
}

func ValidateRepositorySnapshot(value RepositorySnapshot) error {
	if value.SchemaVersion != IndexSchemaVersion ||
		!validAuthority(value.TenantID, "tenant_") ||
		!validAuthority(value.RepositoryID, "repo_") ||
		!gitObjectPattern.MatchString(value.SourceCommit) ||
		!gitObjectPattern.MatchString(value.SourceTree) ||
		!contentHashPattern.MatchString(value.ConfigurationHash) ||
		!contentHashPattern.MatchString(value.AdapterSetHash) ||
		value.Version < 1 || strings.TrimSpace(value.CreatedBy) == "" ||
		len(value.CreatedBy) > 160 || value.CreatedAt.IsZero() {
		return fmt.Errorf("repository snapshot identity or lifecycle is invalid")
	}
	wantAdapterHash, err := ComputeAdapterSetHash(value.Adapters)
	if err != nil || wantAdapterHash != value.AdapterSetHash {
		return fmt.Errorf("repository snapshot adapter set is invalid")
	}
	if !snapshotIDPattern.MatchString(value.SnapshotID) || value.SnapshotID != ComputeSnapshotID(value) {
		return fmt.Errorf("repository snapshot ID does not match its identity projection")
	}
	if value.Counts.Files < 0 || value.Counts.Files > 1000000 ||
		value.Counts.Symbols < 0 || value.Counts.Symbols > 10000000 ||
		value.Counts.Relations < 0 || value.Counts.Relations > 50000000 ||
		value.Counts.Diagnostics < 0 || value.Counts.Diagnostics > 10000000 {
		return fmt.Errorf("repository snapshot counts are invalid")
	}
	terminalBody := value.ArtifactID != nil || value.ArtifactContentHash != nil || value.ValidatedAt != nil
	switch value.Status {
	case "proposed", "extracting", "failed":
		if terminalBody {
			return fmt.Errorf("unvalidated repository snapshot carries artifact evidence")
		}
	case "validated", "active", "superseded", "invalidated":
		if value.ArtifactID == nil || !artifactIDPattern.MatchString(*value.ArtifactID) ||
			value.ArtifactContentHash == nil || !contentHashPattern.MatchString(*value.ArtifactContentHash) ||
			value.ValidatedAt == nil || value.ValidatedAt.Before(value.CreatedAt) {
			return fmt.Errorf("validated repository snapshot lacks artifact evidence")
		}
	default:
		return fmt.Errorf("repository snapshot status is invalid")
	}
	return nil
}

func ValidateFileCard(value FileCard) error {
	if value.SchemaVersion != IndexSchemaVersion || !snapshotIDPattern.MatchString(value.SnapshotID) ||
		!validAuthority(value.RepositoryID, "repo_") || !gitObjectPattern.MatchString(value.SourceCommit) ||
		!gitObjectPattern.MatchString(value.GitBlobID) || !contentHashPattern.MatchString(value.SourceHash) ||
		value.SizeBytes < 0 || value.SizeBytes > MaximumIndexedFileBytes {
		return fmt.Errorf("file card identity or source evidence is invalid")
	}
	if _, err := NormalizeRepositoryPath(value.Path); err != nil {
		return err
	}
	if !slices.Contains([]string{"go", "typescript", "javascript", "python", "json", "yaml", "markdown", "other"}, value.Language) {
		return fmt.Errorf("file card language is invalid")
	}
	if !fileIDPattern.MatchString(value.FileID) || value.FileID != ComputeFileID(value) {
		return fmt.Errorf("file card ID does not match its identity projection")
	}
	if value.SymbolIDs == nil || len(value.SymbolIDs) > MaximumFileSymbols || !sortedUniqueIndexValues(value.SymbolIDs) {
		return fmt.Errorf("file card symbol IDs are not canonical")
	}
	for _, symbolID := range value.SymbolIDs {
		if !symbolIDPattern.MatchString(symbolID) {
			return fmt.Errorf("file card carries an invalid symbol ID")
		}
	}
	if value.Diagnostics == nil || len(value.Diagnostics) > MaximumFileDiagnostics {
		return fmt.Errorf("file card diagnostics are invalid")
	}
	previous := ""
	for _, diagnostic := range value.Diagnostics {
		key := diagnostic.Severity + "\x00" + diagnostic.Code
		if !slices.Contains([]string{"info", "warning", "error"}, diagnostic.Severity) ||
			strings.TrimSpace(diagnostic.Code) == "" || len(diagnostic.Code) > 160 ||
			diagnostic.Count < 1 || key <= previous {
			return fmt.Errorf("file card diagnostic summaries are not canonical")
		}
		previous = key
	}
	return nil
}

func ValidateSymbolCard(value SymbolCard) error {
	if value.SchemaVersion != IndexSchemaVersion || !snapshotIDPattern.MatchString(value.SnapshotID) ||
		!fileIDPattern.MatchString(value.FileID) ||
		!slices.Contains([]string{"go", "typescript", "javascript", "python"}, value.Language) ||
		!slices.Contains([]string{"package", "module", "namespace", "class", "interface", "type", "struct", "enum", "function", "method", "constructor", "variable", "constant", "field", "property", "parameter", "import", "export", "route", "schema", "test"}, value.Kind) ||
		strings.TrimSpace(value.Name) == "" || len(value.Name) > 512 ||
		strings.TrimSpace(value.QualifiedName) == "" || len(value.QualifiedName) > 2048 ||
		len(value.Signature) > 8192 || validateSourceRange(value.Declaration) != nil {
		return fmt.Errorf("symbol card identity or declaration is invalid")
	}
	if value.DocumentationHash != nil && !contentHashPattern.MatchString(*value.DocumentationHash) {
		return fmt.Errorf("symbol documentation hash is invalid")
	}
	if !symbolIDPattern.MatchString(value.SymbolID) || value.SymbolID != ComputeSymbolID(value) {
		return fmt.Errorf("symbol card ID does not match its identity projection")
	}
	return nil
}

func ValidateRelationEvidence(value RelationEvidence) error {
	if value.SchemaVersion != IndexSchemaVersion || !snapshotIDPattern.MatchString(value.SnapshotID) ||
		!entityIDPattern.MatchString(value.SourceEntityID) || !fileIDPattern.MatchString(value.SourceFileID) ||
		!validRelationKind(value.Kind) || !validEvidenceClass(value.EvidenceClass) ||
		!contentHashPattern.MatchString(value.EvidenceHash) || validateSourceRange(value.Locator) != nil ||
		validateAdapter(value.Adapter) != nil {
		return fmt.Errorf("relation identity or evidence is invalid")
	}
	switch value.Resolution {
	case "resolved":
		if value.TargetEntityID == nil || !entityIDPattern.MatchString(*value.TargetEntityID) || value.UnresolvedName != nil {
			return fmt.Errorf("resolved relation lacks an exact target")
		}
	case "unresolved":
		if value.TargetEntityID != nil || value.UnresolvedName == nil ||
			strings.TrimSpace(*value.UnresolvedName) == "" || len(*value.UnresolvedName) > 2048 {
			return fmt.Errorf("unresolved relation lacks its explicit gap")
		}
	default:
		return fmt.Errorf("relation resolution is invalid")
	}
	if !relationIDPattern.MatchString(value.RelationID) || value.RelationID != ComputeRelationID(value) {
		return fmt.Errorf("relation ID does not match its identity projection")
	}
	return nil
}

func validateAdapters(values []AdapterDescriptor) error {
	if len(values) < 1 || len(values) > MaximumSnapshotAdapters {
		return fmt.Errorf("adapter count is invalid")
	}
	previous := ""
	for _, value := range values {
		if err := validateAdapter(value); err != nil {
			return err
		}
		key := value.Name + "\x00" + value.Version + "\x00" + value.ConfigurationHash + "\x00" + value.CapabilityHash
		if key <= previous {
			return fmt.Errorf("adapters are not sorted and unique")
		}
		previous = key
	}
	return nil
}

func validateAdapter(value AdapterDescriptor) error {
	if !slices.Contains([]string{"go", "typescript", "python"}, value.Name) ||
		strings.TrimSpace(value.Version) == "" || len(value.Version) > 80 ||
		!contentHashPattern.MatchString(value.ConfigurationHash) ||
		!contentHashPattern.MatchString(value.CapabilityHash) {
		return fmt.Errorf("adapter descriptor is invalid")
	}
	return nil
}

func validateSourceRange(value SourceRange) error {
	if value.Start.Line < 1 || value.Start.Column < 1 || value.Start.Offset < 0 ||
		value.End.Line < 1 || value.End.Column < 1 || value.End.Offset < 0 ||
		value.End.Offset < value.Start.Offset ||
		value.End.Line < value.Start.Line ||
		value.End.Line == value.Start.Line && value.End.Column < value.Start.Column ||
		value.End.Offset == value.Start.Offset && (value.End.Line != value.Start.Line || value.End.Column != value.Start.Column) {
		return fmt.Errorf("source range is invalid")
	}
	return nil
}

func sourceRangeKey(value SourceRange) string {
	return fmt.Sprintf("%d:%d:%d-%d:%d:%d", value.Start.Line, value.Start.Column, value.Start.Offset, value.End.Line, value.End.Column, value.End.Offset)
}

func sortedUniqueIndexValues(values []string) bool {
	for index := range values {
		if index > 0 && values[index] <= values[index-1] {
			return false
		}
	}
	return true
}

func validRelationKind(value string) bool {
	return slices.Contains([]string{"contains", "imports", "exports", "declares", "references", "calls", "implements", "extends", "reads", "writes", "tests", "routes_to", "validates", "uses_schema"}, value)
}

func validEvidenceClass(value string) bool {
	return slices.Contains([]string{"candidate_static", "confirmed_static", "confirmed_behavioral", "runtime_observed"}, value)
}
