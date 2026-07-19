package indexing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

func TestInvalidationPropagatesProvenDependenciesAndReusesUnrelatedFiles(t *testing.T) {
	baseDocuments := []SourceDocument{
		sourceDocument("src/model.ts", "export function value(): number { return 1 }\n", "typescript"),
		sourceDocument("src/index.ts", "import { value } from './model.js'\nexport const result = value()\n", "typescript"),
		sourceDocument("src/unrelated.ts", "export const stable = true\n", "typescript"),
	}
	adapter := NewTypeScriptAdapter(toolRoot(t))
	baseline := processBundleAtCommit(t, adapter, baseDocuments, strings.Repeat("a", 40))
	targetDocuments := append([]SourceDocument(nil), baseDocuments...)
	targetDocuments[0] = sourceDocument("src/model.ts", "export function value(): number { return 2 }\n", "typescript")
	target := proposedSnapshotAtCommit(t, baseline.Snapshot, strings.Repeat("b", 40))
	plan, err := PlanInvalidation(
		baseline, target, targetDocuments,
		GitChangeSet{BaseCommit: baseline.Snapshot.SourceCommit, TargetCommit: target.SourceCommit, Changes: []GitChange{{Kind: "modified", Path: "src/model.ts"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.FullReindex || !contains(plan.AffectedPaths, "src/model.ts") || !contains(plan.AffectedPaths, "src/index.ts") {
		t.Fatalf("affected=%v full=%v", plan.AffectedPaths, plan.FullReindex)
	}
	if len(plan.ReusableFiles) != 1 || plan.ReusableFiles[0].Path != "src/unrelated.ts" {
		t.Fatalf("reusable=%#v", plan.ReusableFiles)
	}
}

func TestInvalidationAdapterChangeForcesFullReindex(t *testing.T) {
	documents := []SourceDocument{sourceDocument("app.py", "value = 1\n", "python")}
	adapter := NewPythonAdapter(toolRoot(t), "3.14")
	baseline := processBundleAtCommit(t, adapter, documents, strings.Repeat("c", 40))
	target := proposedSnapshotAtCommit(t, baseline.Snapshot, strings.Repeat("d", 40))
	target.AdapterSetHash = indexDigest("different-adapter-set")
	target.SnapshotID = contracts.ComputeSnapshotID(target)
	if err := contracts.ValidateRepositorySnapshot(target); err == nil {
		t.Fatal("forged adapter set unexpectedly validated")
	}
	target = proposedSnapshotAtCommit(t, baseline.Snapshot, strings.Repeat("d", 40))
	target.ConfigurationHash = indexDigest("different-configuration")
	target.SnapshotID = contracts.ComputeSnapshotID(target)
	plan, err := PlanInvalidation(
		baseline, target, documents,
		GitChangeSet{BaseCommit: baseline.Snapshot.SourceCommit, TargetCommit: target.SourceCommit, Changes: []GitChange{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.FullReindex || len(plan.ReusableFiles) != 0 || len(plan.Invalidations) == 0 {
		t.Fatalf("plan=%#v", plan)
	}
}

func TestBundleDeltasUseLineageAcrossCommitScopedIDs(t *testing.T) {
	documents := []SourceDocument{sourceDocument("app.py", "value = 1\n", "python")}
	adapter := NewPythonAdapter(toolRoot(t), "3.14")
	baseline := processBundleAtCommit(t, adapter, documents, strings.Repeat("e", 40))
	target := processBundleAtCommit(t, adapter, documents, strings.Repeat("f", 40))
	deltas, err := ComputeBundleDeltas(baseline, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) == 0 {
		t.Fatal("no deltas")
	}
	for _, delta := range deltas {
		if delta.ChangeKind != "reused" || delta.PreviousEntityID == nil {
			t.Fatalf("delta=%#v", delta)
		}
	}
}

func processBundleAtCommit(t *testing.T, adapter Adapter, documents []SourceDocument, commit string) IndexBundle {
	t.Helper()
	root := t.TempDir()
	if err := MaterializeDocuments(root, documents); err != nil {
		t.Fatal(err)
	}
	descriptor := adapter.Descriptor()
	snapshot, err := NewProposedSnapshot(
		indexTenantID, indexRepositoryID,
		CommittedTree{CommitID: commit, TreeID: strings.Repeat("9", 40)},
		indexDigest("incremental-config"), []contracts.AdapterDescriptor{descriptor},
		"index-test", time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
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

func proposedSnapshotAtCommit(t *testing.T, baseline contracts.RepositorySnapshot, commit string) contracts.RepositorySnapshot {
	t.Helper()
	value, err := NewProposedSnapshot(
		baseline.TenantID, baseline.RepositoryID,
		CommittedTree{CommitID: commit, TreeID: strings.Repeat("8", 40)},
		baseline.ConfigurationHash, baseline.Adapters, baseline.CreatedBy, baseline.CreatedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func indexDigest(value string) string {
	return hashText(value)
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
