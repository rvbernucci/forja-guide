package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql schema_manifest.json
var migrationFiles embed.FS

type migration struct {
	version  int64
	name     string
	checksum string
	up       string
	down     string
}

type appliedMigration struct {
	version  int64
	name     string
	checksum string
}

type schemaManifest struct {
	Version           string            `json:"version"`
	Tables            map[string]string `json:"tables"`
	ConstraintsSHA256 string            `json:"constraints_sha256"`
	IndexesSHA256     string            `json:"indexes_sha256"`
	TriggersSHA256    string            `json:"triggers_sha256"`
	Trigger           triggerManifest   `json:"trigger"`
}

type triggerManifest struct {
	Relation                string `json:"relation"`
	Name                    string `json:"name"`
	Enabled                 string `json:"enabled"`
	Function                string `json:"function"`
	TriggerType             int    `json:"trigger_type"`
	FunctionLanguage        string `json:"function_language"`
	FunctionSecurityDefiner bool   `json:"function_security_definer"`
	FunctionVolatility      string `json:"function_volatility"`
	FunctionSourceSHA256    string `json:"function_source_sha256"`
	Definition              string `json:"definition"`
}

// Migrate applies every pending migration under a transaction-scoped lock.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(70120402)); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS forja;
		CREATE TABLE IF NOT EXISTS forja.schema_migrations (
			version bigint PRIMARY KEY,
			name text NOT NULL,
			checksum text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
		)`); err != nil {
		return fmt.Errorf("bootstrap migrations: %w", err)
	}
	appliedCount, err := validateMigrationLedger(ctx, tx, migrations)
	if err != nil {
		return err
	}
	for _, item := range migrations[appliedCount:] {
		if _, err := tx.Exec(ctx, item.up); err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", item.version, item.name, err)
		}
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO forja.schema_migrations (version, name, checksum)
			 VALUES ($1, $2, $3)`,
			item.version,
			item.name,
			item.checksum,
		); err != nil {
			return fmt.Errorf("record migration %d: %w", item.version, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

// RollbackLast rolls back the most recently applied known migration.
func RollbackLast(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin migration rollback: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(70120402)); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS forja;
		CREATE TABLE IF NOT EXISTS forja.schema_migrations (
			version bigint PRIMARY KEY,
			name text NOT NULL,
			checksum text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
		)`); err != nil {
		return fmt.Errorf("bootstrap migration rollback: %w", err)
	}
	appliedCount, err := validateMigrationLedger(ctx, tx, migrations)
	if err != nil {
		return err
	}
	if appliedCount == 0 {
		return nil
	}
	selected := migrations[appliedCount-1]
	if _, err := tx.Exec(ctx, selected.down); err != nil {
		return fmt.Errorf("rollback migration %d: %w", selected.version, err)
	}
	if _, err := tx.Exec(
		ctx,
		"DELETE FROM forja.schema_migrations WHERE version=$1",
		selected.version,
	); err != nil {
		return fmt.Errorf("remove migration record %d: %w", selected.version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration rollback: %w", err)
	}
	return nil
}

// VerifySchema confirms exact migration parity and semantic compatibility of
// the canonical schema required by the running binary.
func VerifySchema(
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID string,
	repositoryID string,
) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin schema verification: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	appliedCount, err := validateMigrationLedger(ctx, tx, migrations)
	if err != nil {
		return err
	}
	if appliedCount != len(migrations) {
		return fmt.Errorf(
			"database has %d of %d required migrations",
			appliedCount,
			len(migrations),
		)
	}
	manifest, err := loadSchemaManifest()
	if err != nil {
		return err
	}
	if err := verifyTableSignatures(ctx, tx, manifest.Tables); err != nil {
		return err
	}
	if err := verifySchemaObjectSet(
		ctx,
		tx,
		"constraints",
		`SELECT c.conrelid::regclass::text || ':' || c.conname || ':' || c.contype::text,
		        pg_get_constraintdef(c.oid, true)
		 FROM pg_constraint AS c
		 JOIN pg_namespace AS n ON n.oid=c.connamespace
		 WHERE n.nspname='forja'
		 ORDER BY 1`,
		manifest.ConstraintsSHA256,
	); err != nil {
		return err
	}
	if err := verifySchemaObjectSet(
		ctx,
		tx,
		"indexes",
		`SELECT schemaname || '.' || tablename || ':' || indexname, indexdef
		 FROM pg_indexes
		 WHERE schemaname='forja'
		 ORDER BY 1`,
		manifest.IndexesSHA256,
	); err != nil {
		return err
	}
	if err := verifySchemaObjectSet(
		ctx,
		tx,
		"triggers",
		`SELECT t.tgrelid::regclass::text || ':' || t.tgname,
		        t.tgenabled::text || ':' ||
		        t.tgtype::text || ':' ||
		        l.lanname || ':' ||
		        p.prosecdef::text || ':' ||
		        p.provolatile::text || ':' ||
		        encode(convert_to(p.prosrc, 'UTF8'), 'hex') || ':' ||
		        encode(
		          convert_to(pg_get_triggerdef(t.oid, true), 'UTF8'),
		          'hex'
		        )
		 FROM pg_trigger AS t
		 JOIN pg_class AS c ON c.oid=t.tgrelid
		 JOIN pg_namespace AS n ON n.oid=c.relnamespace
		 JOIN pg_proc AS p ON p.oid=t.tgfoid
		 JOIN pg_language AS l ON l.oid=p.prolang
		 WHERE n.nspname='forja' AND NOT t.tgisinternal
		 ORDER BY 1`,
		manifest.TriggersSHA256,
	); err != nil {
		return err
	}
	if err := verifyAppendOnlyTrigger(ctx, tx, manifest.Trigger); err != nil {
		return err
	}
	var authority bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1
		  FROM forja.tenants AS t
		  JOIN forja.repositories AS r ON r.tenant_id=t.tenant_id
		  WHERE t.tenant_id=$1 AND r.repository_id=$2
		)`,
		tenantID,
		repositoryID,
	).Scan(&authority)
	if err != nil {
		return fmt.Errorf("inspect bootstrap authority: %w", err)
	}
	if !authority {
		return fmt.Errorf("bound tenant/repository authority is missing")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit schema verification: %w", err)
	}
	return nil
}

