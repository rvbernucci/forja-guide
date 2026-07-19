package indexing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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

func TestTypeScriptCompilerAdapterPreservesVariableExportModifiers(t *testing.T) {
	documents := []SourceDocument{sourceDocument("src/exports.ts", `export const publicConst = 1, publicConstSibling = 2
export let publicLet = 3
export var publicVar = 4
const privateConst = 5
export function publicFunction(): number { return publicConst }
`, "typescript")}
	bundle := runProcessFixture(t, NewTypeScriptAdapter(toolRoot(t)), documents)
	want := map[string]bool{
		"publicConst":        true,
		"publicConstSibling": true,
		"publicLet":          true,
		"publicVar":          true,
		"privateConst":       false,
		"publicFunction":     true,
	}
	found := make(map[string]bool, len(want))
	for _, symbol := range bundle.Symbols {
		exported, exists := want[symbol.Name]
		if !exists {
			continue
		}
		found[symbol.Name] = true
		if symbol.Exported != exported {
			t.Errorf("symbol %s exported=%v, want %v", symbol.Name, symbol.Exported, exported)
		}
	}
	for name := range want {
		if !found[name] {
			t.Errorf("missing symbol %s", name)
		}
	}
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
	foundLocalImport := false
	fileIDs := make(map[string]string)
	for _, file := range bundle.Files {
		fileIDs[file.Path] = file.FileID
	}
	for _, relation := range bundle.Relations {
		if relation.Kind == "calls" && relation.EvidenceClass == "candidate_static" {
			foundCandidate = true
		}
		if relation.Kind == "imports" && relation.SourceFileID == fileIDs["tests/test_models.py"] &&
			relation.TargetEntityID != nil && *relation.TargetEntityID == fileIDs["app/models.py"] {
			foundLocalImport = true
		}
	}
	for _, symbol := range bundle.Symbols {
		if symbol.Name == "test_balance" && symbol.Test {
			foundTest = true
		}
	}
	if !foundCandidate || !foundTest || !foundLocalImport {
		t.Fatalf("candidate=%v test=%v local_import=%v", foundCandidate, foundTest, foundLocalImport)
	}
}

func TestProcessAdaptersProduceByteStableEvidence(t *testing.T) {
	fixtures := []struct {
		name      string
		adapter   Adapter
		documents []SourceDocument
	}{
		{
			name: "typescript", adapter: NewTypeScriptAdapter(toolRoot(t)),
			documents: []SourceDocument{sourceDocument("src/main.ts", "export const value: number = 42\n", "typescript")},
		},
		{
			name: "python", adapter: NewPythonAdapter(toolRoot(t), "3.14"),
			documents: []SourceDocument{sourceDocument("app.py", "value: int = 42\n", "python")},
		},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			first := runProcessFixture(t, fixture.adapter, fixture.documents)
			second := runProcessFixture(t, fixture.adapter, fixture.documents)
			firstJSON, err := json.Marshal(first)
			if err != nil {
				t.Fatal(err)
			}
			secondJSON, err := json.Marshal(second)
			if err != nil {
				t.Fatal(err)
			}
			if string(firstJSON) != string(secondJSON) {
				t.Fatal("unchanged source produced non-deterministic process-adapter bytes")
			}
		})
	}
}

func TestProcessAdaptersPreserveOverloadedAndRepeatedDeclarations(t *testing.T) {
	fixtures := []struct {
		name      string
		adapter   Adapter
		document  SourceDocument
		symbol    string
		wantCount int
	}{
		{
			name: "typescript overloads", adapter: NewTypeScriptAdapter(toolRoot(t)),
			document: sourceDocument("overload.ts", `export function parse(value: string): string;
export function parse(value: number): number;
export function parse(value: string | number): string | number { return value; }
`, "typescript"),
			symbol: "parse", wantCount: 3,
		},
		{
			name: "python repeated declarations", adapter: NewPythonAdapter(toolRoot(t), "3.14"),
			document: sourceDocument("repeat.py", `def value():
    return 1

def value():
    return 2
`, "python"),
			symbol: "value", wantCount: 2,
		},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			bundle := runProcessFixture(t, fixture.adapter, []SourceDocument{fixture.document})
			lineages := make(map[string]int)
			ids := make(map[string]struct{})
			count := 0
			for _, symbol := range bundle.Symbols {
				if symbol.Name != fixture.symbol {
					continue
				}
				count++
				lineages[symbol.LineageID]++
				ids[symbol.SymbolID] = struct{}{}
			}
			if count != fixture.wantCount || len(ids) != fixture.wantCount || len(lineages) != 1 {
				t.Fatalf("count=%d ids=%d lineages=%v", count, len(ids), lineages)
			}
		})
	}
}

