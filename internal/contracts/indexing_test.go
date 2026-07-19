package contracts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStableIndexIDGoldenVector(t *testing.T) {
	t.Parallel()
	got := StableIndexID(
		"file", "repo_x", "abc", "src/main.go", "blob", "sha256:body",
	)
	const want = "file_8b811ae7191c0fef1710b789c20d2848f1a850a51a533108136f1573f73d6c96"
	if got != want {
		t.Fatalf("stable ID = %s, want %s", got, want)
	}
}

func TestRegistryAndSemanticValidationAcceptIndexContracts(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, file, symbol, relation := validIndexContracts(t)
	for name, value := range map[string]any{
		"repository-snapshot.schema.json": snapshot,
		"file-card.schema.json":           file,
		"symbol-card.schema.json":         symbol,
		"relation-evidence.schema.json":   relation,
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
		"snapshot": func() error { return ValidateRepositorySnapshot(snapshot) },
		"file":     func() error { return ValidateFileCard(file) },
		"symbol":   func() error { return ValidateSymbolCard(symbol) },
		"relation": func() error { return ValidateRelationEvidence(relation) },
	} {
		if err := validate(); err != nil {
			t.Fatalf("%s semantic validation failed: %v", name, err)
		}
	}
}

func TestIndexContractsFailClosed(t *testing.T) {
	t.Parallel()
	snapshot, file, symbol, relation := validIndexContracts(t)

	t.Run("snapshot adapter order", func(t *testing.T) {
		other := snapshot.Adapters[0]
		other.Name = "python"
		snapshot.Adapters = []AdapterDescriptor{other, snapshot.Adapters[0]}
		if err := ValidateRepositorySnapshot(snapshot); err == nil {
			t.Fatal("noncanonical adapters passed")
		}
	})
	t.Run("path traversal", func(t *testing.T) {
		file.Path = "src/../secret.go"
		file.FileID = ComputeFileID(file)
		if err := ValidateFileCard(file); err == nil {
			t.Fatal("path traversal passed")
		}
	})
	t.Run("symbol identity mismatch", func(t *testing.T) {
		symbol.Signature = "func Changed()"
		if err := ValidateSymbolCard(symbol); err == nil {
			t.Fatal("stale symbol ID passed")
		}
	})
	t.Run("semantic relation", func(t *testing.T) {
		value := relation
		value.EvidenceClass = "candidate_semantic"
		value.RelationID = ComputeRelationID(value)
		if err := ValidateRelationEvidence(value); err == nil {
			t.Fatal("semantic evidence entered the canonical relation contract")
		}
	})
	t.Run("ambiguous resolved relation", func(t *testing.T) {
		value := relation
		name := "unknown.Target"
		value.UnresolvedName = &name
		value.RelationID = ComputeRelationID(value)
		if err := ValidateRelationEvidence(value); err == nil {
			t.Fatal("resolved relation with an unresolved name passed")
		}
	})
}

func TestNormalizeRepositoryPathRejectsAmbiguity(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", ".", "..", "../a", "a/../b", "/a", "a/", "a\\b", "a//b", "a/./b", "a\x00b"} {
		if _, err := NormalizeRepositoryPath(value); err == nil {
			t.Errorf("NormalizeRepositoryPath(%q) passed", value)
		}
	}
	if got, err := NormalizeRepositoryPath("src/accounting/balance.go"); err != nil || got != "src/accounting/balance.go" {
		t.Fatalf("canonical path = %q, %v", got, err)
	}
}

func FuzzNormalizeRepositoryPath(f *testing.F) {
	f.Add("src/main.go")
	f.Add("../secret")
	f.Fuzz(func(t *testing.T, value string) {
		got, err := NormalizeRepositoryPath(value)
		if err == nil && (got != value || strings.Contains(got, "\\")) {
			t.Fatalf("normalization changed accepted path %q to %q", value, got)
		}
	})
}

func validIndexContracts(t *testing.T) (RepositorySnapshot, FileCard, SymbolCard, RelationEvidence) {
	t.Helper()
	adapter := AdapterDescriptor{
		Name:              "go",
		Version:           "go1.26.5",
		ConfigurationHash: testDigest,
		CapabilityHash:    "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	adapterHash, err := ComputeAdapterSetHash([]AdapterDescriptor{adapter})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	snapshot := RepositorySnapshot{
		SchemaVersion: IndexSchemaVersion, TenantID: testTenantID, RepositoryID: testRepositoryID,
		SourceCommit: strings.Repeat("a", 40), SourceTree: strings.Repeat("b", 40),
		ConfigurationHash: testDigest, AdapterSetHash: adapterHash, Adapters: []AdapterDescriptor{adapter},
		Status: "proposed", Version: 1, Counts: SnapshotCounts{}, CreatedBy: "indexer", CreatedAt: now,
	}
	snapshot.SnapshotID = ComputeSnapshotID(snapshot)
	file := FileCard{
		SchemaVersion: IndexSchemaVersion, SnapshotID: snapshot.SnapshotID, RepositoryID: testRepositoryID,
		SourceCommit: snapshot.SourceCommit, Path: "pkg/accounting/balance.go", GitBlobID: strings.Repeat("c", 40),
		SourceHash: testDigest, SizeBytes: 128, Language: "go", SymbolIDs: []string{}, Diagnostics: []DiagnosticSummary{},
	}
	file.FileID = ComputeFileID(file)
	file.LineageID = ComputeFileLineageID(file)
	symbol := SymbolCard{
		SchemaVersion: IndexSchemaVersion, SnapshotID: snapshot.SnapshotID,
		FileID: file.FileID, FileLineageID: file.LineageID,
		Language: "go", Kind: "function", Name: "Balance", QualifiedName: "accounting.Balance",
		Signature: "func Balance(entries []Entry) Amount", Declaration: SourceRange{
			Start: SourcePosition{Line: 10, Column: 1, Offset: 100},
			End:   SourcePosition{Line: 12, Column: 2, Offset: 180},
		}, Exported: true,
	}
	symbol.SymbolID = ComputeSymbolID(symbol)
	symbol.LineageID = ComputeSymbolLineageID(symbol)
	file.SymbolIDs = []string{symbol.SymbolID}
	target := StableIndexID("external", "fmt.Stringer")
	relation := RelationEvidence{
		SchemaVersion: IndexSchemaVersion, SnapshotID: snapshot.SnapshotID,
		SourceEntityID: symbol.SymbolID, Kind: "implements", Resolution: "resolved", TargetEntityID: &target,
		EvidenceClass: "confirmed_static", SourceFileID: file.FileID, Locator: symbol.Declaration,
		EvidenceHash: testDigest, Adapter: adapter,
	}
	relation.RelationID = ComputeRelationID(relation)
	return snapshot, file, symbol, relation
}
