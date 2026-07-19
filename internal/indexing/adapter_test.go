package indexing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

const (
	indexTenantID     = "tenant_00000000-0000-4000-8000-000000000001"
	indexRepositoryID = "repo_00000000-0000-4000-8000-000000000002"
)

func TestGoAdapterAndNormalizerProduceByteStableEvidence(t *testing.T) {
	documents := []SourceDocument{
		sourceDocument("go.mod", "module example.invalid/fixture\n\ngo 1.26\n", "other"),
		sourceDocument("accounting/balance.go", `package accounting

import "fmt"

type Entry struct { Amount int }

func Balance(entries []Entry) int {
	total := 0
	for _, entry := range entries { total += entry.Amount }
	fmt.Sprint(total)
	return total
}
`, "go"),
		sourceDocument("accounting/balance_test.go", `package accounting

func TestBalance() { _ = Balance([]Entry{{Amount: 2}}) }
`, "go"),
	}
	root := t.TempDir()
	if err := MaterializeDocuments(root, documents); err != nil {
		t.Fatal(err)
	}
	adapter := NewGoAdapter()
	descriptors := []contracts.AdapterDescriptor{adapter.Descriptor()}
	tree := CommittedTree{CommitID: strings.Repeat("a", 40), TreeID: strings.Repeat("b", 40)}
	snapshot, err := NewProposedSnapshot(
		indexTenantID, indexRepositoryID, tree, hashText("fixture-config"), descriptors,
		"index-test", time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}

	first := extractAndNormalize(t, adapter, root, documents, snapshot)
	second := extractAndNormalize(t, adapter, root, documents, snapshot)
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("unchanged Go source produced non-deterministic index bytes")
	}
	if first.Snapshot.Counts.Files != 3 || first.Snapshot.Counts.Symbols < 4 || first.Snapshot.Counts.Relations < first.Snapshot.Counts.Symbols {
		t.Fatalf("counts=%#v", first.Snapshot.Counts)
	}
	wantKinds := map[string]bool{"declares": false, "imports": false, "calls": false, "references": false}
	for _, relation := range first.Relations {
		if _, exists := wantKinds[relation.Kind]; exists {
			wantKinds[relation.Kind] = true
		}
		if err := contracts.ValidateRelationEvidence(relation); err != nil {
			t.Fatal(err)
		}
	}
	for kind, found := range wantKinds {
		if !found {
			t.Errorf("missing %s relation", kind)
		}
	}
	foundTest := false
	for _, symbol := range first.Symbols {
		if symbol.Test && symbol.Name == "TestBalance" {
			foundTest = true
		}
		if err := contracts.ValidateSymbolCard(symbol); err != nil {
			t.Fatal(err)
		}
	}
	if !foundTest {
		t.Fatal("Go test symbol was not classified")
	}
}

func TestMaterializeDocumentsCannotEscapeRoot(t *testing.T) {
	document := sourceDocument("../escape.go", "package escape\n", "go")
	if err := MaterializeDocuments(t.TempDir(), []SourceDocument{document}); err == nil {
		t.Fatal("materializer accepted traversal")
	}
}

func TestTypeScriptCompilerAdapterProducesTypedRelations(t *testing.T) {
	documents := []SourceDocument{
		sourceDocument("src/model.ts", `export interface Entry { amount: number }
export function balance(entries: Entry[]): number {
  return entries.reduce((sum, entry) => sum + entry.amount, 0)
}
`, "typescript"),
		sourceDocument("src/index.ts", `import { balance } from "./model.js"
export const result = balance([{ amount: 2 }])
`, "typescript"),
	}
	bundle := runProcessFixture(t, NewTypeScriptAdapter(toolRoot(t)), documents)
	assertBundleKinds(t, bundle, []string{"function", "interface", "variable"}, []string{"imports", "calls", "references"})
}

