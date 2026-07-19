package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/indexing"
)

func (s *Store) GetActiveIndexBundle(ctx context.Context) (indexing.IndexBundle, bool, error) {
	return loadIndexBundle(ctx, s.pool, s.tenantID, s.repositoryID, "")
}

type indexBundleQueryer interface {
	indexSnapshotQueryer
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func loadIndexBundle(
	ctx context.Context,
	queryer indexBundleQueryer,
	tenantID, repositoryID, snapshotID string,
) (indexing.IndexBundle, bool, error) {
	snapshot, _, found, err := loadIndexSnapshot(
		ctx, queryer, tenantID, repositoryID, snapshotID, false,
	)
	if err != nil || !found {
		return indexing.IndexBundle{}, found, err
	}
	bundle := indexing.IndexBundle{
		Snapshot: snapshot, Files: []contracts.FileCard{},
		Symbols: []contracts.SymbolCard{}, Relations: []contracts.RelationEvidence{},
	}
	rows, err := queryer.Query(ctx, `
		SELECT file_id, lineage_id, path, git_blob_id, encode(source_sha256,'hex'),
		       size_bytes, language, generated, diagnostics
		FROM forja.index_files
		WHERE tenant_id=$1 AND repository_id=$2 AND snapshot_id=$3
		ORDER BY path, file_id`, tenantID, repositoryID, snapshot.SnapshotID)
	if err != nil {
		return indexing.IndexBundle{}, false, databaseError("postgres.GetActiveIndexBundle.files", err)
	}
	for rows.Next() {
		var file contracts.FileCard
		var sourceHash string
		var diagnostics []byte
		if err := rows.Scan(
			&file.FileID, &file.LineageID, &file.Path, &file.GitBlobID, &sourceHash,
			&file.SizeBytes, &file.Language, &file.Generated, &diagnostics,
		); err != nil {
			rows.Close()
			return indexing.IndexBundle{}, false, databaseError("postgres.GetActiveIndexBundle.file", err)
		}
		file.SchemaVersion = contracts.IndexSchemaVersion
		file.SnapshotID = snapshot.SnapshotID
		file.RepositoryID = snapshot.RepositoryID
		file.SourceCommit = snapshot.SourceCommit
		file.SourceHash = "sha256:" + sourceHash
		file.SymbolIDs = []string{}
		if err := json.Unmarshal(diagnostics, &file.Diagnostics); err != nil {
			rows.Close()
			return indexing.IndexBundle{}, false, fmt.Errorf("decode index file diagnostics: %w", err)
		}
		bundle.Files = append(bundle.Files, file)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return indexing.IndexBundle{}, false, databaseError("postgres.GetActiveIndexBundle.files", err)
	}
	rows.Close()

	rows, err = queryer.Query(ctx, `
		SELECT symbol_id, lineage_id, file_id, language, kind, name, qualified_name,
		       signature, start_line, start_column, start_offset, end_line, end_column,
		       end_offset, exported, is_test, is_route, is_schema,
		       encode(documentation_sha256,'hex')
		FROM forja.index_symbols
		WHERE tenant_id=$1 AND repository_id=$2 AND snapshot_id=$3
		ORDER BY symbol_id`, tenantID, repositoryID, snapshot.SnapshotID)
	if err != nil {
		return indexing.IndexBundle{}, false, databaseError("postgres.GetActiveIndexBundle.symbols", err)
	}
	fileSymbols := make(map[string][]string, len(bundle.Files))
	fileLineage := make(map[string]string, len(bundle.Files))
	for _, file := range bundle.Files {
		fileLineage[file.FileID] = file.LineageID
	}
	for rows.Next() {
		var symbol contracts.SymbolCard
		var documentationHash *string
		if err := rows.Scan(
			&symbol.SymbolID, &symbol.LineageID, &symbol.FileID, &symbol.Language,
			&symbol.Kind, &symbol.Name, &symbol.QualifiedName, &symbol.Signature,
			&symbol.Declaration.Start.Line, &symbol.Declaration.Start.Column,
			&symbol.Declaration.Start.Offset, &symbol.Declaration.End.Line,
			&symbol.Declaration.End.Column, &symbol.Declaration.End.Offset,
			&symbol.Exported, &symbol.Test, &symbol.Route, &symbol.Schema, &documentationHash,
		); err != nil {
			rows.Close()
			return indexing.IndexBundle{}, false, databaseError("postgres.GetActiveIndexBundle.symbol", err)
		}
		symbol.SchemaVersion = contracts.IndexSchemaVersion
		symbol.SnapshotID = snapshot.SnapshotID
		symbol.FileLineageID = fileLineage[symbol.FileID]
		if documentationHash != nil {
			value := "sha256:" + *documentationHash
			symbol.DocumentationHash = &value
		}
		fileSymbols[symbol.FileID] = append(fileSymbols[symbol.FileID], symbol.SymbolID)
		bundle.Symbols = append(bundle.Symbols, symbol)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return indexing.IndexBundle{}, false, databaseError("postgres.GetActiveIndexBundle.symbols", err)
	}
	rows.Close()
	for index := range bundle.Files {
		values := fileSymbols[bundle.Files[index].FileID]
		if values == nil {
			values = []string{}
		}
		bundle.Files[index].SymbolIDs = values
		sort.Strings(bundle.Files[index].SymbolIDs)
	}

	rows, err = queryer.Query(ctx, `
		SELECT relation_id, source_entity_id, kind, resolution, target_entity_id,
		       unresolved_name, evidence_class, source_file_id, start_line,
		       start_column, start_offset, end_line, end_column, end_offset,
		       encode(evidence_sha256,'hex'), adapter
		FROM forja.index_relations
		WHERE tenant_id=$1 AND repository_id=$2 AND snapshot_id=$3
		ORDER BY relation_id`, tenantID, repositoryID, snapshot.SnapshotID)
	if err != nil {
		return indexing.IndexBundle{}, false, databaseError("postgres.GetActiveIndexBundle.relations", err)
	}
	for rows.Next() {
		var relation contracts.RelationEvidence
		var evidenceHash string
		var adapter []byte
		if err := rows.Scan(
			&relation.RelationID, &relation.SourceEntityID, &relation.Kind,
			&relation.Resolution, &relation.TargetEntityID, &relation.UnresolvedName,
			&relation.EvidenceClass, &relation.SourceFileID,
			&relation.Locator.Start.Line, &relation.Locator.Start.Column,
			&relation.Locator.Start.Offset, &relation.Locator.End.Line,
			&relation.Locator.End.Column, &relation.Locator.End.Offset,
			&evidenceHash, &adapter,
		); err != nil {
			rows.Close()
			return indexing.IndexBundle{}, false, databaseError("postgres.GetActiveIndexBundle.relation", err)
		}
		relation.SchemaVersion = contracts.IndexSchemaVersion
		relation.SnapshotID = snapshot.SnapshotID
		relation.EvidenceHash = "sha256:" + evidenceHash
		if err := json.Unmarshal(adapter, &relation.Adapter); err != nil {
			rows.Close()
			return indexing.IndexBundle{}, false, fmt.Errorf("decode index relation adapter: %w", err)
		}
		bundle.Relations = append(bundle.Relations, relation)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return indexing.IndexBundle{}, false, databaseError("postgres.GetActiveIndexBundle.relations", err)
	}
	rows.Close()
	if err := indexing.ValidateBundle(bundle); err != nil {
		return indexing.IndexBundle{}, false, fmt.Errorf("validate canonical active index bundle: %w", err)
	}
	return bundle, true, nil
}
