// Command forja-retrieval runs one bounded governed-retrieval operation.
//
// It never accepts credentials as flags. PostgreSQL, Qdrant, and AWS
// credentials are resolved only from their approved environment boundaries.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/objectstore"
	"github.com/rvbernucci/forja-guide/internal/postgres"
	"github.com/rvbernucci/forja-guide/internal/retrieval"
)

const (
	maximumQueryFileBytes        = 64 << 10
	maximumEvaluationPlanBytes   = 16 << 20
	maximumOperationTime         = 30 * time.Second
	defaultOperationTime         = 20 * time.Second
	maximumEvaluationCaptureTime = 10 * time.Minute
	defaultEvaluationCaptureTime = 5 * time.Minute
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.LookupEnv); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "forja-retrieval: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, stdout, stderr io.Writer, lookup func(string) (string, bool)) error {
	if len(arguments) == 0 {
		return fmt.Errorf("expected project-once, query, capture, or preflight subcommand")
	}
	switch arguments[0] {
	case "project-once":
		return projectOnce(ctx, arguments[1:], stdout, stderr, lookup)
	case "query":
		return queryOnce(ctx, arguments[1:], stdout, stderr, lookup)
	case "capture":
		return captureRequiredRankings(ctx, arguments[1:], stdout, stderr, lookup)
	case "preflight":
		return preflightRuntime(ctx, arguments[1:], stdout, stderr, lookup)
	default:
		return fmt.Errorf("unsupported subcommand %q", arguments[0])
	}
}

// preflightRuntime verifies the bounded dependency contract required before an
// operator starts projection or private evaluation. It makes one synthetic
// embedding call but never prints credentials, identity, text, vectors, hosts,
// collection names, or provider responses.
func preflightRuntime(parent context.Context, arguments []string, _ io.Writer, stderr io.Writer, lookup func(string) (string, bool)) error {
	flags := flag.NewFlagSet("forja-retrieval preflight", flag.ContinueOnError)
	flags.SetOutput(stderr)
	output := flags.String("output", "", "private redacted preflight receipt JSON path")
	timeout := flags.Duration("timeout", defaultOperationTime, "whole-operation deadline (maximum 30s)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || !safeOutputPath(*output) || !validOperationTimeout(*timeout) {
		return fmt.Errorf("private output and bounded timeout are required")
	}
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	runtime, closeRuntime, err := openRuntime(ctx, lookup)
	if err != nil {
		return err
	}
	defer closeRuntime()
	collectionClient, ok := runtime.qdrant.(retrieval.QdrantCollectionClient)
	if !ok {
		return errors.New("Qdrant runtime cannot inspect the configured collection")
	}
	physicalCollection, err := preflightCollectionTarget(ctx, runtime)
	if err != nil {
		return err
	}
	descriptor := runtime.embedder.Descriptor()
	plan, err := retrieval.BuildQdrantCollectionPlan(physicalCollection, descriptor.Dimensions, runtime.generation)
	if err != nil {
		return fmt.Errorf("build configured Qdrant collection contract: %w", err)
	}
	info, err := collectionClient.GetCollectionInfo(ctx, physicalCollection)
	if err != nil {
		return fmt.Errorf("inspect configured Qdrant collection: %w", err)
	}
	if err := retrieval.VerifyQdrantCollection(info, plan); err != nil {
		return fmt.Errorf("verify configured Qdrant collection: %w", err)
	}
	vector, err := runtime.embedder.Embed(ctx, "forja retrieval preflight")
	if err != nil {
		return fmt.Errorf("verify embedding capability: %w", err)
	}
	if len(vector) != descriptor.Dimensions {
		return errors.New("preflight vector dimensions are invalid")
	}
	receipt := retrievalPreflightReceipt{
		SchemaVersion: contracts.RetrievalSchemaVersion,
		Generation:    runtime.generation,
		PostgresReady: true, QdrantVerified: true,
		EmbeddingProvider:   runtime.embeddingProvider,
		EmbeddingDimensions: len(vector),
	}
	if runtime.embeddingProvider == "bedrock" {
		receipt.BedrockDimensions = len(vector)
	}
	return writePreflightReceipt(*output, receipt)
}

