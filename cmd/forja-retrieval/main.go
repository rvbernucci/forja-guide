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
	"github.com/rvbernucci/forja-guide/internal/postgres"
	"github.com/rvbernucci/forja-guide/internal/retrieval"
)

const (
	maximumQueryFileBytes = 64 << 10
	maximumOperationTime  = 30 * time.Second
	defaultOperationTime  = 20 * time.Second
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.LookupEnv); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "forja-retrieval: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, stdout, stderr io.Writer, lookup func(string) (string, bool)) error {
	if len(arguments) == 0 {
		return fmt.Errorf("expected project-once or query subcommand")
	}
	switch arguments[0] {
	case "project-once":
		return projectOnce(ctx, arguments[1:], stdout, stderr, lookup)
	case "query":
		return queryOnce(ctx, arguments[1:], stdout, stderr, lookup)
	default:
		return fmt.Errorf("unsupported subcommand %q", arguments[0])
	}
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
		Deliveries: runtime.store, Source: runtime.store, Recorder: runtime.store,
		Writer: writer, Deleter: writer, Embedder: runtime.embedder, Sparse: retrieval.HashingSparseEncoder{},
		WorkerID: *workerID, Generation: runtime.generation, BatchSize: *batchSize,
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
		Resolver: runtime.store, QueryTimeout: *timeout,
	}
	result, err := service.Search(ctx, query)
	if err != nil {
		return fmt.Errorf("query governed retrieval: %w", err)
	}
	return writeQueryResult(*output, result)
}

type runtime struct {
	store        *postgres.Store
	qdrant       qdrantClient
	embedder     *retrieval.BedrockTitanEmbedder
	tenantID     string
	repositoryID string
	collection   string
	generation   string
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
	store, err := postgres.NewStore(pool, nil, config.tenantID, config.repositoryID)
	if err != nil {
		closePool()
		return runtime{}, func() {}, err
	}
	if err := store.Ready(openContext); err != nil {
		closePool()
		return runtime{}, func() {}, fmt.Errorf("canonical PostgreSQL is not ready: %w", err)
	}
	embedder, err := retrieval.NewBedrockTitanEmbedder(openContext, retrieval.BedrockTitanConfig{
		Region: config.region, Model: retrieval.BedrockTitanTextEmbeddingV2Model,
		Version: retrieval.BedrockTitanTextEmbeddingV2Version, Dimensions: retrieval.BedrockTitanTextEmbeddingV2Dimensions,
		SparseEncoderVersion: retrieval.SparseEncoderVersion,
	})
	if err != nil {
		closePool()
		return runtime{}, func() {}, fmt.Errorf("configure Bedrock embedding: %w", err)
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
			store: store, qdrant: client, embedder: embedder,
			tenantID: config.tenantID, repositoryID: config.repositoryID, collection: config.collection, generation: generation,
		}, func() {
			_ = client.Close()
			closePool()
		}, nil
}

type runtimeConfig struct {
	databaseURL, tenantID, repositoryID  string
	qdrantHost, qdrantAPIKey, collection string
	qdrantPort                           int
	qdrantTLS                            bool
	region                               string
}

func runtimeConfigFromEnv(lookup func(string) (string, bool)) (runtimeConfig, error) {
	if lookup == nil {
		return runtimeConfig{}, errors.New("environment lookup is required")
	}
	get := func(name string) string { value, _ := lookup(name); return strings.TrimSpace(value) }
	config := runtimeConfig{
		databaseURL: get("FORJA_DATABASE_URL"), tenantID: get("FORJA_TENANT_ID"), repositoryID: get("FORJA_REPOSITORY_ID"),
		qdrantHost: get("FORJA_QDRANT_HOST"), qdrantAPIKey: get("FORJA_QDRANT_API_KEY"),
		collection: get("FORJA_RETRIEVAL_COLLECTION"), region: get("AWS_REGION"), qdrantPort: 6334,
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
	if config.databaseURL == "" || config.tenantID == "" || config.repositoryID == "" || config.qdrantHost == "" || config.collection == "" || config.region == "" {
		return runtimeConfig{}, errors.New("FORJA_DATABASE_URL, FORJA_TENANT_ID, FORJA_REPOSITORY_ID, FORJA_QDRANT_HOST, FORJA_RETRIEVAL_COLLECTION, and AWS_REGION are required")
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

func readQuery(path string) (contracts.RetrievalQuery, error) {
	file, err := os.Open(path)
	if err != nil {
		return contracts.RetrievalQuery{}, fmt.Errorf("open retrieval query: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return contracts.RetrievalQuery{}, fmt.Errorf("stat retrieval query: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return contracts.RetrievalQuery{}, errors.New("retrieval query must be a private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumQueryFileBytes+1))
	if err != nil {
		return contracts.RetrievalQuery{}, fmt.Errorf("read retrieval query: %w", err)
	}
	if len(data) > maximumQueryFileBytes {
		return contracts.RetrievalQuery{}, fmt.Errorf("retrieval query exceeds %d bytes", maximumQueryFileBytes)
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
func safeInputPath(path string) bool  { return strings.TrimSpace(path) != "" && path != "-" }
func safeOutputPath(path string) bool { return strings.TrimSpace(path) != "" && path != "-" }
