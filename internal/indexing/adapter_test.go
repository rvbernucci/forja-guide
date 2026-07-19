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