func loadSchemaManifest() (schemaManifest, error) {
	data, err := migrationFiles.ReadFile("schema_manifest.json")
	if err != nil {
		return schemaManifest{}, fmt.Errorf("read schema manifest: %w", err)
	}
	var manifest schemaManifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return schemaManifest{}, fmt.Errorf("decode schema manifest: %w", err)
	}
	if manifest.Version != "1.0" ||
		len(manifest.Tables) == 0 ||
		manifest.ConstraintsSHA256 == "" ||
		manifest.IndexesSHA256 == "" ||
		manifest.TriggersSHA256 == "" ||
		manifest.Trigger.Relation == "" {
		return schemaManifest{}, fmt.Errorf("schema manifest is incomplete")
	}
	return manifest, nil
}

func verifyTableSignatures(
	ctx context.Context,
	tx pgx.Tx,
	expected map[string]string,
) error {
	rows, err := tx.Query(ctx, `
		SELECT table_name,
		       string_agg(
		         column_name || ':' || udt_name || ':' || is_nullable || ':' ||
		         is_identity || ':' || COALESCE(identity_generation, '') || ':' ||
		         COALESCE(column_default, ''),
		         ',' ORDER BY ordinal_position
		       )
		FROM information_schema.columns
		WHERE table_schema='forja'
		GROUP BY table_name
		ORDER BY table_name`)
	if err != nil {
		return fmt.Errorf("inspect canonical columns: %w", err)
	}
	defer rows.Close()
	actual := make(map[string]string)
	for rows.Next() {
		var table, signature string
		if err := rows.Scan(&table, &signature); err != nil {
			return fmt.Errorf("scan canonical columns: %w", err)
		}
		actual[table] = signature
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate canonical columns: %w", err)
	}
	if len(actual) != len(expected) {
		return fmt.Errorf(
			"canonical table count mismatch: got %d, want %d",
			len(actual),
			len(expected),
		)
	}
	for table, signature := range expected {
		if actual[table] != signature {
			return fmt.Errorf("canonical column signature mismatch for forja.%s", table)
		}
	}
	return nil
}

func verifySchemaObjectSet(
	ctx context.Context,
	tx pgx.Tx,
	kind string,
	query string,
	expectedSHA256 string,
) error {
	rows, err := tx.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("inspect canonical %s: %w", kind, err)
	}
	defer rows.Close()
	digest := sha256.New()
	for rows.Next() {
		var signature, definition string
		if err := rows.Scan(&signature, &definition); err != nil {
			return fmt.Errorf("scan canonical %s: %w", kind, err)
		}
		_, _ = fmt.Fprintf(digest, "%s\t%s\n", signature, definition)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate canonical %s: %w", kind, err)
	}
	actualSHA256 := fmt.Sprintf("%x", digest.Sum(nil))
	if actualSHA256 != expectedSHA256 {
		return fmt.Errorf("canonical %s differ from the release manifest", kind)
	}
	return nil
}

