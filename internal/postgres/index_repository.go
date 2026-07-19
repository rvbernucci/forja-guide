package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/indexing"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

func (s *Store) PublishIndexSnapshot(
	ctx context.Context,
	publication persistence.IndexPublication,
	metadata runstate.CommandMetadata,
) (contracts.RepositorySnapshot, error) {
	requestHash, requestIdentity, err := s.validateIndexPublication(publication, metadata)
	if err != nil {
		return contracts.RepositorySnapshot{}, err
	}
	snapshot := publication.Bundle.Snapshot
	scope := "index_publish:" + s.repositoryID + ":" + snapshot.SnapshotID
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contracts.RepositorySnapshot{}, databaseError("postgres.PublishIndexSnapshot.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return contracts.RepositorySnapshot{}, err
	}
	if replay, found, err := loadControlReplay[contracts.RepositorySnapshot](
		ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash,
	); err != nil {
		return contracts.RepositorySnapshot{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return contracts.RepositorySnapshot{}, databaseError("postgres.PublishIndexSnapshot.replay", err)
		}
		return replay, nil
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", advisoryLockKey("index_snapshot\x00"+s.tenantID+"\x00"+s.repositoryID)); err != nil {
		return contracts.RepositorySnapshot{}, databaseError("postgres.PublishIndexSnapshot.lock", err)
	}
	stored, storedHash, found, err := loadIndexSnapshot(ctx, tx, s.tenantID, s.repositoryID, snapshot.SnapshotID, true)
	if err != nil {
		return contracts.RepositorySnapshot{}, err
	}
	if found {
		if !bytes.Equal(storedHash, requestHash) {
			return contracts.RepositorySnapshot{}, fault.New(fault.CodeConflict, "postgres.PublishIndexSnapshot", "snapshot identity is bound to different evidence")
		}
		now := postgresTimestamp(s.clock.Now())
		replayID := contracts.StableIndexID(
			"index_replay", snapshot.SnapshotID, metadata.IdempotencyKey,
		)
		if err := s.appendKnowledgeEvent(
			ctx, tx, "audit", replayID, 1, "index_snapshot.replayed", now,
			indexEventPayload(stored, requestIdentity, requestHash), metadata,
		); err != nil {
			return contracts.RepositorySnapshot{}, err
		}
		if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 200, stored); err != nil {
			return contracts.RepositorySnapshot{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return contracts.RepositorySnapshot{}, databaseError("postgres.PublishIndexSnapshot.existing", err)
		}
		return stored, nil
	}
	previousSnapshotID := ""
	var previousSnapshot contracts.RepositorySnapshot
	if previous, _, previousFound, previousErr := loadIndexSnapshot(
		ctx, tx, s.tenantID, s.repositoryID, "", true,
	); previousErr != nil {
		return contracts.RepositorySnapshot{}, previousErr
	} else if previousFound {
		previousSnapshotID = previous.SnapshotID
		previousSnapshot = previous
	}
	artifactHash, _ := decodeContentHash(*snapshot.ArtifactContentHash)
	var artifactKind, artifactStatus string
	var tombstonedAt *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT kind, status, tombstoned_at
		FROM forja.artifacts
		WHERE tenant_id=$1 AND repository_id=$2 AND artifact_id=$3 AND content_sha256=$4
		FOR SHARE`,
		s.tenantID, s.repositoryID, *snapshot.ArtifactID, artifactHash,
	).Scan(&artifactKind, &artifactStatus, &tombstonedAt); errors.Is(err, pgx.ErrNoRows) {
		return contracts.RepositorySnapshot{}, fault.New(fault.CodeInvalidArgument, "postgres.PublishIndexSnapshot", "snapshot artifact is not canonical evidence")
	} else if err != nil {
		return contracts.RepositorySnapshot{}, databaseError("postgres.PublishIndexSnapshot.artifact", err)
	}
	if artifactKind != "index_snapshot" || !slices.Contains([]string{"active", "validated"}, artifactStatus) || tombstonedAt != nil {
		return contracts.RepositorySnapshot{}, fault.New(fault.CodeInvalidArgument, "postgres.PublishIndexSnapshot", "snapshot artifact is not active index evidence")
	}
	now := postgresTimestamp(s.clock.Now())
	if now.Before(snapshot.CreatedAt) || snapshot.ValidatedAt == nil || now.Before(*snapshot.ValidatedAt) {
		return contracts.RepositorySnapshot{}, fault.New(fault.CodeInvalidArgument, "postgres.PublishIndexSnapshot", "snapshot timestamps are in the future")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE forja.index_snapshots
		SET status='superseded', version=version+1, updated_at=$3
		WHERE tenant_id=$1 AND repository_id=$2 AND status='active'`,
		s.tenantID, s.repositoryID, now,
	); err != nil {
		return contracts.RepositorySnapshot{}, databaseError("postgres.PublishIndexSnapshot.supersede", err)
	}
	adaptersJSON, _ := json.Marshal(snapshot.Adapters)
	configurationHash, _ := decodeContentHash(snapshot.ConfigurationHash)
	adapterSetHash, _ := decodeContentHash(snapshot.AdapterSetHash)
	validatedAt := postgresTimestamp(*snapshot.ValidatedAt)
	createdAt := postgresTimestamp(snapshot.CreatedAt)
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.index_snapshots (
			tenant_id, repository_id, snapshot_id, source_commit, source_tree,
			configuration_sha256, adapter_set_sha256, adapters, request_sha256,
			status, version, file_count, symbol_count, relation_count,
			diagnostic_count, artifact_id, artifact_content_sha256, created_by,
			created_at, validated_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'active',1,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		s.tenantID, s.repositoryID, snapshot.SnapshotID, snapshot.SourceCommit,
		snapshot.SourceTree, configurationHash, adapterSetHash, adaptersJSON,
		requestHash, snapshot.Counts.Files, snapshot.Counts.Symbols,
		snapshot.Counts.Relations, snapshot.Counts.Diagnostics, *snapshot.ArtifactID,
		artifactHash, snapshot.CreatedBy, createdAt, validatedAt, now,
	); err != nil {
		return contracts.RepositorySnapshot{}, databaseError("postgres.PublishIndexSnapshot.snapshot", err)
	}
	if err := copyIndexFiles(ctx, tx, s.tenantID, s.repositoryID, publication); err != nil {
		return contracts.RepositorySnapshot{}, err
	}
	if err := copyIndexSymbols(ctx, tx, s.tenantID, s.repositoryID, publication); err != nil {
		return contracts.RepositorySnapshot{}, err
	}
	if err := copyIndexRelations(ctx, tx, s.tenantID, s.repositoryID, publication); err != nil {
		return contracts.RepositorySnapshot{}, err
	}
	if err := validateIndexDeltaAuthority(
		ctx, tx, s.tenantID, s.repositoryID, previousSnapshotID, publication,
	); err != nil {
		return contracts.RepositorySnapshot{}, err
	}
	if err := copyIndexMetadata(ctx, tx, s.tenantID, s.repositoryID, publication); err != nil {
		return contracts.RepositorySnapshot{}, err
	}
	snapshot.Status = "active"
	snapshot.Version = 1
	snapshot.CreatedAt = createdAt
	snapshot.ValidatedAt = &validatedAt
	if previousSnapshotID != "" {
		previousSnapshot.Status = "superseded"
		previousSnapshot.Version++
		if err := s.appendKnowledgeEvent(
			ctx, tx, "index_snapshot", previousSnapshot.SnapshotID,
			previousSnapshot.Version, "index_snapshot.superseded", now,
			map[string]any{
				"snapshot": previousSnapshot, "superseded_by": snapshot.SnapshotID,
			},
			metadata,
		); err != nil {
			return contracts.RepositorySnapshot{}, err
		}
	}
	if err := s.appendKnowledgeEvent(
		ctx, tx, "index_snapshot", snapshot.SnapshotID, 1,
		"index_snapshot.activated", now,
		indexEventPayload(snapshot, requestIdentity, requestHash), metadata,
	); err != nil {
		return contracts.RepositorySnapshot{}, err
	}
	if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 201, snapshot); err != nil {
		return contracts.RepositorySnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.RepositorySnapshot{}, databaseError("postgres.PublishIndexSnapshot.commit", err)
	}
	return snapshot, nil
}

