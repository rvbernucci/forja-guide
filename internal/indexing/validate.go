package indexing

import (
	"fmt"
	"sort"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

// ValidateBundle proves that every card belongs to the declared snapshot and
// that all local relation and symbol references close over the same bundle.
func ValidateBundle(bundle IndexBundle) error {
	if err := contracts.ValidateRepositorySnapshot(bundle.Snapshot); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	if bundle.Files == nil || bundle.Symbols == nil || bundle.Relations == nil {
		return fmt.Errorf("index bundle collections must be present")
	}
	if bundle.Snapshot.Counts.Files != len(bundle.Files) ||
		bundle.Snapshot.Counts.Symbols != len(bundle.Symbols) ||
		bundle.Snapshot.Counts.Relations != len(bundle.Relations) {
		return fmt.Errorf("snapshot counts do not match the bundle")
	}
	files := make(map[string]contracts.FileCard, len(bundle.Files))
	paths := make(map[string]struct{}, len(bundle.Files))
	diagnostics := 0
	for _, file := range bundle.Files {
		if err := contracts.ValidateFileCard(file); err != nil {
			return fmt.Errorf("file %q: %w", file.FileID, err)
		}
		if file.SnapshotID != bundle.Snapshot.SnapshotID ||
			file.RepositoryID != bundle.Snapshot.RepositoryID ||
			file.SourceCommit != bundle.Snapshot.SourceCommit {
			return fmt.Errorf("file %q escapes the snapshot authority", file.FileID)
		}
		if _, exists := files[file.FileID]; exists {
			return fmt.Errorf("duplicate file %q", file.FileID)
		}
		if _, exists := paths[file.Path]; exists {
			return fmt.Errorf("duplicate file path %q", file.Path)
		}
		files[file.FileID], paths[file.Path] = file, struct{}{}
		for _, diagnostic := range file.Diagnostics {
			diagnostics += diagnostic.Count
		}
	}
	if diagnostics != bundle.Snapshot.Counts.Diagnostics {
		return fmt.Errorf("diagnostic count does not match the bundle")
	}
	symbols := make(map[string]contracts.SymbolCard, len(bundle.Symbols))
	fileSymbols := make(map[string][]string, len(files))
	for _, symbol := range bundle.Symbols {
		if err := contracts.ValidateSymbolCard(symbol); err != nil {
			return fmt.Errorf("symbol %q: %w", symbol.SymbolID, err)
		}
		if symbol.SnapshotID != bundle.Snapshot.SnapshotID {
			return fmt.Errorf("symbol %q escapes the snapshot authority", symbol.SymbolID)
		}
		if _, exists := files[symbol.FileID]; !exists {
			return fmt.Errorf("symbol %q references an unknown file", symbol.SymbolID)
		}
		if files[symbol.FileID].LineageID != symbol.FileLineageID {
			return fmt.Errorf("symbol %q carries the wrong file lineage", symbol.SymbolID)
		}
		if _, exists := symbols[symbol.SymbolID]; exists {
			return fmt.Errorf("duplicate symbol %q", symbol.SymbolID)
		}
		symbols[symbol.SymbolID] = symbol
		fileSymbols[symbol.FileID] = append(fileSymbols[symbol.FileID], symbol.SymbolID)
	}
	for fileID, file := range files {
		actual := fileSymbols[fileID]
		sort.Strings(actual)
		if !equalStrings(actual, file.SymbolIDs) {
			return fmt.Errorf("file %q symbol closure does not match", fileID)
		}
	}
	relations := make(map[string]struct{}, len(bundle.Relations))
	for _, relation := range bundle.Relations {
		if err := contracts.ValidateRelationEvidence(relation); err != nil {
			return fmt.Errorf("relation %q: %w", relation.RelationID, err)
		}
		if relation.SnapshotID != bundle.Snapshot.SnapshotID {
			return fmt.Errorf("relation %q escapes the snapshot authority", relation.RelationID)
		}
		if _, exists := files[relation.SourceFileID]; !exists {
			return fmt.Errorf("relation %q references an unknown source file", relation.RelationID)
		}
		if !knownLocalEntity(relation.SourceEntityID, files, symbols) {
			return fmt.Errorf("relation %q references an unknown source entity", relation.RelationID)
		}
		if sourceSymbol, isSymbol := symbols[relation.SourceEntityID]; isSymbol &&
			sourceSymbol.FileID != relation.SourceFileID {
			return fmt.Errorf("relation %q source symbol belongs to another file", relation.RelationID)
		}
		if _, isFile := files[relation.SourceEntityID]; isFile &&
			relation.SourceEntityID != relation.SourceFileID {
			return fmt.Errorf("relation %q source file disagrees with its locator file", relation.RelationID)
		}
		if relation.TargetEntityID != nil && !knownTargetEntity(*relation.TargetEntityID, files, symbols) {
			return fmt.Errorf("relation %q references an unknown target entity", relation.RelationID)
		}
		if _, exists := relations[relation.RelationID]; exists {
			return fmt.Errorf("duplicate relation %q", relation.RelationID)
		}
		relations[relation.RelationID] = struct{}{}
	}
	return nil
}

func knownLocalEntity(id string, files map[string]contracts.FileCard, symbols map[string]contracts.SymbolCard) bool {
	_, file := files[id]
	_, symbol := symbols[id]
	return file || symbol
}

func knownTargetEntity(id string, files map[string]contracts.FileCard, symbols map[string]contracts.SymbolCard) bool {
	if len(id) > len("external_") && id[:len("external_")] == "external_" {
		return true
	}
	return knownLocalEntity(id, files, symbols)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
