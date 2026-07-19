package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/logging"
	"github.com/rvbernucci/forja-guide/internal/observability"
	"github.com/rvbernucci/forja-guide/internal/version"
	"github.com/rvbernucci/forja-guide/internal/worker"
)

const maximumTaskBytes = 1 << 20

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Environ()))
}

func run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	environ []string,
) int {
	flags := flag.NewFlagSet("forja-worker", flag.ContinueOnError)
	flags.SetOutput(stderr)
	taskPath := flags.String("task", "-", "worker task JSON file, or - for stdin")
	resultPath := flags.String("result", "-", "worker result JSON file, or - for stdout")
	codexPath := flags.String("codex", "codex", "Codex CLI executable")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		if err == nil {
			_, _ = fmt.Fprintln(stderr, "forja-worker: positional arguments are not accepted")
		}
		return 2
	}
	registry, err := contracts.NewRegistry()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "forja-worker: initialize contracts: %v\n", err)
		return 1
	}
	taskData, err := readBounded(*taskPath, os.Stdin)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "forja-worker: read task: %v\n", err)
		return 2
	}
	task, err := contracts.DecodeStrict[contracts.WorkerTask](
		registry,
		"worker-task.schema.json",
		taskData,
	)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "forja-worker: invalid task: %v\n", err)
		return 2
	}
	lookup := environmentLookup(environ)
	environment := "development"
	if value, ok := lookup("FORJA_ENVIRONMENT"); ok && strings.TrimSpace(value) != "" {
		environment = strings.TrimSpace(value)
	}
	logger := logging.New(stderr, "info")
	telemetryConfig, err := observability.RuntimeConfigFromEnv(
		"forja-worker", version.Version, environment, lookup,
	)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "forja-worker: configure observability: %v\n", err)
		return 2
	}
	telemetry, err := observability.NewRuntime(ctx, telemetryConfig, logger)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "forja-worker: initialize observability: %v\n", err)
		return 1
	}
	defer shutdownWorkerTelemetry(telemetry, logger)
	ctx = observability.ExtractTraceContextFromEnv(ctx, lookup)
	supervisor, err := worker.NewSupervisor(
		registry,
		worker.CodexAdapter{Executable: *codexPath},
		worker.CodexIsolationPolicy{Executable: *codexPath},
		&eventWriter{writer: stderr},
		environ,
		telemetry.Observer,
	)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "forja-worker: initialize supervisor: %v\n", err)
		return 1
	}
	result, executeErr := supervisor.Execute(ctx, task)
	if result.TaskID != "" {
		if err := writeResult(*resultPath, stdout, result); err != nil {
			_, _ = fmt.Fprintf(stderr, "forja-worker: write result: %v\n", err)
			return 1
		}
	}
	if executeErr != nil {
		_, _ = fmt.Fprintf(stderr, "forja-worker: execution failed closed: %v\n", executeErr)
		return 1
	}
	switch result.Status {
	case "succeeded":
		return 0
	case "blocked":
		return 3
	case "cancelled":
		return 130
	default:
		return 1
	}
}

func environmentLookup(environ []string) func(string) (string, bool) {
	values := make(map[string]string, len(environ))
	for _, item := range environ {
		key, value, found := strings.Cut(item, "=")
		if found && key != "" {
			values[key] = value
		}
	}
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func shutdownWorkerTelemetry(runtime *observability.Runtime, logger interface {
	Warn(string, ...any)
}) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		logger.Warn(
			"worker telemetry shutdown incomplete",
			"failure_class", observability.FailureUnavailable,
		)
	}
}

type eventWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *eventWriter) Emit(_ context.Context, event worker.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return json.NewEncoder(w.writer).Encode(event)
}

func readBounded(path string, stdin io.Reader) ([]byte, error) {
	reader := stdin
	var file *os.File
	if path != "-" {
		var err error
		file, err = os.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, maximumTaskBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maximumTaskBytes {
		return nil, fmt.Errorf("task exceeds %d bytes", maximumTaskBytes)
	}
	return data, nil
}

func writeResult(
	path string,
	stdout io.Writer,
	result contracts.WorkerResult,
) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if path == "-" {
		_, err = stdout.Write(data)
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".forja-worker-result-*")
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
	return syncDirectory(directory)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}
