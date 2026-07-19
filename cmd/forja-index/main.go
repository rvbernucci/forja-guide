package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path"
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
	tenantID := os.Getenv("FORJA_TENANT_ID")
	repositoryID := os.Getenv("FORJA_REPOSITORY_ID")
	actorID := os.Getenv("FORJA_INDEX_ACTOR_ID")
	if databaseURL == "" || bucket == "" || region == "" ||
		tenantID == "" || repositoryID == "" || actorID == "" {
		return fmt.Errorf(
			"FORJA_DATABASE_URL, FORJA_S3_BUCKET, FORJA_S3_REGION, FORJA_TENANT_ID, " +
				"FORJA_REPOSITORY_ID, and FORJA_INDEX_ACTOR_ID are required",
		)
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
	createdAt := tree.CommittedAt.UTC().Truncate(time.Microsecond)
	if createdAt.IsZero() {
		return fmt.Errorf("committed Git timestamp is required")
	}
	snapshot, err := indexing.NewProposedSnapshot(
		"tenant_"+tenantID, "repo_"+repositoryID,
		tree, configurationHash, descriptors, actorID, createdAt,
	)
	if err != nil {
		return err
	}
	pool, err := postgres.Open(ctx, databaseURL, 4)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := postgres.Migrate(ctx, pool); err != nil {
		return err
	}
	repository, err := postgres.NewStore(pool, nil, tenantID, repositoryID)
	if err != nil {
		return err
	}
	baseline, hasBaseline, err := repository.GetActiveIndexBundle(ctx)
	if err != nil {
		return err
	}
	if hasBaseline && baseline.Snapshot.SnapshotID == snapshot.SnapshotID {
		_, err = fmt.Fprintf(os.Stdout, "%s\n", baseline.Snapshot.SnapshotID)
		return err
	}
	var changeSet indexing.GitChangeSet
	var invalidationPlan indexing.InvalidationPlan
	if hasBaseline {
		changeSet, err = source.ChangeSet(ctx, baseline.Snapshot.SourceCommit, snapshot.SourceCommit)
		if err != nil {
			return err
		}
		invalidationPlan, err = indexing.PlanInvalidation(
			baseline, snapshot, documents, changeSet,
		)
		if err != nil {
			return err
		}
	}
	reusedAdapters := reusableAdapterDescriptors(
		adapters, baseline, invalidationPlan, changeSet, documents, hasBaseline,
	)
	reusedAdapterKeys := make(map[string]struct{}, len(reusedAdapters))
	for _, descriptor := range reusedAdapters {
		reusedAdapterKeys[adapterDescriptorKey(descriptor)] = struct{}{}
	}
	results := make([]indexing.RawAdapterResult, len(adapters))
	adapterRuns := make([]persistence.IndexAdapterRun, len(adapters))
	for index, adapter := range adapters {
		if _, reused := reusedAdapterKeys[adapterDescriptorKey(adapter.Descriptor())]; reused {
			results[index] = indexing.RawAdapterResult{
				Descriptor: adapter.Descriptor(), Symbols: []indexing.RawSymbol{},
				Relations: []indexing.RawRelation{}, Diagnostics: []indexing.RawDiagnostic{},
			}
			adapterRuns[index] = persistence.IndexAdapterRun{
				Adapter: adapter.Descriptor(), Status: "reused",
				DiagnosticCount: adapterDiagnosticCount(baseline, adapter),
			}
			continue
		}
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
	bundle, err = indexing.RebindReusableAdapters(bundle, baseline, reusedAdapters)
	if err != nil {
		return err
	}
	deltas := initialDeltas(bundle)
	invalidations := []persistence.IndexInvalidation{}
	if hasBaseline {
		computed, err := indexing.ComputeBundleDeltasWithChanges(baseline, bundle, changeSet)
		if err != nil {
			return err
		}
		deltas = persistenceDeltas(computed)
		invalidations = persistenceInvalidations(invalidationPlan.Invalidations)
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
		IdempotencyKey: *idempotencyKey, ActorType: "system", ActorID: actorID,
		CorrelationID: "index:" + snapshot.SnapshotID,
	}
	published, err := service.Publish(ctx, indexservice.PublishCommand{
		Bundle: bundle, AdapterRuns: adapterRuns, Deltas: deltas,
		Invalidations: invalidations, Metadata: metadata,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(os.Stdout, "%s\n", published.SnapshotID)
	return err
}

func persistenceDeltas(values []indexing.EntityDelta) []persistence.IndexDelta {
	result := make([]persistence.IndexDelta, len(values))
	for index, value := range values {
		result[index] = persistence.IndexDelta{
			Ordinal: value.Ordinal, ChangeKind: value.ChangeKind,
			EntityKind: value.EntityKind, EntityID: value.EntityID,
			PreviousEntityID: value.PreviousEntityID,
		}
	}
	return result
}

func persistenceInvalidations(values []indexing.EntityInvalidation) []persistence.IndexInvalidation {
	result := make([]persistence.IndexInvalidation, len(values))
	for index, value := range values {
		var sourceHash *string
		if value.SourceHash != "" {
			sourceHash = &value.SourceHash
		}
		result[index] = persistence.IndexInvalidation{
			EntityID: value.EntityID, Reason: value.Reason, SourceHash: sourceHash,
		}
	}
	return result
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

func reusableAdapterDescriptors(
	adapters []indexing.Adapter,
	baseline indexing.IndexBundle,
	plan indexing.InvalidationPlan,
	changes indexing.GitChangeSet,
	documents []indexing.SourceDocument,
	hasBaseline bool,
) []contracts.AdapterDescriptor {
	if !hasBaseline || plan.FullReindex {
		return []contracts.AdapterDescriptor{}
	}
	reusablePaths := make(map[string]struct{}, len(plan.ReusableFiles))
	for _, file := range plan.ReusableFiles {
		reusablePaths[file.Path] = struct{}{}
	}
	baselineAdapters := make(map[string]struct{}, len(baseline.Snapshot.Adapters))
	for _, descriptor := range baseline.Snapshot.Adapters {
		baselineAdapters[adapterDescriptorKey(descriptor)] = struct{}{}
	}
	result := make([]contracts.AdapterDescriptor, 0, len(adapters))
	for _, adapter := range adapters {
		descriptor := adapter.Descriptor()
		if _, exists := baselineAdapters[adapterDescriptorKey(descriptor)]; !exists {
			continue
		}
		if adapterConfigurationChanged(adapter, changes) {
			continue
		}
		languages := make(map[string]struct{}, len(adapter.Languages()))
		for _, language := range adapter.Languages() {
			languages[language] = struct{}{}
		}
		targetPaths := make(map[string]struct{})
		allReusable := true
		for _, document := range documents {
			if _, owned := languages[document.Language]; !owned {
				continue
			}
			targetPaths[document.Path] = struct{}{}
			if _, reusable := reusablePaths[document.Path]; !reusable {
				allReusable = false
			}
		}
		baselineCount := 0
		for _, file := range baseline.Files {
			if _, owned := languages[file.Language]; owned {
				baselineCount++
				if _, stillPresent := targetPaths[file.Path]; !stillPresent {
					allReusable = false
				}
			}
		}
		if allReusable && len(targetPaths) > 0 && baselineCount == len(targetPaths) {
			result = append(result, descriptor)
		}
	}
	return result
}

func adapterConfigurationChanged(adapter indexing.Adapter, changes indexing.GitChangeSet) bool {
	for _, change := range changes.Changes {
		if adapterConfigurationPath(adapter.Descriptor().Name, change.Path) {
			return true
		}
		if change.FromPath != nil && adapterConfigurationPath(adapter.Descriptor().Name, *change.FromPath) {
			return true
		}
	}
	return false
}

func adapterConfigurationPath(adapterName, repositoryPath string) bool {
	lowerPath := strings.ToLower(repositoryPath)
	base := path.Base(lowerPath)
	switch adapterName {
	case "go":
		return base == "go.mod" || base == "go.sum" || base == "go.work" ||
			base == "go.work.sum" || strings.HasSuffix(lowerPath, "/vendor/modules.txt") ||
			lowerPath == "vendor/modules.txt"
	case "typescript":
		return base == "package.json" || base == "package-lock.json" ||
			base == "pnpm-lock.yaml" || base == "yarn.lock" || base == "bun.lock" ||
			base == "bun.lockb" || strings.HasPrefix(base, "tsconfig") && strings.HasSuffix(base, ".json") ||
			strings.HasPrefix(base, "jsconfig") && strings.HasSuffix(base, ".json")
	case "python":
		return base == "pyproject.toml" || base == "setup.py" || base == "setup.cfg" ||
			base == "poetry.lock" || base == "uv.lock" || base == "pipfile" ||
			base == "pipfile.lock" || strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt")
	default:
		return true
	}
}

func adapterDiagnosticCount(bundle indexing.IndexBundle, adapter indexing.Adapter) int {
	languages := make(map[string]struct{}, len(adapter.Languages()))
	for _, language := range adapter.Languages() {
		languages[language] = struct{}{}
	}
	count := 0
	for _, file := range bundle.Files {
		if _, owned := languages[file.Language]; !owned {
			continue
		}
		for _, diagnostic := range file.Diagnostics {
			count += diagnostic.Count
		}
	}
	return count
}

func adapterDescriptorKey(value contracts.AdapterDescriptor) string {
	return value.Name + "\x00" + value.Version + "\x00" + value.ConfigurationHash + "\x00" + value.CapabilityHash
}
