package indexing

import (
	"bytes"
	"testing"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

func TestCanonicalIndexArtifactIsByteStableAndUnbound(t *testing.T) {
	bundle := runProcessFixture(t, NewPythonAdapter(toolRoot(t), "3.14"), []SourceDocument{
		sourceDocument("app.py", "value = 42\n", "python"),
	})
	first, err := MarshalCanonicalBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	second, err := MarshalCanonicalBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || len(first) == 0 || first[len(first)-1] != '\n' {
		t.Fatal("canonical artifact bytes are unstable")
	}
	active := bundle
	active.Snapshot.Status = "active"
	if _, err := MarshalCanonicalBundle(active); err == nil {
		t.Fatal("artifact encoder accepted a lifecycle-bound snapshot")
	}
}

func TestCanonicalIndexArtifactIgnoresCallerCollectionOrder(t *testing.T) {
	bundle := runProcessFixture(t, NewPythonAdapter(toolRoot(t), "3.14"), []SourceDocument{
		sourceDocument("a.py", "def alpha():\n    return 1\n", "python"),
		sourceDocument("b.py", "def beta():\n    return 2\n", "python"),
	})
	permuted := bundle
	permuted.Files = reverseFiles(bundle.Files)
	permuted.Symbols = reverseSymbols(bundle.Symbols)
	permuted.Relations = reverseRelations(bundle.Relations)
	first, err := MarshalCanonicalBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	second, err := MarshalCanonicalBundle(permuted)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("caller collection order changed canonical artifact bytes")
	}
}

func reverseFiles(values []contracts.FileCard) []contracts.FileCard {
	result := append([]contracts.FileCard(nil), values...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func reverseSymbols(values []contracts.SymbolCard) []contracts.SymbolCard {
	result := append([]contracts.SymbolCard(nil), values...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func reverseRelations(values []contracts.RelationEvidence) []contracts.RelationEvidence {
	result := append([]contracts.RelationEvidence(nil), values...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}
