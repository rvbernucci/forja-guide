// Command forja-retrieval-eval scores an offline governed-retrieval run.
// It deliberately has no network, database, or secret configuration surface.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/retrieval"
)

const maximumEvaluationFileBytes = 16 << 20

var (
	commitPattern      = regexp.MustCompile(`^[a-f0-9]{7,64}$`)
	contentHashPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

type evaluationCorpus struct {
	SchemaVersion string                     `json:"schema_version"`
	CorpusID      string                     `json:"corpus_id"`
	Split         string                     `json:"split"`
	Cases         []retrieval.EvaluationCase `json:"cases"`
}

type evaluationOutcomes struct {
	SchemaVersion string                        `json:"schema_version"`
	CorpusID      string                        `json:"corpus_id"`
	Outcomes      []retrieval.EvaluationOutcome `json:"outcomes"`
}

type embeddingDescriptor struct {
	Model                string `json:"model"`
	Version              string `json:"version"`
	Dimensions           int    `json:"dimensions"`
	SparseEncoderVersion string `json:"sparse_encoder_version"`
}

type evaluationReport struct {
	SchemaVersion        string                   `json:"schema_version"`
	CorpusID             string                   `json:"corpus_id"`
	CorpusSHA256         string                   `json:"corpus_sha256"`
	Split                string                   `json:"split"`
	CodeCommit           string                   `json:"code_commit"`
	Embedding            embeddingDescriptor      `json:"embedding"`
	PolicyHash           string                   `json:"policy_hash"`
	K                    int                      `json:"k"`
	SampleCount          int                      `json:"sample_count"`
	Metrics              retrieval.RankingMetrics `json:"metrics"`
	DurationMilliseconds int64                    `json:"duration_milliseconds"`
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "forja-retrieval-eval: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	flags := flag.NewFlagSet("forja-retrieval-eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	corpusPath := flags.String("corpus", "", "retrieval evaluation corpus JSON path")
	outcomesPath := flags.String("outcomes", "", "retrieval evaluation outcomes JSON path")
	outputPath := flags.String("output", "-", "report JSON path, or - for stdout")
	k := flags.Int("k", 10, "bounded ranking cutoff")
	commit := flags.String("commit", "", "immutable evaluated source commit")
	model := flags.String("embedding-model", "", "embedding model identifier")
	version := flags.String("embedding-version", "", "embedding model version")
	dimensions := flags.Int("embedding-dimensions", 0, "embedding vector dimensions")
	sparseVersion := flags.String("sparse-encoder-version", "", "sparse encoder version")
	policyHash := flags.String("policy-hash", "", "sha256 policy hash")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("positional arguments are not accepted")
	}
	if err := validateArguments(*corpusPath, *outcomesPath, *outputPath, *k, *commit, *model, *version, *dimensions, *sparseVersion, *policyHash); err != nil {
		return err
	}
	registry, err := contracts.NewRegistry()
	if err != nil {
		return fmt.Errorf("initialize contract registry: %w", err)
	}
	corpusData, err := readBounded(*corpusPath)
	if err != nil {
		return fmt.Errorf("read corpus: %w", err)
	}
	corpus, err := contracts.DecodeStrict[evaluationCorpus](registry, "retrieval-evaluation-corpus.schema.json", corpusData)
	if err != nil {
		return fmt.Errorf("decode corpus: %w", err)
	}
	outcomeData, err := readBounded(*outcomesPath)
	if err != nil {
		return fmt.Errorf("read outcomes: %w", err)
	}
	outcomes, err := contracts.DecodeStrict[evaluationOutcomes](registry, "retrieval-evaluation-outcomes.schema.json", outcomeData)
	if err != nil {
		return fmt.Errorf("decode outcomes: %w", err)
	}
	if outcomes.CorpusID != corpus.CorpusID {
		return fmt.Errorf("outcomes corpus ID does not match corpus")
	}
	started := time.Now()
	metrics, err := retrieval.ScoreRankings(corpus.Cases, outcomes.Outcomes, *k)
	if err != nil {
		return fmt.Errorf("score rankings: %w", err)
	}
	digest := sha256.Sum256(corpusData)
	report := evaluationReport{
		SchemaVersion: "1.0", CorpusID: corpus.CorpusID,
		CorpusSHA256: "sha256:" + hex.EncodeToString(digest[:]), Split: corpus.Split,
		CodeCommit: *commit,
		Embedding:  embeddingDescriptor{Model: *model, Version: *version, Dimensions: *dimensions, SparseEncoderVersion: *sparseVersion},
		PolicyHash: *policyHash, K: *k, SampleCount: len(corpus.Cases), Metrics: metrics,
		DurationMilliseconds: time.Since(started).Milliseconds(),
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := registry.ValidateJSON("retrieval-evaluation-report.schema.json", encoded); err != nil {
		return fmt.Errorf("validate report: %w", err)
	}
	if *outputPath == "-" {
		_, err = stdout.Write(encoded)
		return err
	}
	return writeAtomic(*outputPath, encoded)
}

func validateArguments(corpusPath, outcomesPath, outputPath string, k int, commit, model, version string, dimensions int, sparseVersion, policyHash string) error {
	if corpusPath == "" || outcomesPath == "" || outputPath == "" || k < 1 || k > 1000 || !commitPattern.MatchString(commit) || model == "" || version == "" || dimensions < 1 || dimensions > 65536 || sparseVersion == "" || !contentHashPattern.MatchString(policyHash) {
		return fmt.Errorf("corpus, outcomes, output, bounded k, lowercase commit, embedding descriptor, and sha256 policy hash are required")
	}
	return nil
}

func readBounded(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximumEvaluationFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maximumEvaluationFileBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", maximumEvaluationFileBytes)
	}
	return data, nil
}

func writeAtomic(path string, data []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".forja-retrieval-eval-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
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
