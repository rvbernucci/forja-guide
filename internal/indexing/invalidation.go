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
		if _, err := contracts.NormalizeRepositoryPath(document.Path); err != nil {
			return InvalidationPlan{}, fmt.Errorf("target document: %w", err)
		}
		if _, exists := targetByPath[document.Path]; exists {
			return InvalidationPlan{}, fmt.Errorf("duplicate target document %q", document.Path)
		}
		targetByPath[document.Path] = document
	}
	if err := validateChangeSetCoverage(baseFilesByPath, targetByPath, changes); err != nil {
		return InvalidationPlan{}, err
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

func validateChangeSetCoverage(
	baseline map[string]contracts.FileCard,
	target map[string]SourceDocument,
	changes GitChangeSet,
) error {
	mentioned := make(map[string]struct{}, len(changes.Changes)*2)
	for _, change := range changes.Changes {
		if _, err := contracts.NormalizeRepositoryPath(change.Path); err != nil {
			return fmt.Errorf("change path: %w", err)
		}
		if _, duplicate := mentioned[change.Path]; duplicate {
			return fmt.Errorf("change set repeats path %q", change.Path)
		}
		mentioned[change.Path] = struct{}{}
		_, hadBefore := baseline[change.Path]
		_, hasAfter := target[change.Path]
		switch change.Kind {
		case "added":
			if hadBefore || !hasAfter || change.FromPath != nil {
				return fmt.Errorf("added change %q contradicts repository states", change.Path)
			}
		case "modified":
			if !hadBefore || !hasAfter || change.FromPath != nil {
				return fmt.Errorf("modified change %q contradicts repository states", change.Path)
			}
		case "deleted":
			if !hadBefore || hasAfter || change.FromPath != nil {
				return fmt.Errorf("deleted change %q contradicts repository states", change.Path)
			}
		case "renamed":
			if change.FromPath == nil {
				return fmt.Errorf("renamed change %q lacks its source path", change.Path)
			}
			if _, err := contracts.NormalizeRepositoryPath(*change.FromPath); err != nil {
				return fmt.Errorf("rename source path: %w", err)
			}
			if _, duplicate := mentioned[*change.FromPath]; duplicate {
				return fmt.Errorf("change set repeats path %q", *change.FromPath)
			}
			mentioned[*change.FromPath] = struct{}{}
			_, oldBefore := baseline[*change.FromPath]
			_, oldAfter := target[*change.FromPath]
			if hadBefore || !hasAfter || !oldBefore || oldAfter || *change.FromPath == change.Path {
				return fmt.Errorf("renamed change %q contradicts repository states", change.Path)
			}
		default:
			return fmt.Errorf("change set carries unsupported kind %q", change.Kind)
		}
	}
	paths := make(map[string]struct{}, len(baseline)+len(target))
	for path := range baseline {
		paths[path] = struct{}{}
	}
	for path := range target {
		paths[path] = struct{}{}
	}
	for path := range paths {
		before, hadBefore := baseline[path]
		after, hasAfter := target[path]
		changed := hadBefore != hasAfter || hadBefore && before.SourceHash != after.SourceHash
		if _, declared := mentioned[path]; changed && !declared {
			return fmt.Errorf("change set omits changed path %q", path)
		}
	}
	return nil
}

func ComputeBundleDeltas(baseline, target IndexBundle) ([]EntityDelta, error) {
	if err := ValidateBundle(baseline); err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	if err := ValidateBundle(target); err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}
	deltas := compareLineages("file", fileLineages(baseline.Files), fileLineages(target.Files))
	deltas = inferExactFileRenames(deltas, baseline.Files, target.Files)
	deltas = append(deltas, compareLineages("symbol", symbolLineages(baseline.Symbols), symbolLineages(target.Symbols))...)
	deltas = append(deltas, compareLineages("relation", relationLineages(baseline), relationLineages(target))...)
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

func ComputeBundleDeltasWithChanges(
	baseline, target IndexBundle,
	changes GitChangeSet,
) ([]EntityDelta, error) {
	if baseline.Snapshot.SourceCommit != changes.BaseCommit || target.Snapshot.SourceCommit != changes.TargetCommit {
		return nil, fmt.Errorf("change set does not bind the supplied bundles")
	}
	return ComputeBundleDeltas(baseline, target)
}

func stringPointer(value string) *string { return &value }

func inferExactFileRenames(
	deltas []EntityDelta,
	baseline, target []contracts.FileCard,
) []EntityDelta {
	baselineByHash := make(map[string][]contracts.FileCard)
	targetByHash := make(map[string][]contracts.FileCard)
	for _, file := range baseline {
		baselineByHash[file.SourceHash] = append(baselineByHash[file.SourceHash], file)
	}
	for _, file := range target {
		targetByHash[file.SourceHash] = append(targetByHash[file.SourceHash], file)
	}
	added := make(map[string]struct{})
	deleted := make(map[string]struct{})
	for _, delta := range deltas {
		if delta.EntityKind != "file" {
			continue
		}
		switch delta.ChangeKind {
		case "added":
			added[delta.EntityID] = struct{}{}
		case "deleted":
			deleted[delta.EntityID] = struct{}{}
		}
	}
	renamedAdded := make(map[string]string)
	renamedDeleted := make(map[string]struct{})
	for sourceHash, before := range baselineByHash {
		after := targetByHash[sourceHash]
		if len(before) != 1 || len(after) != 1 || before[0].Path == after[0].Path {
			continue
		}
		if _, wasDeleted := deleted[before[0].FileID]; !wasDeleted {
			continue
		}
		if _, wasAdded := added[after[0].FileID]; !wasAdded {
			continue
		}
		renamedAdded[after[0].FileID] = before[0].FileID
		renamedDeleted[before[0].FileID] = struct{}{}
	}
	result := make([]EntityDelta, 0, len(deltas))
	for _, delta := range deltas {
		if delta.EntityKind == "file" && delta.ChangeKind == "deleted" {
			if _, renamed := renamedDeleted[delta.EntityID]; renamed {
				continue
			}
		}
		if delta.EntityKind == "file" && delta.ChangeKind == "added" {
			if previous, renamed := renamedAdded[delta.EntityID]; renamed {
				result = append(result, EntityDelta{
					ChangeKind: "renamed", EntityKind: "file", EntityID: delta.EntityID,
					PreviousEntityID: stringPointer(previous),
				})
				continue
			}
		}
		result = append(result, delta)
	}
	return result
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

type lineageVersions map[string][]lineageVersion

func fileLineages(files []contracts.FileCard) lineageVersions {
	result := make(lineageVersions, len(files))
	for _, file := range files {
		result[file.LineageID] = append(
			result[file.LineageID],
			lineageVersion{ID: file.FileID, Fingerprint: file.SourceHash},
		)
	}
	return result
}

func symbolLineages(symbols []contracts.SymbolCard) lineageVersions {
	result := make(lineageVersions, len(symbols))
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
		result[symbol.LineageID] = append(
			result[symbol.LineageID],
			lineageVersion{ID: symbol.SymbolID, Fingerprint: fingerprint},
		)
	}
	return result
}

func relationLineages(bundle IndexBundle) lineageVersions {
	entities := make(map[string]string, len(bundle.Files)+len(bundle.Symbols))
	for _, file := range bundle.Files {
		entities[file.FileID] = file.LineageID
	}
	for _, symbol := range bundle.Symbols {
		entities[symbol.SymbolID] = symbol.LineageID
	}
	result := make(lineageVersions, len(bundle.Relations))
	for _, relation := range bundle.Relations {
		source := entities[relation.SourceEntityID]
		if source == "" {
			source = relation.SourceEntityID
		}
		target := ""
		if relation.TargetEntityID != nil {
			target = entities[*relation.TargetEntityID]
			if target == "" {
				target = *relation.TargetEntityID
			}
		} else if relation.UnresolvedName != nil {
			target = "unresolved:" + *relation.UnresolvedName
		}
		sourceFile := entities[relation.SourceFileID]
		lineage := contracts.StableIndexID(
			"relation_lineage", source, relation.Kind, relation.Resolution, target,
			sourceFile, fmt.Sprintf("%v", relation.Locator),
		)
		fingerprint := contracts.StableIndexID(
			"relation_fingerprint", relation.EvidenceClass, relation.EvidenceHash,
			relation.Adapter.Name, relation.Adapter.Version,
			relation.Adapter.ConfigurationHash, relation.Adapter.CapabilityHash,
		)
		result[lineage] = append(
			result[lineage],
			lineageVersion{ID: relation.RelationID, Fingerprint: fingerprint},
		)
	}
	return result
}

func compareLineages(kind string, baseline, target lineageVersions) []EntityDelta {
	lineages := make(map[string]struct{}, len(baseline)+len(target))
	for lineage := range baseline {
		lineages[lineage] = struct{}{}
	}
	for lineage := range target {
		lineages[lineage] = struct{}{}
	}
	result := make([]EntityDelta, 0, len(lineages))
	for lineage := range lineages {
		before := append([]lineageVersion(nil), baseline[lineage]...)
		after := append([]lineageVersion(nil), target[lineage]...)
		sortLineageVersions(before)
		sortLineageVersions(after)
		unmatchedBefore, unmatchedAfter := make([]lineageVersion, 0), make([]lineageVersion, 0)
		for len(before) > 0 && len(after) > 0 {
			switch {
			case before[0].Fingerprint == after[0].Fingerprint:
				previous := before[0].ID
				result = append(result, EntityDelta{
					ChangeKind: "reused", EntityKind: kind, EntityID: after[0].ID,
					PreviousEntityID: &previous,
				})
				before, after = before[1:], after[1:]
			case before[0].Fingerprint < after[0].Fingerprint:
				unmatchedBefore = append(unmatchedBefore, before[0])
				before = before[1:]
			default:
				unmatchedAfter = append(unmatchedAfter, after[0])
				after = after[1:]
			}
		}
		unmatchedBefore = append(unmatchedBefore, before...)
		unmatchedAfter = append(unmatchedAfter, after...)
		sort.Slice(unmatchedBefore, func(i, j int) bool { return unmatchedBefore[i].ID < unmatchedBefore[j].ID })
		sort.Slice(unmatchedAfter, func(i, j int) bool { return unmatchedAfter[i].ID < unmatchedAfter[j].ID })
		paired := min(len(unmatchedBefore), len(unmatchedAfter))
		for index := range paired {
			previous := unmatchedBefore[index].ID
			result = append(result, EntityDelta{
				ChangeKind: "modified", EntityKind: kind,
				EntityID: unmatchedAfter[index].ID, PreviousEntityID: &previous,
			})
		}
		for _, value := range unmatchedAfter[paired:] {
			result = append(result, EntityDelta{ChangeKind: "added", EntityKind: kind, EntityID: value.ID})
		}
		for _, value := range unmatchedBefore[paired:] {
			result = append(result, EntityDelta{ChangeKind: "deleted", EntityKind: kind, EntityID: value.ID})
		}
	}
	return result
}

func sortLineageVersions(values []lineageVersion) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].Fingerprint == values[j].Fingerprint {
			return values[i].ID < values[j].ID
		}
		return values[i].Fingerprint < values[j].Fingerprint
	})
}
