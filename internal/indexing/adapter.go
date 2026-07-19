package indexing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

type SourceDocument struct {
	CommittedFile
	SourceHash string
	Body       []byte
}

type Adapter interface {
	Descriptor() contracts.AdapterDescriptor
	Extract(context.Context, string, []SourceDocument) (RawAdapterResult, error)
}

type RawAdapterResult struct {
	Descriptor  contracts.AdapterDescriptor `json:"descriptor"`
	Symbols     []RawSymbol                 `json:"symbols"`
	Relations   []RawRelation               `json:"relations"`
	Diagnostics []RawDiagnostic             `json:"diagnostics"`
}

type RawSymbol struct {
	Key               string                `json:"key"`
	Path              string                `json:"path"`
	Language          string                `json:"language"`
	Kind              string                `json:"kind"`
	Name              string                `json:"name"`
	QualifiedName     string                `json:"qualified_name"`
	Signature         string                `json:"signature"`
	Declaration       contracts.SourceRange `json:"declaration"`
	Exported          bool                  `json:"exported"`
	Test              bool                  `json:"test"`
	Route             bool                  `json:"route"`
	Schema            bool                  `json:"schema"`
	DocumentationHash *string               `json:"documentation_hash,omitempty"`
}

type RawRelation struct {
	SourceKey      *string               `json:"source_key,omitempty"`
	SourcePath     string                `json:"source_path"`
	Kind           string                `json:"kind"`
	TargetKey      *string               `json:"target_key,omitempty"`
	TargetPath     *string               `json:"target_path,omitempty"`
	ExternalName   *string               `json:"external_name,omitempty"`
	UnresolvedName *string               `json:"unresolved_name,omitempty"`
	EvidenceClass  string                `json:"evidence_class"`
	Locator        contracts.SourceRange `json:"locator"`
}

type RawDiagnostic struct {
	Path     string `json:"path"`
	Severity string `json:"severity"`
	Code     string `json:"code"`
}

type IndexBundle struct {
	Snapshot  contracts.RepositorySnapshot `json:"snapshot"`
	Files     []contracts.FileCard         `json:"files"`
	Symbols   []contracts.SymbolCard       `json:"symbols"`
	Relations []contracts.RelationEvidence `json:"relations"`
}

func LoadDocuments(ctx context.Context, source *GitSource, tree CommittedTree) ([]SourceDocument, error) {
	documents := make([]SourceDocument, 0, len(tree.Files))
	for _, file := range tree.Files {
		body, sourceHash, err := source.ReadFile(ctx, file)
		if err != nil {
			return nil, fmt.Errorf("read committed file %s: %w", file.Path, err)
		}
		documents = append(documents, SourceDocument{CommittedFile: file, SourceHash: sourceHash, Body: body})
	}
	return documents, nil
}

func MaterializeDocuments(root string, documents []SourceDocument) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve materialization root: %w", err)
	}
	for _, document := range documents {
		canonical, err := contracts.NormalizeRepositoryPath(document.Path)
		if err != nil {
			return err
		}
		target := filepath.Join(absRoot, filepath.FromSlash(canonical))
		if !strings.HasPrefix(target, absRoot+string(os.PathSeparator)) {
			return fmt.Errorf("materialized path escapes root")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("create materialized directory: %w", err)
		}
		mode := os.FileMode(0o600)
		if document.Mode == "100755" {
			mode = 0o700
		}
		if err := os.WriteFile(target, document.Body, mode); err != nil {
			return fmt.Errorf("materialize %s: %w", canonical, err)
		}
	}
	return nil
}

func NewProposedSnapshot(
	tenantID, repositoryID string,
	tree CommittedTree,
	configurationHash string,
	adapters []contracts.AdapterDescriptor,
	createdBy string,
	createdAt time.Time,
) (contracts.RepositorySnapshot, error) {
	adapterHash, err := contracts.ComputeAdapterSetHash(adapters)
	if err != nil {
		return contracts.RepositorySnapshot{}, err
	}
	value := contracts.RepositorySnapshot{
		SchemaVersion: contracts.IndexSchemaVersion,
		TenantID:      tenantID, RepositoryID: repositoryID,
		SourceCommit: tree.CommitID, SourceTree: tree.TreeID,
		ConfigurationHash: configurationHash, AdapterSetHash: adapterHash,
		Adapters: adapters, Status: "proposed", Version: 1,
		Counts: contracts.SnapshotCounts{}, CreatedBy: createdBy, CreatedAt: createdAt,
	}
	value.SnapshotID = contracts.ComputeSnapshotID(value)
	if err := contracts.ValidateRepositorySnapshot(value); err != nil {
		return contracts.RepositorySnapshot{}, err
	}
	return value, nil
}

