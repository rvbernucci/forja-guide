package indexing

import (
	"fmt"
	"sort"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

type ReuseCandidate struct {
	Path           string `json:"path"`
	FileLineageID  string `json:"file_lineage_id"`
	PreviousFileID string `json:"previous_file_id"`
	SourceHash     string `json:"source_hash"`
}

type EntityInvalidation struct {
	EntityID   string `json:"entity_id"`
	LineageID  string `json:"lineage_id,omitempty"`
	Reason     string `json:"reason"`
	SourceHash string `json:"source_hash,omitempty"`
}

type InvalidationPlan struct {
	FullReindex   bool                 `json:"full_reindex"`
	AffectedPaths []string             `json:"affected_paths"`
	ReusableFiles []ReuseCandidate     `json:"reusable_files"`
	Invalidations []EntityInvalidation `json:"invalidations"`
}

type EntityDelta struct {
	Ordinal          int     `json:"ordinal"`
	ChangeKind       string  `json:"change_kind"`
	EntityKind       string  `json:"entity_kind"`
	EntityID         string  `json:"entity_id"`
	PreviousEntityID *string `json:"previous_entity_id,omitempty"`
}

// PlanInvalidation computes a conservative, deterministic affected region.
// It reuses only exact source bytes under the same configuration and adapter
// set, and propagates proven dependency edges in the reverse direction.
func PlanInvalidation(
	baseline IndexBundle,
	target contracts.RepositorySnapshot,
	targetDocuments []SourceDocument,
	changes GitChangeSet,
) (InvalidationPlan, error) {
	if err := ValidateBundle(baseline); err != nil {
		return InvalidationPlan{}, fmt.Errorf("baseline: %w", err)
	}
	if err := contracts.ValidateRepositorySnapshot(target); err != nil {
		return InvalidationPlan{}, fmt.Errorf("target: %w", err)
	}
	if baseline.Snapshot.RepositoryID != target.RepositoryID ||
		baseline.Snapshot.TenantID != target.TenantID ||
		baseline.Snapshot.SourceCommit != changes.BaseCommit ||
		target.SourceCommit != changes.TargetCommit {
		return InvalidationPlan{}, fmt.Errorf("change set does not bind the supplied snapshots")
	}
	baseFilesByID := make(map[string]contracts.FileCard, len(baseline.Files))
	baseFilesByPath := make(map[string]contracts.FileCard, len(baseline.Files))
	for _, file := range baseline.Files {
		baseFilesByID[file.FileID], baseFilesByPath[file.Path] = file, file
	}
	symbolFile := make(map[string]string, len(baseline.Symbols))
	for _, symbol := range baseline.Symbols {
		symbolFile[symbol.SymbolID] = symbol.FileID
	}
	targetByPath := make(map[string]SourceDocument, len(targetDocuments))
	for _, document := range targetDocuments {
		if _, exists := targetByPath[document.Path]; exists {
			return InvalidationPlan{}, fmt.Errorf("duplicate target document %q", document.Path)
		}
		targetByPath[document.Path] = document
	}
	full := baseline.Snapshot.ConfigurationHash != target.ConfigurationHash ||
		baseline.Snapshot.AdapterSetHash != target.AdapterSetHash
	reasons := make(map[string]string)
	if full {
		reason := "configuration_changed"
		if baseline.Snapshot.ConfigurationHash == target.ConfigurationHash {
			reason = "adapter_changed"
		}
		for path := range baseFilesByPath {
			reasons[path] = reason
		}
		for path := range targetByPath {
			reasons[path] = reason
		}
	} else {
		structuralChange := false
		for _, change := range changes.Changes {
			reason := "source_changed"
			if change.Kind == "deleted" {
				reason = "deleted"
			}
			reasons[change.Path] = reason
			if change.FromPath != nil {
				reasons[*change.FromPath] = "deleted"
			}
			if change.Kind == "added" || change.Kind == "deleted" || change.Kind == "renamed" {
				structuralChange = true
			}
		}
		reverse := reverseFileDependencies(baseline, baseFilesByID, symbolFile)
		if structuralChange {
			for _, relation := range baseline.Relations {
				if relation.Resolution == "unresolved" {
					if file, exists := baseFilesByID[relation.SourceFileID]; exists {
						setReason(reasons, file.Path, "dependency_changed")
					}
				}
			}
		}
		queue := sortedKeys(reasons)
		for index := 0; index < len(queue); index++ {
			for _, dependent := range reverse[queue[index]] {
				if _, exists := reasons[dependent]; exists {
					continue
				}
				reasons[dependent] = "dependency_changed"
				queue = append(queue, dependent)
			}
			sort.Strings(queue[index+1:])
		}
	}
	plan := InvalidationPlan{FullReindex: full, AffectedPaths: sortedKeys(reasons), ReusableFiles: []ReuseCandidate{}, Invalidations: []EntityInvalidation{}}
	for path, targetDocument := range targetByPath {
		base, exists := baseFilesByPath[path]
		if !exists || reasons[path] != "" || base.SourceHash != targetDocument.SourceHash {
			continue
		}
		plan.ReusableFiles = append(plan.ReusableFiles, ReuseCandidate{
			Path: path, FileLineageID: base.LineageID, PreviousFileID: base.FileID,
			SourceHash: base.SourceHash,
		})
	}
	sort.Slice(plan.ReusableFiles, func(i, j int) bool { return plan.ReusableFiles[i].Path < plan.ReusableFiles[j].Path })
	for _, path := range sortedKeys(reasons) {
		file, exists := baseFilesByPath[path]
		if !exists {
			continue
		}
		plan.Invalidations = append(plan.Invalidations, EntityInvalidation{
			EntityID: file.FileID, LineageID: file.LineageID,
			Reason: reasons[path], SourceHash: file.SourceHash,
		})
		for _, symbol := range baseline.Symbols {
			if symbol.FileID == file.FileID {
				plan.Invalidations = append(plan.Invalidations, EntityInvalidation{
					EntityID: symbol.SymbolID, LineageID: symbol.LineageID,
					Reason: reasons[path],
				})
			}
		}
		for _, relation := range baseline.Relations {
			if relation.SourceFileID == file.FileID {
				plan.Invalidations = append(plan.Invalidations, EntityInvalidation{EntityID: relation.RelationID, Reason: reasons[path]})
			}
		}
	}
	sort.Slice(plan.Invalidations, func(i, j int) bool {
		if plan.Invalidations[i].EntityID != plan.Invalidations[j].EntityID {
			return plan.Invalidations[i].EntityID < plan.Invalidations[j].EntityID
		}
		return plan.Invalidations[i].Reason < plan.Invalidations[j].Reason
	})
	return plan, nil
}

func ComputeBundleDeltas(baseline, target IndexBundle) ([]EntityDelta, error) {
	if err := ValidateBundle(baseline); err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	if err := ValidateBundle(target); err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}
	deltas := compareLineages("file", fileLineages(baseline.Files), fileLineages(target.Files))
	deltas = append(deltas, compareLineages("symbol", symbolLineages(baseline.Symbols), symbolLineages(target.Symbols))...)
	sort.Slice(deltas, func(i, j int) bool {
		left := deltas[i].EntityKind + "\x00" + deltas[i].EntityID + "\x00" + deltas[i].ChangeKind
		right := deltas[j].EntityKind + "\x00" + deltas[j].EntityID + "\x00" + deltas[j].ChangeKind
		return left < right
	})
	for index := range deltas {
		deltas[index].Ordinal = index
	}
	return deltas, nil
}