func TestProcessAdapterDescriptorsMatchLoadedToolchains(t *testing.T) {
	root := toolRoot(t)
	typescript := NewTypeScriptAdapter(root).Descriptor()
	command := exec.Command(
		"node", "--input-type=module", "--eval",
		"import ts from '@typescript/typescript6'; process.stdout.write(ts.version)",
	)
	command.Dir = root
	command.Env = restrictedProcessEnvironment(root)
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(typescript.Version, "compiler-"+string(output)) {
		t.Fatalf("TypeScript descriptor %q does not bind compiler %q", typescript.Version, output)
	}

	python := NewPythonAdapter(root, "3.14").Descriptor()
	command = exec.Command("python3", "-I", "-c", "import sys; print(f'{sys.version_info.major}.{sys.version_info.minor}', end='')")
	command.Env = restrictedProcessEnvironment(root)
	output, err = command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if python.Version != string(output) {
		t.Fatalf("Python descriptor %q does not bind interpreter boundary %q", python.Version, output)
	}
}

func TestProcessAdapterConfigurationBindsExactSourceBytes(t *testing.T) {
	source := filepath.Join(toolRoot(t), "adapters", "python-indexer.py")
	body, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	makeRoot := func(body []byte) string {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "adapters"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "adapters", "python-indexer.py"), body, 0o600); err != nil {
			t.Fatal(err)
		}
		return root
	}
	first := NewPythonAdapter(makeRoot(body), "3.14").Descriptor()
	second := NewPythonAdapter(makeRoot(append(append([]byte(nil), body...), '\n')), "3.14").Descriptor()
	if first.ConfigurationHash == second.ConfigurationHash {
		t.Fatal("changed adapter source preserved its configuration hash")
	}
	missing := NewPythonAdapter(t.TempDir(), "3.14")
	if _, err := missing.Extract(t.Context(), t.TempDir(), []SourceDocument{sourceDocument("app.py", "value = 1\n", "python")}); err == nil {
		t.Fatal("missing adapter source did not fail closed")
	}
}

func TestProcessAdapterRejectsOversizedRequestBeforeLaunch(t *testing.T) {
	adapter := NewPythonAdapter(toolRoot(t), "3.14")
	documents := make([]SourceDocument, 5000)
	for index := range documents {
		documents[index] = SourceDocument{CommittedFile: CommittedFile{
			Path:     strings.Repeat("a", 4090) + fmt.Sprintf("%06d", index),
			Language: "python",
		}}
	}
	if _, err := adapter.Extract(t.Context(), t.TempDir(), documents); err == nil ||
		!strings.Contains(err.Error(), "input limit") {
		t.Fatalf("oversized adapter request error=%v", err)
	}
}

func TestMalformedProcessSourcesRemainExplicitDiagnostics(t *testing.T) {
	fixtures := []struct {
		name     string
		adapter  Adapter
		document SourceDocument
	}{
		{"typescript", NewTypeScriptAdapter(toolRoot(t)), sourceDocument("broken.ts", "export function (\n", "typescript")},
		{"python", NewPythonAdapter(toolRoot(t), "3.14"), sourceDocument("broken.py", "def broken(:\n", "python")},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			bundle := runProcessFixture(t, fixture.adapter, []SourceDocument{fixture.document})
			if bundle.Snapshot.Counts.Diagnostics == 0 || len(bundle.Files[0].Diagnostics) == 0 {
				t.Fatalf("malformed source produced no diagnostic: %#v", bundle)
			}
		})
	}
}

func TestNormalizerRejectsCrossFileSourceSymbolForgery(t *testing.T) {
	documents := []SourceDocument{
		sourceDocument("a.py", "value = 1\n", "python"),
		sourceDocument("b.py", "other = 2\n", "python"),
	}
	descriptor := NewPythonAdapter(toolRoot(t), "3.14").Descriptor()
	snapshot, err := NewProposedSnapshot(
		indexTenantID, indexRepositoryID,
		CommittedTree{CommitID: strings.Repeat("a", 40), TreeID: strings.Repeat("b", 40)},
		indexDigest("forged-source-config"), []contracts.AdapterDescriptor{descriptor},
		"index-test", time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	key := "python:a.py:0:variable:a.value"
	external := "example.external"
	_, err = NormalizeResults(snapshot, documents, []RawAdapterResult{{
		Descriptor: descriptor,
		Symbols: []RawSymbol{{
			Key: key, Path: "a.py", Language: "python", Kind: "variable",
			Name: "value", QualifiedName: "a.value", Declaration: zeroRange(),
		}},
		Relations: []RawRelation{{
			SourceKey: &key, SourcePath: "b.py", Kind: "references",
			ExternalName: &external, EvidenceClass: "candidate_static", Locator: zeroRange(),
		}},
		Diagnostics: []RawDiagnostic{},
	}})
	if err == nil || !strings.Contains(err.Error(), "different file") {
		t.Fatalf("cross-file source relation error=%v", err)
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
