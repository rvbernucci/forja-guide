package indexing

import (
	"strings"
	"testing"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

func TestValidateBundleRejectsOpenReferencesAndCountDrift(t *testing.T) {
	bundle := runProcessFixture(t, NewPythonAdapter(toolRoot(t), "3.14"), []SourceDocument{
		sourceDocument("app/main.py", "def answer():\n    return 42\n", "python"),
	})
	if err := ValidateBundle(bundle); err != nil {
		t.Fatalf("valid bundle: %v", err)
	}
	drifted := bundle
	drifted.Snapshot.Counts.Symbols++
	if err := ValidateBundle(drifted); err == nil {
		t.Fatal("count drift was accepted")
	}
	if len(bundle.Relations) == 0 {
		t.Fatal("fixture has no relations")
	}
	open := bundle
	open.Relations = append([]contracts.RelationEvidence(nil), bundle.Relations...)
	open.Relations[0].SourceEntityID = "symbol_" + strings.Repeat("f", 64)
	open.Relations[0].RelationID = contracts.ComputeRelationID(open.Relations[0])
	if err := ValidateBundle(open); err == nil {
		t.Fatal("open source reference was accepted")
	}
}