// preflightCollectionTarget resolves an optional serving alias to the physical
// collection whose immutable contract is inspected. An absent alias is valid
// when runtime configuration names a physical collection directly; an alias
// inspection failure is never interpreted as absence.
func preflightCollectionTarget(ctx context.Context, runtime runtime) (string, error) {
	inspector, ok := runtime.qdrant.(retrieval.QdrantAliasInspector)
	if !ok {
		return "", errors.New("Qdrant runtime cannot inspect collection aliases")
	}
	return preflightCollectionName(ctx, inspector, runtime.collection)
}

func preflightCollectionName(ctx context.Context, inspector retrieval.QdrantAliasInspector, configuredCollection string) (string, error) {
	observation, err := retrieval.ObserveQdrantAlias(ctx, inspector, configuredCollection)
	if err != nil {
		return "", fmt.Errorf("observe configured Qdrant alias: %w", err)
	}
	if observation.Exists {
		return observation.CollectionName, nil
	}
	return configuredCollection, nil
}

// captureRequiredRankings runs the fixed four baseline policies against a
// private query plan. The plan cannot contain labels, while the separately
// access-controlled corpus cannot be read by this command.
func captureRequiredRankings(parent context.Context, arguments []string, _ io.Writer, stderr io.Writer, lookup func(string) (string, bool)) error {
	flags := flag.NewFlagSet("forja-retrieval capture", flag.ContinueOnError)
	flags.SetOutput(stderr)
	planPath := flags.String("plan", "", "private label-free evaluation query plan JSON path")
	output := flags.String("output", "", "private four-baseline comparison JSON path")
	timeout := flags.Duration("timeout", defaultEvaluationCaptureTime, "whole-capture deadline (maximum 10m)")
	queryTimeout := flags.Duration("query-timeout", defaultOperationTime, "per-query deadline (maximum 30s)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || !safeInputPath(*planPath) || !safeOutputPath(*output) ||
		!validEvaluationCaptureTimeout(*timeout) || !validOperationTimeout(*queryTimeout) {
		return fmt.Errorf("private plan, output, bounded capture timeout, and bounded query timeout are required")
	}
	plan, err := readEvaluationCapturePlan(*planPath)
	if err != nil {
		return err
	}
	runtime, closeRuntime, err := openRuntime(parent, lookup)
	if err != nil {
		return err
	}
	defer closeRuntime()
	cases, policies, err := normalizeEvaluationCapturePlan(runtime, plan)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	service := retrieval.QueryService{
		Client: runtime.qdrant, CollectionName: runtime.collection,
		Embedder: runtime.embedder, Sparse: retrieval.HashingSparseEncoder{},
		Resolver: runtime.store, Freshness: runtime.store, QueryTimeout: *queryTimeout,
	}
	variants, err := retrieval.CaptureRequiredRankings(ctx, service, cases, policies)
	if err != nil {
		return fmt.Errorf("capture governed retrieval baselines: %w", err)
	}
	return writeEvaluationComparison(*output, evaluationComparisonCapture{
		SchemaVersion: contracts.RetrievalSchemaVersion, CorpusID: plan.CorpusID, Variants: variants,
	})
}

func projectOnce(parent context.Context, arguments []string, _ io.Writer, stderr io.Writer, lookup func(string) (string, bool)) error {
	flags := flag.NewFlagSet("forja-retrieval project-once", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workerID := flags.String("worker-id", "", "stable, non-secret projector worker identity")
	batchSize := flags.Int("batch-size", 25, "maximum claimed deliveries (1-1000)")
	timeout := flags.Duration("timeout", defaultOperationTime, "whole-operation deadline (maximum 30s)")
	output := flags.String("output", "", "private projection receipt JSON path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*workerID) == "" || *batchSize < 1 || *batchSize > 1000 || !validOperationTimeout(*timeout) || !safeOutputPath(*output) {
		return fmt.Errorf("worker ID, bounded batch size, bounded timeout, and an output file are required")
	}
	runtime, closeRuntime, err := openRuntime(parent, lookup)
	if err != nil {
		return err
	}
	defer closeRuntime()
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	writer := retrieval.QdrantPointWriter{Client: runtime.qdrant, CollectionName: runtime.collection}
	worker := retrieval.ProjectionWorker{
		Deliveries: runtime.store, Source: runtime.store, Decisions: runtime.store, Incidents: runtime.store, Memories: runtime.store,
		MemoryBodies: runtime.memoryBodies, Recorder: runtime.store,
		Writer: writer, Deleter: writer, Embedder: runtime.embedder, Sparse: retrieval.HashingSparseEncoder{},
		WorkerID: *workerID, Generation: runtime.generation,
		DecisionTenantID: runtime.tenantID, DecisionRepositoryID: runtime.repositoryID, BatchSize: *batchSize,
		ClaimTTL: *timeout, MaxAttempts: 5, RetryDelay: time.Second,
		DeliveryTimeout: min(*timeout, 15*time.Second),
	}
	run, err := worker.ProcessOnce(ctx)
	if err != nil {
		return fmt.Errorf("project governed retrieval: %w", err)
	}
	return writePrivateJSON(*output, projectionReceipt{
		SchemaVersion: contracts.RetrievalSchemaVersion, Generation: runtime.generation,
		Claimed: run.Claimed, Published: run.Published, Skipped: run.Skipped, Retried: run.Retried, Dead: run.Dead,
	})
}

func queryOnce(parent context.Context, arguments []string, _ io.Writer, stderr io.Writer, lookup func(string) (string, bool)) error {
	flags := flag.NewFlagSet("forja-retrieval query", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "", "governed retrieval query JSON path")
	output := flags.String("output", "", "private retrieval result JSON path")
	timeout := flags.Duration("timeout", defaultOperationTime, "whole-operation deadline (maximum 30s)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || !safeInputPath(*input) || !safeOutputPath(*output) || !validOperationTimeout(*timeout) {
		return fmt.Errorf("bounded input, output, and timeout are required")
	}
	query, err := readQuery(*input)
	if err != nil {
		return err
	}
	runtime, closeRuntime, err := openRuntime(parent, lookup)
	if err != nil {
		return err
	}
	defer closeRuntime()
	if query.TenantID != runtime.tenantID || query.RepositoryID != runtime.repositoryID {
		return fmt.Errorf("query scope does not match configured authority")
	}
	if query.ExpectedGeneration == nil {
		query.ExpectedGeneration = &runtime.generation
	} else if *query.ExpectedGeneration != runtime.generation {
		return fmt.Errorf("query generation does not match configured embedding")
	}
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	service := retrieval.QueryService{
		Client: runtime.qdrant, CollectionName: runtime.collection,
		Embedder: runtime.embedder, Sparse: retrieval.HashingSparseEncoder{},
		Resolver: runtime.store, Freshness: runtime.store, QueryTimeout: *timeout,
	}
	result, err := service.Search(ctx, query)
	if err != nil {
		return fmt.Errorf("query governed retrieval: %w", err)
	}
	return writeQueryResult(*output, result)
}

type runtime struct {
	store             *postgres.Store
	qdrant            qdrantClient
	embedder          retrieval.Embedder
	memoryBodies      *objectstore.Store
	tenantID          string
	repositoryID      string
	collection        string
	generation        string
	embeddingProvider string
}

// qdrantClient is the smallest runtime capability set; lifecycle mutations
// such as alias switching remain in the explicit operator path.
type qdrantClient interface {
	Close() error
	retrieval.QdrantQueryClient
	retrieval.QdrantUpsertClient
	retrieval.QdrantDeleteClient
}

func openRuntime(ctx context.Context, lookup func(string) (string, bool)) (runtime, func(), error) {
	config, err := runtimeConfigFromEnv(lookup)
	if err != nil {
		return runtime{}, func() {}, err
	}
	openContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pool, err := postgres.Open(openContext, config.databaseURL, 4)
	if err != nil {
		return runtime{}, func() {}, fmt.Errorf("open canonical PostgreSQL: %w", err)
	}
	closePool := func() { pool.Close() }
	memoryBodies, err := objectstore.New(openContext, objectstore.Config{
		Bucket: config.s3Bucket, Region: config.s3Region, BaseEndpoint: config.s3Endpoint, UsePathStyle: config.s3PathStyle,
	})
	if err != nil {
		closePool()
		return runtime{}, func() {}, fmt.Errorf("configure governed memory object reader: %w", err)
	}
	store, err := postgres.NewStore(pool, nil, config.tenantID, config.repositoryID, postgres.WithMemoryBodyReader(memoryBodies))
	if err != nil {
		closePool()
		return runtime{}, func() {}, err
	}
	if err := store.Ready(openContext); err != nil {
		closePool()
		return runtime{}, func() {}, fmt.Errorf("canonical PostgreSQL is not ready: %w", err)
	}
	embedder, err := openEmbedder(openContext, config)
	if err != nil {
		closePool()
		return runtime{}, func() {}, err
	}
	client, err := retrieval.OpenQdrant(retrieval.QdrantEndpoint{
		Host: config.qdrantHost, Port: config.qdrantPort, APIKey: config.qdrantAPIKey, UseTLS: config.qdrantTLS,
	})
	if err != nil {
		closePool()
		return runtime{}, func() {}, err
	}
	descriptor := embedder.Descriptor()
	generation := contracts.RetrievalGenerationID(descriptor.Model, descriptor.Version, descriptor.Dimensions, descriptor.SparseEncoderVersion)
	return runtime{
			store: store, qdrant: client, embedder: embedder, memoryBodies: memoryBodies,
			tenantID: config.tenantID, repositoryID: config.repositoryID, collection: config.collection,
			generation: generation, embeddingProvider: config.embeddingProvider,
		}, func() {
			_ = client.Close()
			closePool()
		}, nil
}

type runtimeConfig struct {
	databaseURL, tenantID, repositoryID  string
	qdrantHost, qdrantAPIKey, collection string
	s3Bucket, s3Region, s3Endpoint       string
	embeddingProvider                    string
	localEmbeddingEndpoint               string
	localEmbeddingModel                  string
	localEmbeddingVersion                string
	localEmbeddingDimensions             int
	qdrantPort                           int
	qdrantTLS, s3PathStyle               bool
	region                               string
}

func openEmbedder(ctx context.Context, config runtimeConfig) (retrieval.Embedder, error) {
	switch config.embeddingProvider {
	case "bedrock":
		embedder, err := retrieval.NewBedrockTitanEmbedder(ctx, retrieval.BedrockTitanConfig{
			Region: config.region, Model: retrieval.BedrockTitanTextEmbeddingV2Model,
			Version: retrieval.BedrockTitanTextEmbeddingV2Version, Dimensions: retrieval.BedrockTitanTextEmbeddingV2Dimensions,
			SparseEncoderVersion: retrieval.SparseEncoderVersion,
		})
		if err != nil {
			return nil, fmt.Errorf("configure Bedrock embedding: %w", err)
		}
		return embedder, nil
	case "local":
		embedder, err := retrieval.NewLocalHTTPEmbedder(retrieval.LocalHTTPEmbeddingConfig{
			Endpoint: config.localEmbeddingEndpoint, Model: config.localEmbeddingModel,
			Version: config.localEmbeddingVersion, Dimensions: config.localEmbeddingDimensions,
			SparseEncoderVersion: retrieval.SparseEncoderVersion,
		})
		if err != nil {
			return nil, fmt.Errorf("configure local embedding: %w", err)
		}
		return embedder, nil
	default:
		return nil, errors.New("embedding provider is invalid")
	}
}

func runtimeConfigFromEnv(lookup func(string) (string, bool)) (runtimeConfig, error) {
	if lookup == nil {
		return runtimeConfig{}, errors.New("environment lookup is required")
	}
	get := func(name string) string { value, _ := lookup(name); return strings.TrimSpace(value) }
	// The provider adapter uses only the AWS SDK credential chain. Refuse the
	// legacy application bearer boundary instead of silently coexisting with it
	// in a process that is intended to demonstrate workload identity.
	if get("CHAVE_API_AWS_BEDROCK") != "" || get("AWS_BEARER_TOKEN_BEDROCK") != "" {
		return runtimeConfig{}, errors.New("legacy Bedrock application credentials are not accepted")
	}
	config := runtimeConfig{
		databaseURL: get("FORJA_DATABASE_URL"), tenantID: get("FORJA_TENANT_ID"), repositoryID: get("FORJA_REPOSITORY_ID"),
		qdrantHost: get("FORJA_QDRANT_HOST"), qdrantAPIKey: get("FORJA_QDRANT_API_KEY"),
		collection: get("FORJA_RETRIEVAL_COLLECTION"), region: get("AWS_REGION"), qdrantPort: 6334,
		s3Bucket: get("FORJA_S3_BUCKET"), s3Region: get("FORJA_S3_REGION"), s3Endpoint: get("FORJA_S3_ENDPOINT"),
		embeddingProvider:      get("FORJA_RETRIEVAL_EMBEDDING_PROVIDER"),
		localEmbeddingEndpoint: get("FORJA_LOCAL_EMBEDDING_ENDPOINT"),
		localEmbeddingModel:    get("FORJA_LOCAL_EMBEDDING_MODEL"),
		localEmbeddingVersion:  get("FORJA_LOCAL_EMBEDDING_VERSION"),
	}
	if config.embeddingProvider == "" {
		config.embeddingProvider = "bedrock"
	}
	if config.embeddingProvider != "bedrock" && config.embeddingProvider != "local" {
		return runtimeConfig{}, errors.New("FORJA_RETRIEVAL_EMBEDDING_PROVIDER is invalid")
	}
	if value := get("FORJA_LOCAL_EMBEDDING_DIMENSIONS"); value != "" {
		dimensions, err := strconv.Atoi(value)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("FORJA_LOCAL_EMBEDDING_DIMENSIONS is invalid")
		}
		config.localEmbeddingDimensions = dimensions
	}
	if value := get("FORJA_QDRANT_GRPC_PORT"); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("FORJA_QDRANT_GRPC_PORT is invalid")
		}
		config.qdrantPort = port
	}
	if value := get("FORJA_QDRANT_TLS"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("FORJA_QDRANT_TLS is invalid")
		}
		config.qdrantTLS = enabled
	}
	if value := get("FORJA_S3_PATH_STYLE"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("FORJA_S3_PATH_STYLE is invalid")
		}
		config.s3PathStyle = enabled
	}
	if config.databaseURL == "" || config.tenantID == "" || config.repositoryID == "" || config.qdrantHost == "" || config.collection == "" || config.s3Bucket == "" || config.s3Region == "" {
		return runtimeConfig{}, errors.New("FORJA_DATABASE_URL, FORJA_TENANT_ID, FORJA_REPOSITORY_ID, FORJA_QDRANT_HOST, FORJA_RETRIEVAL_COLLECTION, FORJA_S3_BUCKET, and FORJA_S3_REGION are required")
	}
	if config.embeddingProvider == "bedrock" && config.region == "" {
		return runtimeConfig{}, errors.New("AWS_REGION is required for Bedrock embeddings")
	}
	if config.embeddingProvider == "local" && (config.localEmbeddingEndpoint == "" || config.localEmbeddingModel == "" || config.localEmbeddingVersion == "" || config.localEmbeddingDimensions == 0) {
		return runtimeConfig{}, errors.New("FORJA_LOCAL_EMBEDDING_ENDPOINT, FORJA_LOCAL_EMBEDDING_MODEL, FORJA_LOCAL_EMBEDDING_VERSION, and FORJA_LOCAL_EMBEDDING_DIMENSIONS are required for local embeddings")
	}
	if _, err := (retrieval.QdrantEndpoint{Host: config.qdrantHost, Port: config.qdrantPort, APIKey: config.qdrantAPIKey, UseTLS: config.qdrantTLS}).ClientConfig(); err != nil {
		return runtimeConfig{}, err
	}
	return config, nil
}