func NormalizeResults(
	snapshot contracts.RepositorySnapshot,
	documents []SourceDocument,
	results []RawAdapterResult,
) (IndexBundle, error) {
	if err := contracts.ValidateRepositorySnapshot(snapshot); err != nil {
		return IndexBundle{}, err
	}
	documentByPath := make(map[string]SourceDocument, len(documents))
	fileByPath := make(map[string]*contracts.FileCard, len(documents))
	files := make([]contracts.FileCard, 0, len(documents))
	for _, document := range documents {
		if _, exists := documentByPath[document.Path]; exists {
			return IndexBundle{}, fmt.Errorf("duplicate source document %q", document.Path)
		}
		documentByPath[document.Path] = document
		file := contracts.FileCard{
			SchemaVersion: contracts.IndexSchemaVersion, SnapshotID: snapshot.SnapshotID,
			RepositoryID: snapshot.RepositoryID, SourceCommit: snapshot.SourceCommit,
			Path: document.Path, GitBlobID: document.GitBlobID, SourceHash: document.SourceHash,
			SizeBytes: document.SizeBytes, Language: document.Language, Generated: document.Generated,
			SymbolIDs: []string{}, Diagnostics: []contracts.DiagnosticSummary{},
		}
		file.FileID = contracts.ComputeFileID(file)
		file.LineageID = contracts.ComputeFileLineageID(file)
		files = append(files, file)
		fileByPath[file.Path] = &files[len(files)-1]
	}

	resultByAdapter := make(map[string]RawAdapterResult, len(results))
	for _, result := range results {
		key := adapterKey(result.Descriptor)
		if _, exists := resultByAdapter[key]; exists {
			return IndexBundle{}, fmt.Errorf("duplicate adapter result %q", key)
		}
		resultByAdapter[key] = result
	}
	for _, descriptor := range snapshot.Adapters {
		if _, exists := resultByAdapter[adapterKey(descriptor)]; !exists {
			return IndexBundle{}, fmt.Errorf("missing result for adapter %q", descriptor.Name)
		}
	}
	if len(resultByAdapter) != len(snapshot.Adapters) {
		return IndexBundle{}, fmt.Errorf("adapter result set exceeds the authorized snapshot")
	}

	type boundSymbol struct {
		raw     RawSymbol
		card    contracts.SymbolCard
		adapter contracts.AdapterDescriptor
	}
	bound := make([]boundSymbol, 0)
	diagnosticCounts := make(map[string]map[string]int)
	allRelations := make([]struct {
		raw     RawRelation
		adapter contracts.AdapterDescriptor
	}, 0)
	for _, descriptor := range snapshot.Adapters {
		result := resultByAdapter[adapterKey(descriptor)]
		if result.Descriptor != descriptor {
			return IndexBundle{}, fmt.Errorf("adapter descriptor mismatch")
		}
		for _, raw := range result.Symbols {
			file := fileByPath[raw.Path]
			document, exists := documentByPath[raw.Path]
			if file == nil || !exists || raw.Language != file.Language || !rangeWithin(raw.Declaration, len(document.Body)) {
				return IndexBundle{}, fmt.Errorf("adapter %s emitted an out-of-scope symbol", descriptor.Name)
			}
			card := contracts.SymbolCard{
				SchemaVersion: contracts.IndexSchemaVersion, SnapshotID: snapshot.SnapshotID,
				FileID: file.FileID, FileLineageID: file.LineageID,
				Language: raw.Language, Kind: raw.Kind,
				Name: raw.Name, QualifiedName: raw.QualifiedName, Signature: raw.Signature,
				Declaration: raw.Declaration, Exported: raw.Exported, Test: raw.Test,
				Route: raw.Route, Schema: raw.Schema, DocumentationHash: raw.DocumentationHash,
			}
			card.SymbolID = contracts.ComputeSymbolID(card)
			card.LineageID = contracts.ComputeSymbolLineageID(card)
			if err := contracts.ValidateSymbolCard(card); err != nil {
				return IndexBundle{}, fmt.Errorf("adapter %s symbol: %w", descriptor.Name, err)
			}
			bound = append(bound, boundSymbol{raw: raw, card: card, adapter: descriptor})
		}
		for _, diagnostic := range result.Diagnostics {
			if fileByPath[diagnostic.Path] == nil || !validDiagnostic(diagnostic) {
				return IndexBundle{}, fmt.Errorf("adapter %s emitted an invalid diagnostic", descriptor.Name)
			}
			if diagnosticCounts[diagnostic.Path] == nil {
				diagnosticCounts[diagnostic.Path] = make(map[string]int)
			}
			diagnosticCounts[diagnostic.Path][diagnostic.Severity+"\x00"+diagnostic.Code]++
		}
		for _, relation := range result.Relations {
			allRelations = append(allRelations, struct {
				raw     RawRelation
				adapter contracts.AdapterDescriptor
			}{relation, descriptor})
		}
	}
	sort.Slice(bound, func(i, j int) bool {
		left, right := bound[i].raw, bound[j].raw
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		if left.Declaration.Start.Offset != right.Declaration.Start.Offset {
			return left.Declaration.Start.Offset < right.Declaration.Start.Offset
		}
		return left.Key < right.Key
	})
	symbolByKey := make(map[string]string, len(bound))
	symbols := make([]contracts.SymbolCard, 0, len(bound))
	for _, item := range bound {
		if strings.TrimSpace(item.raw.Key) == "" {
			return IndexBundle{}, fmt.Errorf("adapter emitted an empty symbol key")
		}
		if _, exists := symbolByKey[item.raw.Key]; exists {
			return IndexBundle{}, fmt.Errorf("adapter symbol key %q is ambiguous", item.raw.Key)
		}
		symbolByKey[item.raw.Key] = item.card.SymbolID
		symbols = append(symbols, item.card)
		fileByPath[item.raw.Path].SymbolIDs = append(fileByPath[item.raw.Path].SymbolIDs, item.card.SymbolID)
	}
	for index := range files {
		sort.Strings(files[index].SymbolIDs)
		for key, count := range diagnosticCounts[files[index].Path] {
			parts := strings.SplitN(key, "\x00", 2)
			files[index].Diagnostics = append(files[index].Diagnostics, contracts.DiagnosticSummary{
				Severity: parts[0], Code: parts[1], Count: count,
			})
		}
		sort.Slice(files[index].Diagnostics, func(i, j int) bool {
			left := files[index].Diagnostics[i]
			right := files[index].Diagnostics[j]
			return left.Severity+"\x00"+left.Code < right.Severity+"\x00"+right.Code
		})
		if err := contracts.ValidateFileCard(files[index]); err != nil {
			return IndexBundle{}, err
		}
	}
	relations := make([]contracts.RelationEvidence, 0, len(allRelations)+len(bound))
	for _, item := range bound {
		raw := RawRelation{
			SourcePath: item.raw.Path, Kind: "declares", TargetKey: &item.raw.Key,
			EvidenceClass: "confirmed_static", Locator: item.raw.Declaration,
		}
		relation, err := bindRelation(snapshot, raw, item.adapter, fileByPath, documentByPath, symbolByKey)
		if err != nil {
			return IndexBundle{}, err
		}
		relations = append(relations, relation)
	}
	for _, item := range allRelations {
		relation, err := bindRelation(snapshot, item.raw, item.adapter, fileByPath, documentByPath, symbolByKey)
		if err != nil {
			return IndexBundle{}, err
		}
		relations = append(relations, relation)
	}
	sort.Slice(relations, func(i, j int) bool { return relations[i].RelationID < relations[j].RelationID })
	for index := 1; index < len(relations); index++ {
		if relations[index].RelationID == relations[index-1].RelationID {
			return IndexBundle{}, fmt.Errorf("duplicate canonical relation")
		}
	}
	snapshot.Counts = contracts.SnapshotCounts{
		Files: len(files), Symbols: len(symbols), Relations: len(relations),
	}
	for _, file := range files {
		for _, diagnostic := range file.Diagnostics {
			snapshot.Counts.Diagnostics += diagnostic.Count
		}
	}
	return IndexBundle{Snapshot: snapshot, Files: files, Symbols: symbols, Relations: relations}, nil
}