func (s *Store) ValidateIndexPublicationAuthority(
	_ context.Context,
	bundle indexing.IndexBundle,
	metadata runstate.CommandMetadata,
) error {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return err
	}
	if err := indexing.ValidateBundle(bundle); err != nil {
		return fault.Wrap(
			fault.CodeInvalidArgument,
			"postgres.ValidateIndexPublicationAuthority",
			"validate proposed index bundle",
			err,
		)
	}
	snapshot := bundle.Snapshot
	if snapshot.TenantID != "tenant_"+s.tenantID ||
		snapshot.RepositoryID != "repo_"+s.repositoryID ||
		snapshot.Status != "proposed" || snapshot.CreatedBy != metadata.ActorID ||
		snapshot.ArtifactID != nil || snapshot.ArtifactContentHash != nil ||
		snapshot.ValidatedAt != nil {
		return fault.New(
			fault.CodeInvalidArgument,
			"postgres.ValidateIndexPublicationAuthority",
			"snapshot authority or lifecycle is invalid",
		)
	}
	return nil
}

func indexEventPayload(
	snapshot contracts.RepositorySnapshot,
	requestIdentity string,
	requestHash []byte,
) map[string]any {
	return map[string]any{
		"snapshot":              snapshot,
		"request_identity_json": requestIdentity,
		"request_sha256":        hex.EncodeToString(requestHash),
	}
}