type projectionReceipt struct {
	SchemaVersion string `json:"schema_version"`
	Generation    string `json:"generation"`
	Claimed       int    `json:"claimed"`
	Published     int    `json:"published"`
	Skipped       int    `json:"skipped"`
	Retried       int    `json:"retried"`
	Dead          int    `json:"dead"`
}

type evaluationCapturePlan struct {
	SchemaVersion string                        `json:"schema_version"`
	CorpusID      string                        `json:"corpus_id"`
	Queries       []evaluationCapturePlanQuery  `json:"queries"`
	Policies      []evaluationCapturePlanPolicy `json:"policies"`
}

type evaluationCapturePlanQuery struct {
	CaseID string                   `json:"case_id"`
	Query  contracts.RetrievalQuery `json:"query"`
}

type evaluationCapturePlanPolicy struct {
	Name   string                    `json:"name"`
	Policy contracts.RetrievalPolicy `json:"policy"`
}

type evaluationComparisonCapture struct {
	SchemaVersion string                        `json:"schema_version"`
	CorpusID      string                        `json:"corpus_id"`
	Variants      []retrieval.EvaluationVariant `json:"variants"`
}

type retrievalPreflightReceipt struct {
	SchemaVersion       string `json:"schema_version"`
	Generation          string `json:"generation"`
	PostgresReady       bool   `json:"postgres_ready"`
	QdrantVerified      bool   `json:"qdrant_verified"`
	EmbeddingProvider   string `json:"embedding_provider"`
	EmbeddingDimensions int    `json:"embedding_dimensions"`
	BedrockDimensions   int    `json:"bedrock_dimensions,omitempty"`
}

