package indexing

import (
	"strings"
	"testing"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

func TestRebindReusableAdapterProducesCompleteValidatedBundle(t *testing.T) {
	documents := []SourceDocument{
		sourceDocument("src/model.ts", "export function value(): number { return 1 }\n", "typescript"),
		sourceDocument("src/main.ts", "import { value } from './model.js'\nexport const result = value()\n", "typescript"),
	}
	adapter := NewTypeScriptAdapter(toolRoot(t))
	baseline := processBundleAtCommit(t, adapter, documents, strings.Repeat("7", 40))
	targetSnapshot := proposedSnapshotAtCommit(t, baseline.Snapshot, strings.Repeat("8", 40))
	partial, err := NormalizeResults(
		targetSnapshot, documents,
		[]RawAdapterResult{{
			Descriptor: adapter.Descriptor(), Symbols: []RawSymbol{},
			Relations: []RawRelation{}, Diagnostics: []RawDiagnostic{},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	rebound, err := RebindReusableAdapters(
		partial, baseline, []contracts.AdapterDescriptor{adapter.Descriptor()},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rebound.Symbols) != len(baseline.Symbols) || len(rebound.Relations) != len(baseline.Relations) {
		t.Fatalf("rebound counts=%#v baseline=%#v", rebound.Snapshot.Counts, baseline.Snapshot.Counts)
	}
	deltas, err := ComputeBundleDeltas(baseline, rebound)
	if err != nil {
		t.Fatal(err)
	}
	for _, delta := range deltas {
		if delta.ChangeKind != "reused" || delta.PreviousEntityID == nil {
			t.Fatalf("delta=%#v", delta)
		}
	}
}

func TestRebindReusableAdapterRejectsChangedSource(t *testing.T) {
	documents := []SourceDocument{sourceDocument("app.py", "value = 1\n", "python")}
	adapter := NewPythonAdapter(toolRoot(t), "3.14")
	baseline := processBundleAtCommit(t, adapter, documents, strings.Repeat("9", 40))
	targetDocuments := []SourceDocument{sourceDocument("app.py", "value = 2\n", "python")}
	targetSnapshot := proposedSnapshotAtCommit(t, baseline.Snapshot, strings.Repeat("a", 40))
	partial, err := NormalizeResults(
		targetSnapshot, targetDocuments,
		[]RawAdapterResult{{
			Descriptor: adapter.Descriptor(), Symbols: []RawSymbol{},
			Relations: []RawRelation{}, Diagnostics: []RawDiagnostic{},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RebindReusableAdapters(
		partial, baseline, []contracts.AdapterDescriptor{adapter.Descriptor()},
	); err == nil {
		t.Fatal("changed source was accepted as reusable adapter evidence")
	}
}