func validateIndexDeltaAuthority(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, repositoryID, previousSnapshotID string,
	publication persistence.IndexPublication,
) error {
	expected, err := expectedIndexDeltas(
		ctx, tx, tenantID, repositoryID, previousSnapshotID, publication.Bundle,
	)
	if err != nil {
		return err
	}
	actual := append([]persistence.IndexDelta(nil), publication.Deltas...)
	sortIndexDeltaSemantics(expected)
	sortIndexDeltaSemantics(actual)
	if len(actual) != len(expected) {
		return invalidIndexDeltaAuthority()
	}
	for index := range expected {
		if indexDeltaSemanticKey(actual[index]) != indexDeltaSemanticKey(expected[index]) {
			return invalidIndexDeltaAuthority()
		}
	}
	return nil
}

func expectedIndexDeltas(
	ctx context.Context,
	queryer indexBundleQueryer,
	tenantID, repositoryID, previousSnapshotID string,
	target indexing.IndexBundle,
) ([]persistence.IndexDelta, error) {
	if previousSnapshotID == "" {
		result := make([]persistence.IndexDelta, 0, len(target.Files)+len(target.Symbols)+len(target.Relations))
		for _, file := range target.Files {
			result = append(result, persistence.IndexDelta{ChangeKind: "added", EntityKind: "file", EntityID: file.FileID})
		}
		for _, symbol := range target.Symbols {
			result = append(result, persistence.IndexDelta{ChangeKind: "added", EntityKind: "symbol", EntityID: symbol.SymbolID})
		}
		for _, relation := range target.Relations {
			result = append(result, persistence.IndexDelta{ChangeKind: "added", EntityKind: "relation", EntityID: relation.RelationID})
		}
		return result, nil
	}
	baseline, found, err := loadIndexBundle(
		ctx, queryer, tenantID, repositoryID, previousSnapshotID,
	)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fault.New(
			fault.CodeConflict, "postgres.PublishIndexSnapshot",
			"canonical baseline snapshot disappeared during publication",
		)
	}
	computed, err := indexing.ComputeBundleDeltas(baseline, target)
	if err != nil {
		return nil, fault.Wrap(
			fault.CodeInvalidArgument, "postgres.PublishIndexSnapshot",
			"recompute canonical index deltas", err,
		)
	}
	result := make([]persistence.IndexDelta, len(computed))
	for index, delta := range computed {
		result[index] = persistence.IndexDelta{
			ChangeKind: delta.ChangeKind, EntityKind: delta.EntityKind,
			EntityID: delta.EntityID, PreviousEntityID: delta.PreviousEntityID,
		}
	}
	return result, nil
}

func sortIndexDeltaSemantics(values []persistence.IndexDelta) {
	slices.SortFunc(values, func(left, right persistence.IndexDelta) int {
		return strings.Compare(indexDeltaSemanticKey(left), indexDeltaSemanticKey(right))
	})
}

func indexDeltaSemanticKey(value persistence.IndexDelta) string {
	previous := "<absent>"
	if value.PreviousEntityID != nil {
		previous = "<present>" + *value.PreviousEntityID
	}
	return value.ChangeKind + "\x00" + value.EntityKind + "\x00" + value.EntityID + "\x00" + previous
}

func invalidIndexDeltaAuthority() error {
	return fault.New(
		fault.CodeInvalidArgument, "postgres.PublishIndexSnapshot",
		"index deltas do not match the canonical baseline-to-target transition",
	)
}