func readQuery(path string) (contracts.RetrievalQuery, error) {
	data, err := readPrivateJSONFile(path, maximumQueryFileBytes, "retrieval query")
	if err != nil {
		return contracts.RetrievalQuery{}, err
	}
	registry, err := contracts.NewRegistry()
	if err != nil {
		return contracts.RetrievalQuery{}, fmt.Errorf("initialize contracts: %w", err)
	}
	query, err := contracts.DecodeStrict[contracts.RetrievalQuery](registry, "retrieval-query.schema.json", data)
	if err != nil {
		return contracts.RetrievalQuery{}, fmt.Errorf("decode retrieval query: %w", err)
	}
	return query, nil
}

func readEvaluationCapturePlan(path string) (evaluationCapturePlan, error) {
	data, err := readPrivateJSONFile(path, maximumEvaluationPlanBytes, "retrieval evaluation plan")
	if err != nil {
		return evaluationCapturePlan{}, err
	}
	registry, err := contracts.NewRegistry()
	if err != nil {
		return evaluationCapturePlan{}, fmt.Errorf("initialize contracts: %w", err)
	}
	plan, err := contracts.DecodeStrict[evaluationCapturePlan](registry, "retrieval-evaluation-query-plan.schema.json", data)
	if err != nil {
		return evaluationCapturePlan{}, fmt.Errorf("decode retrieval evaluation plan: %w", err)
	}
	return plan, nil
}

