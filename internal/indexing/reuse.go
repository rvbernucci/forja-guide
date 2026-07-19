package indexing

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

// RebindReusableAdapters carries forward exact adapter evidence after the
// invalidation planner proves every source owned by that adapter reusable.
func RebindReusableAdapters(
	target, baseline IndexBundle,
	reusableDescriptors []contracts.AdapterDescriptor,
) (IndexBundle, error) {
	if err := ValidateBundle(target); err != nil {
		return IndexBundle{}, fmt.Errorf("target: %w", err)
	}
	if len(reusableDescriptors) == 0 {
		return target, nil
	}
	if err := ValidateBundle(baseline); err != nil {
		return IndexBundle{}, fmt.Errorf("baseline: %w", err)
	}
	reusable := make(map[string]contracts.AdapterDescriptor, len(reusableDescriptors))
	targetAdapters := make(map[string]contracts.AdapterDescriptor, len(target.Snapshot.Adapters))
	for _, descriptor := range target.Snapshot.Adapters {
		targetAdapters[adapterKey(descriptor)] = descriptor
	}
	for _, descriptor := range reusableDescriptors {
		key := adapterKey(descriptor)
		if targetAdapters[key] != descriptor {
			return IndexBundle{}, fmt.Errorf("reused adapter is not authorized by target snapshot")
		}
		reusable[key] = descriptor
	}
	targetFileByPath := make(map[string]*contracts.FileCard, len(target.Files))
	targetFileByID := make(map[string]*contracts.FileCard, len(target.Files))
	for index := range target.Files {
		targetFileByPath[target.Files[index].Path] = &target.Files[index]
		targetFileByID[target.Files[index].FileID] = &target.Files[index]
	}
	entityMap := make(map[string]string, len(baseline.Files)+len(baseline.Symbols))
	for _, file := range baseline.Files {
		current := targetFileByPath[file.Path]
		if current != nil && current.SourceHash == file.SourceHash {
			entityMap[file.FileID] = current.FileID
		}
	}
	for _, file := range baseline.Files {
		if !descriptorOwnsLanguage(reusable, file.Language) {
			continue
		}
		current := targetFileByPath[file.Path]
		if current == nil || current.SourceHash != file.SourceHash || current.LineageID != file.LineageID {
			return IndexBundle{}, fmt.Errorf("reused adapter file %q lacks exact target evidence", file.Path)
		}
		current.Diagnostics = append([]contracts.DiagnosticSummary(nil), file.Diagnostics...)
		if current.Diagnostics == nil {
			current.Diagnostics = []contracts.DiagnosticSummary{}
		}
	}
	targetSymbolByLineage := make(map[string]string, len(target.Symbols))
	for _, symbol := range target.Symbols {
		targetSymbolByLineage[symbol.LineageID] = symbol.SymbolID
	}
	for _, previous := range baseline.Symbols {
		if current := targetSymbolByLineage[previous.LineageID]; current != "" {
			entityMap[previous.SymbolID] = current
		}
	}
	for _, symbol := range baseline.Symbols {
		fileID := entityMap[symbol.FileID]
		file := targetFileByID[fileID]
		if file == nil || !descriptorOwnsLanguage(reusable, symbol.Language) {
			continue
		}
		current := symbol
		current.SnapshotID = target.Snapshot.SnapshotID
		current.FileID = file.FileID
		current.FileLineageID = file.LineageID
		current.SymbolID = contracts.ComputeSymbolID(current)
		current.LineageID = contracts.ComputeSymbolLineageID(current)
		if err := contracts.ValidateSymbolCard(current); err != nil {
			return IndexBundle{}, err
		}
		entityMap[symbol.SymbolID] = current.SymbolID
		target.Symbols = append(target.Symbols, current)
	}
	for _, relation := range baseline.Relations {
		if _, ok := reusable[adapterKey(relation.Adapter)]; !ok {
			continue
		}
		current := relation
		current.SnapshotID = target.Snapshot.SnapshotID
		current.SourceFileID = entityMap[relation.SourceFileID]
		current.SourceEntityID = entityMap[relation.SourceEntityID]
		if current.SourceFileID == "" || current.SourceEntityID == "" {
			return IndexBundle{}, fmt.Errorf("reused relation source lacks target evidence")
		}
		if relation.TargetEntityID != nil {
			targetID := *relation.TargetEntityID
			if !strings.HasPrefix(targetID, "external_") {
				targetID = entityMap[targetID]
				if targetID == "" {
					return IndexBundle{}, fmt.Errorf("reused relation target lacks target evidence")
				}
			}
			current.TargetEntityID = &targetID
		}
		current.RelationID = contracts.ComputeRelationID(current)
		if err := contracts.ValidateRelationEvidence(current); err != nil {
			return IndexBundle{}, err
		}
		target.Relations = append(target.Relations, current)
	}
	sort.Slice(target.Symbols, func(i, j int) bool { return target.Symbols[i].SymbolID < target.Symbols[j].SymbolID })
	sort.Slice(target.Relations, func(i, j int) bool { return target.Relations[i].RelationID < target.Relations[j].RelationID })
	for index := range target.Files {
		target.Files[index].SymbolIDs = []string{}
	}
	for _, symbol := range target.Symbols {
		file := targetFileByID[symbol.FileID]
		if file == nil {
			return IndexBundle{}, fmt.Errorf("rebound symbol file is absent")
		}
		file.SymbolIDs = append(file.SymbolIDs, symbol.SymbolID)
	}
	for index := range target.Files {
		sort.Strings(target.Files[index].SymbolIDs)
	}
	target.Snapshot.Counts = contracts.SnapshotCounts{
		Files: len(target.Files), Symbols: len(target.Symbols), Relations: len(target.Relations),
	}
	for _, file := range target.Files {
		for _, diagnostic := range file.Diagnostics {
			target.Snapshot.Counts.Diagnostics += diagnostic.Count
		}
	}
	if err := ValidateBundle(target); err != nil {
		return IndexBundle{}, fmt.Errorf("rebound bundle: %w", err)
	}
	return target, nil
}

func descriptorOwnsLanguage(
	descriptors map[string]contracts.AdapterDescriptor,
	language string,
) bool {
	for _, descriptor := range descriptors {
		if descriptor.Name == language || descriptor.Name == "typescript" && language == "javascript" {
			return true
		}
	}
	return false
}