func (s *Store) GetActiveIndexSnapshot(ctx context.Context) (contracts.RepositorySnapshot, bool, error) {
	value, _, found, err := loadIndexSnapshot(ctx, s.pool, s.tenantID, s.repositoryID, "", false)
	return value, found, err
}

func (s *Store) validateIndexPublication(
	value persistence.IndexPublication,
	metadata runstate.CommandMetadata,
) ([]byte, string, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return nil, "", err
	}
	if err := indexing.ValidateBundle(value.Bundle); err != nil {
		return nil, "", fault.Wrap(fault.CodeInvalidArgument, "postgres.PublishIndexSnapshot", "validate index bundle", err)
	}
	snapshot := value.Bundle.Snapshot
	if snapshot.TenantID != "tenant_"+s.tenantID || snapshot.RepositoryID != "repo_"+s.repositoryID ||
		snapshot.Status != "active" || snapshot.CreatedBy != metadata.ActorID {
		return nil, "", fault.New(fault.CodeInvalidArgument, "postgres.PublishIndexSnapshot", "snapshot authority or lifecycle is invalid")
	}
	if len(value.AdapterRuns) != len(snapshot.Adapters) {
		return nil, "", fault.New(fault.CodeInvalidArgument, "postgres.PublishIndexSnapshot", "adapter run set is incomplete")
	}
	for index, run := range value.AdapterRuns {
		if run.Adapter != snapshot.Adapters[index] || !slices.Contains([]string{"passed", "reused"}, run.Status) || run.DiagnosticCount < 0 {
			return nil, "", fault.New(fault.CodeInvalidArgument, "postgres.PublishIndexSnapshot", "adapter run evidence is invalid")
		}
	}
	for index, delta := range value.Deltas {
		if delta.Ordinal != index || !slices.Contains([]string{"added", "modified", "deleted", "renamed", "reused"}, delta.ChangeKind) ||
			!slices.Contains([]string{"file", "symbol", "relation"}, delta.EntityKind) || strings.TrimSpace(delta.EntityID) == "" ||
			(slices.Contains([]string{"added", "deleted"}, delta.ChangeKind) && delta.PreviousEntityID != nil) ||
			(slices.Contains([]string{"modified", "renamed", "reused"}, delta.ChangeKind) &&
				(delta.PreviousEntityID == nil || strings.TrimSpace(*delta.PreviousEntityID) == "")) ||
			delta.ChangeKind == "renamed" && delta.EntityKind != "file" {
			return nil, "", fault.New(fault.CodeInvalidArgument, "postgres.PublishIndexSnapshot", "index delta evidence is invalid")
		}
	}
	previousInvalidation := ""
	for _, invalidation := range value.Invalidations {
		key := invalidation.EntityID + "\x00" + invalidation.Reason
		if key <= previousInvalidation || !slices.Contains([]string{"source_changed", "dependency_changed", "adapter_changed", "configuration_changed", "deleted"}, invalidation.Reason) {
			return nil, "", fault.New(fault.CodeInvalidArgument, "postgres.PublishIndexSnapshot", "index invalidations are not canonical")
		}
		if invalidation.SourceHash != nil {
			if _, err := decodeContentHash(*invalidation.SourceHash); err != nil {
				return nil, "", fault.New(fault.CodeInvalidArgument, "postgres.PublishIndexSnapshot", "invalidation source hash is invalid")
			}
		}
		previousInvalidation = key
	}
	requestIdentity := value
	requestIdentity.Bundle.Snapshot.ValidatedAt = nil
	encoded, err := json.Marshal(requestIdentity)
	if err != nil {
		return nil, "", fault.Wrap(fault.CodeInvalidArgument, "postgres.PublishIndexSnapshot", "encode publication", err)
	}
	canonicalRequest := string(encoded)
	return hashKnowledgeCommand(metadata, "index_publication", canonicalRequest), canonicalRequest, nil
}

type indexSnapshotQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadIndexSnapshot(ctx context.Context, queryer indexSnapshotQueryer, tenantID, repositoryID, snapshotID string, forUpdate bool) (contracts.RepositorySnapshot, []byte, bool, error) {
	where := "status='active'"
	arguments := []any{tenantID, repositoryID}
	if snapshotID != "" {
		where = "snapshot_id=$3"
		arguments = append(arguments, snapshotID)
	}
	locking := ""
	if forUpdate {
		locking = " FOR UPDATE"
	}
	row := queryer.QueryRow(ctx, `
		SELECT snapshot_id, source_commit, source_tree, encode(configuration_sha256,'hex'),
		       encode(adapter_set_sha256,'hex'), adapters, request_sha256, status,
		       version, file_count, symbol_count, relation_count, diagnostic_count,
		       artifact_id, encode(artifact_content_sha256,'hex'), created_by,
		       created_at, validated_at
		FROM forja.index_snapshots
		WHERE tenant_id=$1 AND repository_id=$2 AND `+where+locking, arguments...)
	var value contracts.RepositorySnapshot
	var configurationHash, adapterSetHash string
	var adaptersJSON []byte
	var requestHash []byte
	var artifactHash *string
	err := row.Scan(&value.SnapshotID, &value.SourceCommit, &value.SourceTree, &configurationHash,
		&adapterSetHash, &adaptersJSON, &requestHash, &value.Status, &value.Version,
		&value.Counts.Files, &value.Counts.Symbols, &value.Counts.Relations,
		&value.Counts.Diagnostics, &value.ArtifactID, &artifactHash, &value.CreatedBy,
		&value.CreatedAt, &value.ValidatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.RepositorySnapshot{}, nil, false, nil
	}
	if err != nil {
		return contracts.RepositorySnapshot{}, nil, false, databaseError("postgres.loadIndexSnapshot", err)
	}
	value.SchemaVersion = contracts.IndexSchemaVersion
	value.TenantID = "tenant_" + tenantID
	value.RepositoryID = "repo_" + repositoryID
	value.ConfigurationHash = "sha256:" + configurationHash
	value.AdapterSetHash = "sha256:" + adapterSetHash
	if artifactHash != nil {
		hash := "sha256:" + *artifactHash
		value.ArtifactContentHash = &hash
	}
	if err := json.Unmarshal(adaptersJSON, &value.Adapters); err != nil {
		return contracts.RepositorySnapshot{}, nil, false, fault.Wrap(fault.CodeInternal, "postgres.loadIndexSnapshot", "decode adapters", err)
	}
	value.CreatedAt = value.CreatedAt.UTC()
	if value.ValidatedAt != nil {
		validated := value.ValidatedAt.UTC()
		value.ValidatedAt = &validated
	}
	return value, requestHash, true, nil
}

func copyIndexFiles(ctx context.Context, tx pgx.Tx, tenantID, repositoryID string, value persistence.IndexPublication) error {
	rows := make([][]any, 0, len(value.Bundle.Files))
	for _, file := range value.Bundle.Files {
		hash, _ := decodeContentHash(file.SourceHash)
		diagnostics, _ := json.Marshal(file.Diagnostics)
		rows = append(rows, []any{tenantID, repositoryID, value.Bundle.Snapshot.SnapshotID, file.FileID, file.LineageID, file.Path, file.GitBlobID, hash, file.SizeBytes, file.Language, file.Generated, diagnostics})
	}
	return copyIndexRows(ctx, tx, "index_files", []string{"tenant_id", "repository_id", "snapshot_id", "file_id", "lineage_id", "path", "git_blob_id", "source_sha256", "size_bytes", "language", "generated", "diagnostics"}, rows)
}

func copyIndexSymbols(ctx context.Context, tx pgx.Tx, tenantID, repositoryID string, value persistence.IndexPublication) error {
	rows := make([][]any, 0, len(value.Bundle.Symbols))
	for _, symbol := range value.Bundle.Symbols {
		var documentationHash []byte
		if symbol.DocumentationHash != nil {
			documentationHash, _ = decodeContentHash(*symbol.DocumentationHash)
		}
		rows = append(rows, []any{tenantID, repositoryID, value.Bundle.Snapshot.SnapshotID, symbol.SymbolID, symbol.LineageID, symbol.FileID, symbol.Language, symbol.Kind, symbol.Name, symbol.QualifiedName, symbol.Signature, symbol.Declaration.Start.Line, symbol.Declaration.Start.Column, symbol.Declaration.Start.Offset, symbol.Declaration.End.Line, symbol.Declaration.End.Column, symbol.Declaration.End.Offset, symbol.Exported, symbol.Test, symbol.Route, symbol.Schema, documentationHash})
	}
	return copyIndexRows(ctx, tx, "index_symbols", []string{"tenant_id", "repository_id", "snapshot_id", "symbol_id", "lineage_id", "file_id", "language", "kind", "name", "qualified_name", "signature", "start_line", "start_column", "start_offset", "end_line", "end_column", "end_offset", "exported", "is_test", "is_route", "is_schema", "documentation_sha256"}, rows)
}