func readPrivateJSONFile(path string, maximumBytes int64, description string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", description, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", description, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s must be a private regular file", description)
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", description, err)
	}
	if int64(len(data)) > maximumBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", description, maximumBytes)
	}
	return data, nil
}

func normalizeEvaluationCapturePlan(runtime runtime, plan evaluationCapturePlan) ([]retrieval.EvaluationQueryCase, []retrieval.EvaluationCapturePolicy, error) {
	cases := make([]retrieval.EvaluationQueryCase, 0, len(plan.Queries))
	for _, planQuery := range plan.Queries {
		query := planQuery.Query
		if query.TenantID != runtime.tenantID || query.RepositoryID != runtime.repositoryID {
			return nil, nil, fmt.Errorf("evaluation query %s scope does not match configured authority", planQuery.CaseID)
		}
		if query.ExpectedGeneration == nil {
			query.ExpectedGeneration = &runtime.generation
		} else if *query.ExpectedGeneration != runtime.generation {
			return nil, nil, fmt.Errorf("evaluation query %s generation does not match configured embedding", planQuery.CaseID)
		}
		cases = append(cases, retrieval.EvaluationQueryCase{CaseID: planQuery.CaseID, Query: query})
	}
	policies := make([]retrieval.EvaluationCapturePolicy, 0, len(plan.Policies))
	for _, policy := range plan.Policies {
		policies = append(policies, retrieval.EvaluationCapturePolicy{Name: policy.Name, Policy: policy.Policy})
	}
	return cases, policies, nil
}