func TestPythonASTAdapterPreservesDynamicCallsAsCandidates(t *testing.T) {
	documents := []SourceDocument{
		sourceDocument("app/models.py", `from dataclasses import dataclass

@dataclass
class EntryModel:
    amount: int

def balance(entries: list[EntryModel]) -> int:
    return sum(entry.amount for entry in entries)
`, "python"),
		sourceDocument("tests/test_models.py", `from app.models import EntryModel, balance

def test_balance():
    assert balance([EntryModel(2)]) == 2
`, "python"),
	}
	bundle := runProcessFixture(t, NewPythonAdapter(toolRoot(t), "3.14"), documents)
	assertBundleKinds(t, bundle, []string{"class", "function"}, []string{"imports", "calls", "references"})
	foundCandidate := false
	foundTest := false
	for _, relation := range bundle.Relations {
		if relation.Kind == "calls" && relation.EvidenceClass == "candidate_static" {
			foundCandidate = true
		}
	}
	for _, symbol := range bundle.Symbols {
		if symbol.Name == "test_balance" && symbol.Test {
			foundTest = true
		}
	}
	if !foundCandidate || !foundTest {
		t.Fatalf("candidate=%v test=%v", foundCandidate, foundTest)
	}
}

func runProcessFixture(t *testing.T, adapter Adapter, documents []SourceDocument) IndexBundle {
	t.Helper()
	root := t.TempDir()
	if err := MaterializeDocuments(root, documents); err != nil {
		t.Fatal(err)
	}
	descriptor := adapter.Descriptor()
	tree := CommittedTree{CommitID: strings.Repeat("c", 40), TreeID: strings.Repeat("d", 40)}
	snapshot, err := NewProposedSnapshot(
		indexTenantID, indexRepositoryID, tree, hashText("process-fixture"),
		[]contracts.AdapterDescriptor{descriptor}, "index-test",
		time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	return extractAndNormalize(t, adapter, root, documents, snapshot)
}

func assertBundleKinds(t *testing.T, bundle IndexBundle, symbolKinds, relationKinds []string) {
	t.Helper()
	wantSymbols := make(map[string]bool, len(symbolKinds))
	for _, kind := range symbolKinds {
		wantSymbols[kind] = false
	}
	wantRelations := make(map[string]bool, len(relationKinds))
	for _, kind := range relationKinds {
		wantRelations[kind] = false
	}
	for _, symbol := range bundle.Symbols {
		if _, exists := wantSymbols[symbol.Kind]; exists {
			wantSymbols[symbol.Kind] = true
		}
	}
	for _, relation := range bundle.Relations {
		if _, exists := wantRelations[relation.Kind]; exists {
			wantRelations[relation.Kind] = true
		}
	}
	for kind, found := range wantSymbols {
		if !found {
			t.Errorf("missing %s symbol", kind)
		}
	}
	for kind, found := range wantRelations {
		if !found {
			t.Errorf("missing %s relation", kind)
		}
	}
}

func toolRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func extractAndNormalize(
	t *testing.T,
	adapter Adapter,
	root string,
	documents []SourceDocument,
	snapshot contracts.RepositorySnapshot,
) IndexBundle {
	t.Helper()
	result, err := adapter.Extract(context.Background(), root, documents)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := NormalizeResults(snapshot, documents, []RawAdapterResult{result})
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func sourceDocument(path, body, language string) SourceDocument {
	digest := sha256.Sum256([]byte(body))
	blob := sha256.Sum256([]byte("blob:" + path + ":" + body))
	return SourceDocument{
		CommittedFile: CommittedFile{
			Path: path, Mode: "100644", GitBlobID: hex.EncodeToString(blob[:20]),
			SizeBytes: int64(len(body)), Language: language,
		},
		SourceHash: "sha256:" + hex.EncodeToString(digest[:]), Body: []byte(body),
	}
}

func TestMain(m *testing.M) {
	// packages.Load needs a writable cache even when module downloads are off.
	if os.Getenv("GOCACHE") == "" {
		cache, err := os.MkdirTemp("", "forja-index-go-cache-")
		if err == nil {
			_ = os.Setenv("GOCACHE", filepath.Clean(cache))
			code := m.Run()
			_ = os.RemoveAll(cache)
			os.Exit(code)
		}
	}
	os.Exit(m.Run())
}
