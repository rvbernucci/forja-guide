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
	documents := []SourceDocument{sourceDocument("app.py", "def value() -> int:\n    return 1\n\nresult = value()\n", "python")}
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

func TestPythonLocalImportPropagatesInvalidation(t *testing.T) {
	baseDocuments := []SourceDocument{
		sourceDocument("app/models.py", "def value() -> int:\n    return 1\n", "python"),
		sourceDocument("app/main.py", "from app.models import value\nresult = value()\n", "python"),
	}
	adapter := NewPythonAdapter(toolRoot(t), "3.14")
	baseline := processBundleAtCommit(t, adapter, baseDocuments, strings.Repeat("3", 40))
	targetDocuments := append([]SourceDocument(nil), baseDocuments...)
	targetDocuments[0] = sourceDocument("app/models.py", "def value() -> int:\n    return 2\n", "python")
	target := proposedSnapshotAtCommit(t, baseline.Snapshot, strings.Repeat("4", 40))
	plan, err := PlanInvalidation(
		baseline, target, targetDocuments,
		GitChangeSet{
			BaseCommit:   baseline.Snapshot.SourceCommit,
			TargetCommit: target.SourceCommit,
			Changes:      []GitChange{{Kind: "modified", Path: "app/models.py"}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(plan.AffectedPaths, "app/models.py") || !contains(plan.AffectedPaths, "app/main.py") || len(plan.ReusableFiles) != 0 {
		t.Fatalf("plan=%#v", plan)
	}
}

func TestBundleDeltasUseLineageAcrossCommitScopedIDs(t *testing.T) {
	documents := []SourceDocument{sourceDocument("app.py", "def value() -> int:\n    return 1\n\nresult = value()\n", "python")}
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
	foundRelation := false
	for _, delta := range deltas {
		if delta.ChangeKind != "reused" || delta.PreviousEntityID == nil {
			t.Fatalf("delta=%#v", delta)
		}
		if delta.EntityKind == "relation" {
			foundRelation = true
		}
	}
	if !foundRelation {
		t.Fatal("relation deltas were omitted")
	}
}

func TestLineageComparisonPreservesEveryOverloadedEntity(t *testing.T) {
	t.Parallel()
	baseline := lineageVersions{"shared": {
		{ID: "before-a", Fingerprint: "same"},
		{ID: "before-b", Fingerprint: "old"},
	}}
	target := lineageVersions{"shared": {
		{ID: "after-a", Fingerprint: "same"},
		{ID: "after-b", Fingerprint: "new"},
		{ID: "after-c", Fingerprint: "newer"},
	}}
	deltas := compareLineages("symbol", baseline, target)
	if len(deltas) != 3 {
		t.Fatalf("deltas=%#v", deltas)
	}
	counts := make(map[string]int)
	for _, delta := range deltas {
		counts[delta.ChangeKind]++
	}
	if counts["reused"] != 1 || counts["modified"] != 1 || counts["added"] != 1 {
		t.Fatalf("delta counts=%v", counts)
	}
}

func TestInvalidationRejectsIncompleteOrContradictoryChangeSets(t *testing.T) {
	documents := []SourceDocument{sourceDocument("app.py", "value = 1\n", "python")}
	adapter := NewPythonAdapter(toolRoot(t), "3.14")
	baseline := processBundleAtCommit(t, adapter, documents, strings.Repeat("1", 40))
	targetDocuments := []SourceDocument{sourceDocument("app.py", "value = 2\n", "python")}
	target := proposedSnapshotAtCommit(t, baseline.Snapshot, strings.Repeat("2", 40))

	for name, changes := range map[string][]GitChange{
		"omitted":                {},
		"contradictory addition": {{Kind: "added", Path: "app.py"}},
		"unknown kind":           {{Kind: "copied", Path: "app.py"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := PlanInvalidation(
				baseline, target, targetDocuments,
				GitChangeSet{BaseCommit: baseline.Snapshot.SourceCommit, TargetCommit: target.SourceCommit, Changes: changes},
			)
			if err == nil {
				t.Fatal("invalid change set was accepted")
			}
		})
	}
}

func TestBundleDeltasPreserveExplicitFileRename(t *testing.T) {
	adapter := NewPythonAdapter(toolRoot(t), "3.14")
	baseline := processBundleAtCommit(
		t, adapter, []SourceDocument{sourceDocument("old.py", "value = 1\n", "python")},
		strings.Repeat("5", 40),
	)
	target := processBundleAtCommit(
		t, adapter, []SourceDocument{sourceDocument("new.py", "value = 1\n", "python")},
		strings.Repeat("6", 40),
	)
	deltas, err := ComputeBundleDeltasWithChanges(
		baseline, target,
		GitChangeSet{
			BaseCommit:   baseline.Snapshot.SourceCommit,
			TargetCommit: target.Snapshot.SourceCommit,
			Changes: []GitChange{{
				Kind: "renamed", Path: "new.py", FromPath: stringPointer("old.py"),
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, delta := range deltas {
		if delta.EntityKind == "file" && delta.ChangeKind == "renamed" && delta.PreviousEntityID != nil {
			found = true
		}
		if delta.EntityKind == "file" && (delta.ChangeKind == "added" || delta.ChangeKind == "deleted") {
			t.Fatalf("rename leaked as %s: %#v", delta.ChangeKind, delta)
		}
	}
	if !found {
		t.Fatal("file rename delta was omitted")
	}
}

func TestBundleDeltasDoNotInventAmbiguousOrModifiedRenames(t *testing.T) {
	tests := []struct {
		name     string
		baseline []contracts.FileCard
		target   []contracts.FileCard
		deltas   []EntityDelta
	}{
		{
			name: "modified content",
			baseline: []contracts.FileCard{
				{FileID: "old", Path: "old.py", SourceHash: "sha256:old"},
			},
			target: []contracts.FileCard{
				{FileID: "new", Path: "new.py", SourceHash: "sha256:new"},
			},
			deltas: []EntityDelta{
				{ChangeKind: "deleted", EntityKind: "file", EntityID: "old"},
				{ChangeKind: "added", EntityKind: "file", EntityID: "new"},
			},
		},
		{
			name: "duplicate content",
			baseline: []contracts.FileCard{
				{FileID: "old-a", Path: "old-a.py", SourceHash: "sha256:same"},
				{FileID: "old-b", Path: "old-b.py", SourceHash: "sha256:same"},
			},
			target: []contracts.FileCard{
				{FileID: "new-a", Path: "new-a.py", SourceHash: "sha256:same"},
				{FileID: "new-b", Path: "new-b.py", SourceHash: "sha256:same"},
			},
			deltas: []EntityDelta{
				{ChangeKind: "deleted", EntityKind: "file", EntityID: "old-a"},
				{ChangeKind: "deleted", EntityKind: "file", EntityID: "old-b"},
				{ChangeKind: "added", EntityKind: "file", EntityID: "new-a"},
				{ChangeKind: "added", EntityKind: "file", EntityID: "new-b"},
			},
		},
		{
			name: "modified original plus copy of old content",
			baseline: []contracts.FileCard{
				{FileID: "old", Path: "old.py", SourceHash: "sha256:original"},
			},
			target: []contracts.FileCard{
				{FileID: "changed", Path: "old.py", SourceHash: "sha256:changed"},
				{FileID: "copy", Path: "new.py", SourceHash: "sha256:original"},
			},
			deltas: []EntityDelta{
				{ChangeKind: "modified", EntityKind: "file", EntityID: "changed", PreviousEntityID: stringPointer("old")},
				{ChangeKind: "added", EntityKind: "file", EntityID: "copy"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := inferExactFileRenames(test.deltas, test.baseline, test.target)
			if len(result) != len(test.deltas) {
				t.Fatalf("result=%#v", result)
			}
			for _, delta := range result {
				if delta.ChangeKind == "renamed" {
					t.Fatalf("unproven rename=%#v", delta)
				}
			}
		})
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