func writeQueryResult(path string, result contracts.RetrievalResult) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode retrieval result: %w", err)
	}
	registry, err := contracts.NewRegistry()
	if err != nil {
		return fmt.Errorf("initialize contracts: %w", err)
	}
	if err := registry.ValidateJSON("retrieval-result.schema.json", encoded); err != nil {
		return fmt.Errorf("validate retrieval result: %w", err)
	}
	return writePrivateJSON(path, result)
}

func writeEvaluationComparison(path string, comparison evaluationComparisonCapture) error {
	encoded, err := json.Marshal(comparison)
	if err != nil {
		return fmt.Errorf("encode retrieval evaluation comparison: %w", err)
	}
	registry, err := contracts.NewRegistry()
	if err != nil {
		return fmt.Errorf("initialize contracts: %w", err)
	}
	if err := registry.ValidateJSON("retrieval-evaluation-comparison.schema.json", encoded); err != nil {
		return fmt.Errorf("validate retrieval evaluation comparison: %w", err)
	}
	return writePrivateJSON(path, comparison)
}

func writePreflightReceipt(path string, receipt retrievalPreflightReceipt) error {
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode retrieval preflight receipt: %w", err)
	}
	registry, err := contracts.NewRegistry()
	if err != nil {
		return fmt.Errorf("initialize contracts: %w", err)
	}
	if err := registry.ValidateJSON("retrieval-preflight-receipt.schema.json", encoded); err != nil {
		return fmt.Errorf("validate retrieval preflight receipt: %w", err)
	}
	return writePrivateJSON(path, receipt)
}

func writePrivateJSON(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode operation receipt: %w", err)
	}
	encoded = append(encoded, '\n')
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".forja-retrieval-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}

func validOperationTimeout(value time.Duration) bool {
	return value > 0 && value <= maximumOperationTime
}

func validEvaluationCaptureTimeout(value time.Duration) bool {
	return value > 0 && value <= maximumEvaluationCaptureTime
}
func safeInputPath(path string) bool  { return strings.TrimSpace(path) != "" && path != "-" }
func safeOutputPath(path string) bool { return strings.TrimSpace(path) != "" && path != "-" }