func copyIndexRelations(ctx context.Context, tx pgx.Tx, tenantID, repositoryID string, value persistence.IndexPublication) error {
	rows := make([][]any, 0, len(value.Bundle.Relations))
	for _, relation := range value.Bundle.Relations {
		evidenceHash, _ := decodeContentHash(relation.EvidenceHash)
		adapter, _ := json.Marshal(relation.Adapter)
		rows = append(rows, []any{tenantID, repositoryID, value.Bundle.Snapshot.SnapshotID, relation.RelationID, relation.SourceEntityID, relation.Kind, relation.Resolution, relation.TargetEntityID, relation.UnresolvedName, relation.EvidenceClass, relation.SourceFileID, relation.Locator.Start.Line, relation.Locator.Start.Column, relation.Locator.Start.Offset, relation.Locator.End.Line, relation.Locator.End.Column, relation.Locator.End.Offset, evidenceHash, adapter})
	}
	return copyIndexRows(ctx, tx, "index_relations", []string{"tenant_id", "repository_id", "snapshot_id", "relation_id", "source_entity_id", "kind", "resolution", "target_entity_id", "unresolved_name", "evidence_class", "source_file_id", "start_line", "start_column", "start_offset", "end_line", "end_column", "end_offset", "evidence_sha256", "adapter"}, rows)
}

func copyIndexMetadata(ctx context.Context, tx pgx.Tx, tenantID, repositoryID string, value persistence.IndexPublication) error {
	snapshotID := value.Bundle.Snapshot.SnapshotID
	runs := make([][]any, 0, len(value.AdapterRuns))
	for _, run := range value.AdapterRuns {
		configurationHash, _ := decodeContentHash(run.Adapter.ConfigurationHash)
		capabilityHash, _ := decodeContentHash(run.Adapter.CapabilityHash)
		runs = append(runs, []any{tenantID, repositoryID, snapshotID, run.Adapter.Name, run.Adapter.Version, configurationHash, capabilityHash, run.Status, run.DiagnosticCount})
	}
	if err := copyIndexRows(ctx, tx, "index_adapter_runs", []string{"tenant_id", "repository_id", "snapshot_id", "adapter_name", "adapter_version", "configuration_sha256", "capability_sha256", "status", "diagnostic_count"}, runs); err != nil {
		return err
	}
	deltas := make([][]any, 0, len(value.Deltas))
	for _, delta := range value.Deltas {
		deltas = append(deltas, []any{tenantID, repositoryID, snapshotID, delta.Ordinal, delta.ChangeKind, delta.EntityKind, delta.EntityID, delta.PreviousEntityID})
	}
	if err := copyIndexRows(ctx, tx, "index_deltas", []string{"tenant_id", "repository_id", "snapshot_id", "ordinal", "change_kind", "entity_kind", "entity_id", "previous_entity_id"}, deltas); err != nil {
		return err
	}
	invalidations := make([][]any, 0, len(value.Invalidations))
	for _, invalidation := range value.Invalidations {
		var sourceHash []byte
		if invalidation.SourceHash != nil {
			sourceHash, _ = decodeContentHash(*invalidation.SourceHash)
		}
		invalidations = append(invalidations, []any{tenantID, repositoryID, snapshotID, invalidation.EntityID, invalidation.Reason, sourceHash})
	}
	return copyIndexRows(ctx, tx, "index_invalidations", []string{"tenant_id", "repository_id", "snapshot_id", "entity_id", "reason", "source_sha256"}, invalidations)
}

func copyIndexRows(ctx context.Context, tx pgx.Tx, table string, columns []string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	count, err := tx.CopyFrom(ctx, pgx.Identifier{"forja", table}, columns, pgx.CopyFromRows(rows))
	if err != nil {
		return databaseError("postgres.copyIndexRows."+table, err)
	}
	if count != int64(len(rows)) {
		return fault.New(fault.CodeInternal, "postgres.copyIndexRows."+table, fmt.Sprintf("copied %d rows, want %d", count, len(rows)))
	}
	return nil
}

var _ persistence.IndexRepository = (*Store)(nil)