func verifyAppendOnlyTrigger(
	ctx context.Context,
	tx pgx.Tx,
	expected triggerManifest,
) error {
	var relation, name, enabled, function string
	var triggerType int
	var language string
	var securityDefiner bool
	var volatility, functionSource, definition string
	err := tx.QueryRow(ctx, `
		SELECT t.tgrelid::regclass::text,
		       t.tgname,
		       t.tgenabled::text,
		       p.pronamespace::regnamespace::text || '.' || p.proname,
		       t.tgtype,
		       l.lanname,
		       p.prosecdef,
		       p.provolatile::text,
		       p.prosrc,
		       pg_get_triggerdef(t.oid, true)
		FROM pg_trigger AS t
		JOIN pg_proc AS p ON p.oid=t.tgfoid
		JOIN pg_language AS l ON l.oid=p.prolang
		WHERE t.tgrelid=to_regclass($1)
		  AND t.tgname=$2
		  AND NOT t.tgisinternal`,
		expected.Relation,
		expected.Name,
	).Scan(
		&relation,
		&name,
		&enabled,
		&function,
		&triggerType,
		&language,
		&securityDefiner,
		&volatility,
		&functionSource,
		&definition,
	)
	if err != nil {
		return fmt.Errorf("inspect append-only trigger: %w", err)
	}
	if relation != expected.Relation ||
		name != expected.Name ||
		enabled != expected.Enabled ||
		function != expected.Function ||
		triggerType != expected.TriggerType ||
		language != expected.FunctionLanguage ||
		securityDefiner != expected.FunctionSecurityDefiner ||
		volatility != expected.FunctionVolatility ||
		definition != expected.Definition ||
		fmt.Sprintf(
			"%x",
			sha256.Sum256([]byte(functionSource)),
		) != expected.FunctionSourceSHA256 {
		return fmt.Errorf("append-only trigger signature or enabled state is invalid")
	}
	return nil
}

type migrationQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func validateMigrationLedger(
	ctx context.Context,
	queryer migrationQueryer,
	known []migration,
) (int, error) {
	rows, err := queryer.Query(ctx, `
		SELECT version, name, checksum
		FROM forja.schema_migrations
		ORDER BY version`)
	if err != nil {
		return 0, fmt.Errorf("read migration ledger: %w", err)
	}
	defer rows.Close()
	applied := make([]appliedMigration, 0, len(known))
	for rows.Next() {
		var item appliedMigration
		if err := rows.Scan(&item.version, &item.name, &item.checksum); err != nil {
			return 0, fmt.Errorf("scan migration ledger: %w", err)
		}
		applied = append(applied, item)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate migration ledger: %w", err)
	}
	if len(applied) > len(known) {
		return 0, fmt.Errorf(
			"database contains %d migrations but this binary knows %d",
			len(applied),
			len(known),
		)
	}
	for index, item := range applied {
		expected := known[index]
		if item.version != expected.version {
			return 0, fmt.Errorf(
				"migration history is not a known prefix at position %d: got %d, want %d",
				index+1,
				item.version,
				expected.version,
			)
		}
		if item.name != expected.name {
			return 0, fmt.Errorf(
				"migration %d name mismatch: got %q, want %q",
				item.version,
				item.name,
				expected.name,
			)
		}
		if item.checksum != expected.checksum {
			return 0, fmt.Errorf(
				"migration %d checksum mismatch: applied history was modified",
				item.version,
			)
		}
	}
	return len(applied), nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	byVersion := make(map[int64]*migration)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		parts := strings.Split(entry.Name(), "_")
		if len(parts) < 2 {
			return nil, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		version, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse migration version %q: %w", entry.Name(), err)
		}
		item := byVersion[version]
		if item == nil {
			item = &migration{version: version}
			byVersion[version] = item
		}
		data, err := migrationFiles.ReadFile(path.Join("migrations", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		switch {
		case strings.HasSuffix(entry.Name(), ".up.sql"):
			item.up = string(data)
			item.name = strings.TrimSuffix(strings.Join(parts[1:], "_"), ".up.sql")
		case strings.HasSuffix(entry.Name(), ".down.sql"):
			item.down = string(data)
		default:
			return nil, fmt.Errorf("migration %q must end in .up.sql or .down.sql", entry.Name())
		}
	}
	result := make([]migration, 0, len(byVersion))
	for _, item := range byVersion {
		if item.up == "" || item.down == "" {
			return nil, fmt.Errorf("migration %d is missing an up or down file", item.version)
		}
		digest := sha256.Sum256([]byte(item.up + "\x00" + item.down))
		item.checksum = fmt.Sprintf("%x", digest)
		result = append(result, *item)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].version < result[right].version
	})
	return result, nil
}
