package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/indexing"
	"github.com/rvbernucci/forja-guide/internal/indexservice"
	"github.com/rvbernucci/forja-guide/internal/knowledge"
	"github.com/rvbernucci/forja-guide/internal/objectstore"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/postgres"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "forja-index: %v\n", err)
		os.Exit(1)
	}
}

func run(parent context.Context, arguments []string) error {
	flags := flag.NewFlagSet("forja-index", flag.ContinueOnError)
	repositoryPath := flags.String("repository", ".", "path to the Git repository")
	revision := flags.String("revision", "HEAD", "committed Git revision to index")
	toolRoot := flags.String("tool-root", ".", "root containing adapters and node_modules")
	createdBy := flags.String("created-by", "forja-index", "canonical actor identity")
	idempotencyKey := flags.String("idempotency-key", "", "stable retry key")
	timeout := flags.Duration("timeout", 10*time.Minute, "complete indexing deadline")
	pythonVersion := flags.String("python-version", "3.14", "declared Python AST syntax boundary")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *idempotencyKey == "" || *timeout <= 0 {
		return fmt.Errorf("--idempotency-key and a positive --timeout are required")
	}
	databaseURL := os.Getenv("FORJA_DATABASE_URL")
	bucket := os.Getenv("FORJA_S3_BUCKET")
	region := os.Getenv("FORJA_S3_REGION")
	if databaseURL == "" || bucket == "" || region == "" {
		return fmt.Errorf("FORJA_DATABASE_URL, FORJA_S3_BUCKET, and FORJA_S3_REGION are required")
	}
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	source, err := indexing.NewGitSource(indexing.ExecGitRunner{RepositoryPath: *repositoryPath})
	if err != nil {
		return err
	}
	tree, err := source.InspectCommit(ctx, *revision)
	if err != nil {
		return err
	}
	documents, err := indexing.LoadDocuments(ctx, source, tree)
	if err != nil {
		return err
	}
	materialized, err := os.MkdirTemp("", "forja-index-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(materialized)
	if err := indexing.MaterializeDocuments(materialized, documents); err != nil {
		return err
	}
	adapters := selectedAdapters(*toolRoot, *pythonVersion, documents)
	if len(adapters) == 0 {
		return fmt.Errorf("repository has no supported Go, TypeScript/JavaScript, or Python source")
	}
	descriptors := make([]contracts.AdapterDescriptor, len(adapters))
	for index, adapter := range adapters {
		descriptors[index] = adapter.Descriptor()
	}
	configurationHash := indexing.ConfigurationHash(
		"forja-index-v1", "python-syntax="+*pythonVersion,
	)
	now := time.Now().UTC().Truncate(time.Microsecond)
	snapshot, err := indexing.NewProposedSnapshot(
		"tenant_"+postgres.DefaultTenantID, "repo_"+postgres.DefaultRepositoryID,
		tree, configurationHash, descriptors, *createdBy, now,
	)
	if err != nil {
		return err
	}
	results := make([]indexing.RawAdapterResult, len(adapters))
	adapterRuns := make([]persistence.IndexAdapterRun, len(adapters))
	for index, adapter := range adapters {
		result, extractErr := adapter.Extract(ctx, materialized, documents)
		if extractErr != nil {
			return fmt.Errorf("extract with %s: %w", adapter.Descriptor().Name, extractErr)
		}
		results[index] = result
		adapterRuns[index] = persistence.IndexAdapterRun{
			Adapter: adapter.Descriptor(), Status: "passed", DiagnosticCount: len(result.Diagnostics),
		}
	}
	bundle, err := indexing.NormalizeResults(snapshot, documents, results)
	if err != nil {
		return err
	}
	deltas := initialDeltas(bundle)
	pool, err := postgres.Open(ctx, databaseURL, 4)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := postgres.Migrate(ctx, pool); err != nil {
		return err
	}
	repository, err := postgres.NewStore(pool, nil, postgres.DefaultTenantID, postgres.DefaultRepositoryID)
	if err != nil {
		return err
	}
	pathStyle, err := strconv.ParseBool(defaultString(os.Getenv("FORJA_S3_PATH_STYLE"), "false"))
	if err != nil {
		return fmt.Errorf("FORJA_S3_PATH_STYLE: %w", err)
	}
	bodies, err := objectstore.New(ctx, objectstore.Config{
		Bucket: bucket, Region: region, BaseEndpoint: os.Getenv("FORJA_S3_ENDPOINT"), UsePathStyle: pathStyle,
	})
	if err != nil {
		return err
	}
	artifacts, err := knowledge.NewService(repository, bodies)
	if err != nil {
		return err
	}
	service, err := indexservice.New(artifacts, repository, nil)
	if err != nil {
		return err
	}
	metadata := runstate.CommandMetadata{
		IdempotencyKey: *idempotencyKey, ActorType: "system", ActorID: *createdBy,
		CorrelationID: "index:" + snapshot.SnapshotID,
	}
	published, err := service.Publish(ctx, indexservice.PublishCommand{
		Bundle: bundle, AdapterRuns: adapterRuns, Deltas: deltas,
		Invalidations: []persistence.IndexInvalidation{}, Metadata: metadata,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(os.Stdout, "%s\n", published.SnapshotID)
	return err
}

func selectedAdapters(toolRoot, pythonVersion string, documents []indexing.SourceDocument) []indexing.Adapter {
	languages := make(map[string]bool)
	for _, document := range documents {
		languages[document.Language] = true
	}
	adapters := make([]indexing.Adapter, 0, 3)
	if languages["go"] {
		adapters = append(adapters, indexing.NewGoAdapter())
	}
	if languages["python"] {
		adapters = append(adapters, indexing.NewPythonAdapter(toolRoot, pythonVersion))
	}
	if languages["typescript"] || languages["javascript"] {
		adapters = append(adapters, indexing.NewTypeScriptAdapter(toolRoot))
	}
	sort.Slice(adapters, func(i, j int) bool {
		left, right := adapters[i].Descriptor(), adapters[j].Descriptor()
		return left.Name+"\x00"+left.Version < right.Name+"\x00"+right.Version
	})
	return adapters
}

func initialDeltas(bundle indexing.IndexBundle) []persistence.IndexDelta {
	type entity struct{ kind, id string }
	entities := make([]entity, 0, len(bundle.Files)+len(bundle.Symbols)+len(bundle.Relations))
	for _, file := range bundle.Files {
		entities = append(entities, entity{"file", file.FileID})
	}
	for _, symbol := range bundle.Symbols {
		entities = append(entities, entity{"symbol", symbol.SymbolID})
	}
	for _, relation := range bundle.Relations {
		entities = append(entities, entity{"relation", relation.RelationID})
	}
	sort.Slice(entities, func(i, j int) bool {
		return entities[i].kind+"\x00"+entities[i].id < entities[j].kind+"\x00"+entities[j].id
	})
	result := make([]persistence.IndexDelta, len(entities))
	for index, value := range entities {
		result[index] = persistence.IndexDelta{Ordinal: index, ChangeKind: "added", EntityKind: value.kind, EntityID: value.id}
	}
	return result
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