func reverseFileDependencies(bundle IndexBundle, files map[string]contracts.FileCard, symbolFiles map[string]string) map[string][]string {
	result := make(map[string][]string)
	for _, relation := range bundle.Relations {
		if relation.Resolution != "resolved" || relation.TargetEntityID == nil ||
			!propagatesInvalidation(relation.Kind) {
			continue
		}
		targetFileID := *relation.TargetEntityID
		if symbolFileID, exists := symbolFiles[targetFileID]; exists {
			targetFileID = symbolFileID
		}
		source, sourceOK := files[relation.SourceFileID]
		target, targetOK := files[targetFileID]
		if sourceOK && targetOK && source.Path != target.Path {
			result[target.Path] = appendUnique(result[target.Path], source.Path)
		}
	}
	for path := range result {
		sort.Strings(result[path])
	}
	return result
}

func propagatesInvalidation(kind string) bool {
	switch kind {
	case "imports", "references", "calls", "implements", "extends", "tests", "routes_to", "validates", "uses_schema":
		return true
	default:
		return false
	}
}

func setReason(values map[string]string, path, reason string) {
	if _, exists := values[path]; !exists {
		values[path] = reason
	}
}

func sortedKeys(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

type lineageVersion struct {
	ID          string
	Fingerprint string
}

func fileLineages(files []contracts.FileCard) map[string]lineageVersion {
	result := make(map[string]lineageVersion, len(files))
	for _, file := range files {
		result[file.LineageID] = lineageVersion{ID: file.FileID, Fingerprint: file.SourceHash}
	}
	return result
}

func symbolLineages(symbols []contracts.SymbolCard) map[string]lineageVersion {
	result := make(map[string]lineageVersion, len(symbols))
	for _, symbol := range symbols {
		documentation := ""
		if symbol.DocumentationHash != nil {
			documentation = *symbol.DocumentationHash
		}
		fingerprint := contracts.StableIndexID(
			"symbol_fingerprint", symbol.Signature,
			fmt.Sprintf("%v", symbol.Declaration), fmt.Sprintf("%t", symbol.Exported),
			fmt.Sprintf("%t", symbol.Test), fmt.Sprintf("%t", symbol.Route),
			fmt.Sprintf("%t", symbol.Schema), documentation,
		)
		result[symbol.LineageID] = lineageVersion{ID: symbol.SymbolID, Fingerprint: fingerprint}
	}
	return result
}

func compareLineages(kind string, baseline, target map[string]lineageVersion) []EntityDelta {
	lineages := make(map[string]struct{}, len(baseline)+len(target))
	for lineage := range baseline {
		lineages[lineage] = struct{}{}
	}
	for lineage := range target {
		lineages[lineage] = struct{}{}
	}
	result := make([]EntityDelta, 0, len(lineages))
	for lineage := range lineages {
		before, hadBefore := baseline[lineage]
		after, hasAfter := target[lineage]
		switch {
		case hadBefore && hasAfter && before.Fingerprint == after.Fingerprint:
			previous := before.ID
			result = append(result, EntityDelta{ChangeKind: "reused", EntityKind: kind, EntityID: after.ID, PreviousEntityID: &previous})
		case hadBefore && hasAfter:
			previous := before.ID
			result = append(result, EntityDelta{ChangeKind: "modified", EntityKind: kind, EntityID: after.ID, PreviousEntityID: &previous})
		case hasAfter:
			result = append(result, EntityDelta{ChangeKind: "added", EntityKind: kind, EntityID: after.ID})
		case hadBefore:
			result = append(result, EntityDelta{ChangeKind: "deleted", EntityKind: kind, EntityID: before.ID})
		}
	}
	return result
}