func bindRelation(
	snapshot contracts.RepositorySnapshot,
	raw RawRelation,
	descriptor contracts.AdapterDescriptor,
	files map[string]*contracts.FileCard,
	documents map[string]SourceDocument,
	symbols map[string]string,
) (contracts.RelationEvidence, error) {
	file := files[raw.SourcePath]
	document, exists := documents[raw.SourcePath]
	if file == nil || !exists || !rangeWithin(raw.Locator, len(document.Body)) {
		return contracts.RelationEvidence{}, fmt.Errorf("relation source is outside the snapshot")
	}
	sourceID := file.FileID
	if raw.SourceKey != nil {
		var found bool
		sourceID, found = symbols[*raw.SourceKey]
		if !found {
			return contracts.RelationEvidence{}, fmt.Errorf("relation source symbol is unresolved")
		}
	}
	resolvedTargets := 0
	var targetID *string
	if raw.TargetKey != nil {
		value, found := symbols[*raw.TargetKey]
		if !found {
			return contracts.RelationEvidence{}, fmt.Errorf("relation target symbol is unresolved")
		}
		targetID, resolvedTargets = &value, resolvedTargets+1
	}
	if raw.TargetPath != nil {
		target := files[*raw.TargetPath]
		if target == nil {
			return contracts.RelationEvidence{}, fmt.Errorf("relation target file is unresolved")
		}
		value := target.FileID
		targetID, resolvedTargets = &value, resolvedTargets+1
	}
	if raw.ExternalName != nil {
		value := contracts.StableIndexID("external", *raw.ExternalName)
		targetID, resolvedTargets = &value, resolvedTargets+1
	}
	if resolvedTargets > 1 || resolvedTargets == 1 && raw.UnresolvedName != nil ||
		resolvedTargets == 0 && raw.UnresolvedName == nil {
		return contracts.RelationEvidence{}, fmt.Errorf("relation target evidence is ambiguous")
	}
	evidenceProjection, err := json.Marshal(raw)
	if err != nil {
		return contracts.RelationEvidence{}, err
	}
	digest := sha256.Sum256(evidenceProjection)
	relation := contracts.RelationEvidence{
		SchemaVersion: contracts.IndexSchemaVersion, SnapshotID: snapshot.SnapshotID,
		SourceEntityID: sourceID, Kind: raw.Kind, SourceFileID: file.FileID,
		EvidenceClass: raw.EvidenceClass, Locator: raw.Locator,
		EvidenceHash: "sha256:" + hex.EncodeToString(digest[:]), Adapter: descriptor,
	}
	if resolvedTargets == 1 {
		relation.Resolution = "resolved"
		relation.TargetEntityID = targetID
	} else {
		relation.Resolution = "unresolved"
		relation.UnresolvedName = raw.UnresolvedName
	}
	relation.RelationID = contracts.ComputeRelationID(relation)
	if err := contracts.ValidateRelationEvidence(relation); err != nil {
		return contracts.RelationEvidence{}, err
	}
	return relation, nil
}

func adapterKey(value contracts.AdapterDescriptor) string {
	return value.Name + "\x00" + value.Version + "\x00" + value.ConfigurationHash + "\x00" + value.CapabilityHash
}

// ConfigurationHash exposes the canonical SHA-256 form used by index plans.
func ConfigurationHash(parts ...string) string {
	return hashText(strings.Join(parts, "\x00"))
}

func rangeWithin(value contracts.SourceRange, size int) bool {
	return value.Start.Line >= 1 && value.Start.Column >= 1 && value.Start.Offset >= 0 &&
		value.End.Line >= value.Start.Line && value.End.Column >= 1 &&
		value.End.Offset >= value.Start.Offset && value.End.Offset <= size
}

func validDiagnostic(value RawDiagnostic) bool {
	return (value.Severity == "info" || value.Severity == "warning" || value.Severity == "error") &&
		strings.TrimSpace(value.Code) != "" && len(value.Code) <= 160
}
