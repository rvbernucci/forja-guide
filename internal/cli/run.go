package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/version"
)

const defaultEndpoint = "http://127.0.0.1:8080"

// Run executes the CLI and returns a process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	if args[0] == "version" || args[0] == "--version" {
		return writeOutput(stdout, version.Current())
	}

	registry, err := contracts.NewRegistry()
	if err != nil {
		fmt.Fprintf(stderr, "initialize contracts: %v\n", err)
		return 1
	}

	var code int
	switch args[0] {
	case "run":
		code = runCommand(ctx, args[1:], stdout, stderr, registry)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printUsage(stderr)
		code = 2
	}
	return code
}

func runCommand(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	registry *contracts.Registry,
) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "run subcommand is required")
		return 2
	}
	switch args[0] {
	case "create":
		return runCreate(ctx, args[1:], stdout, stderr, registry)
	case "get":
		return runGet(ctx, args[1:], stdout, stderr, registry)
	case "transition":
		return runTransition(ctx, args[1:], stdout, stderr, registry)
	default:
		fmt.Fprintf(stderr, "unknown run subcommand %q\n", args[0])
		return 2
	}
}

func runCreate(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	registry *contracts.Registry,
) int {
	set := newFlagSet("run create", stderr)
	endpoint := endpointFlag(set)
	objective := set.String("objective", "", "run objective")
	idempotencyKey := set.String(
		"idempotency-key",
		"",
		"stable key to reuse when retrying this command",
	)
	if err := set.Parse(args); err != nil {
		return 2
	}
	if *objective == "" {
		fmt.Fprintln(stderr, "--objective is required")
		return 2
	}
	client, err := NewClient(*endpoint, nil, registry)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	metadata, err := PrepareCommand(*idempotencyKey)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	run, err := client.CreateRunWithCommand(ctx, *objective, metadata)
	if err != nil {
		fmt.Fprintf(stderr, "command %s: %v\n", metadata.IdempotencyKey, err)
		return 1
	}
	return writeCommandOutput(stdout, stderr, metadata, run)
}

func runGet(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	registry *contracts.Registry,
) int {
	set := newFlagSet("run get", stderr)
	endpoint := endpointFlag(set)
	runID := set.String("id", "", "run identifier")
	if err := set.Parse(args); err != nil {
		return 2
	}
	if *runID == "" {
		fmt.Fprintln(stderr, "--id is required")
		return 2
	}
	client, err := NewClient(*endpoint, nil, registry)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	run, err := client.GetRun(ctx, *runID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeOutput(stdout, run)
}

func runTransition(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	registry *contracts.Registry,
) int {
	set := newFlagSet("run transition", stderr)
	endpoint := endpointFlag(set)
	runID := set.String("id", "", "run identifier")
	expected := set.Int("expected-version", 0, "expected aggregate version")
	target := set.String("to", "", "target run state")
	idempotencyKey := set.String(
		"idempotency-key",
		"",
		"stable key to reuse when retrying this command",
	)
	if err := set.Parse(args); err != nil {
		return 2
	}
	if *runID == "" || *expected < 1 || *target == "" {
		fmt.Fprintln(stderr, "--id, --expected-version, and --to are required")
		return 2
	}
	client, err := NewClient(*endpoint, nil, registry)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	metadata, err := PrepareCommand(*idempotencyKey)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	run, err := client.TransitionRunWithCommand(
		ctx,
		*runID,
		*expected,
		*target,
		metadata,
	)
	if err != nil {
		fmt.Fprintf(stderr, "command %s: %v\n", metadata.IdempotencyKey, err)
		return 1
	}
	return writeCommandOutput(stdout, stderr, metadata, run)
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(stderr)
	return set
}

func endpointFlag(set *flag.FlagSet) *string {
	value := os.Getenv("FORJA_ENDPOINT")
	if value == "" {
		value = defaultEndpoint
	}
	return set.String("endpoint", value, "forjad endpoint")
}

func writeOutput(output io.Writer, value any) int {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return 1
	}
	return 0
}

func writeCommandOutput(
	stdout io.Writer,
	stderr io.Writer,
	metadata CommandMetadata,
	value any,
) int {
	if code := writeOutput(stdout, value); code != 0 {
		fmt.Fprintf(
			stderr,
			"command %s committed, but writing its response failed; replay with the same key\n",
			metadata.IdempotencyKey,
		)
		return code
	}
	return 0
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, "usage: forja <version|run create|run get|run transition>")
	fmt.Fprintln(output, "set FORJA_ENDPOINT or pass --endpoint to select forjad")
}

// ParseTimeout reads a positive CLI timeout from an environment value.
func ParseTimeout(value string) (time.Duration, error) {
	if value == "" {
		return 30 * time.Second, nil
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0, fmt.Errorf("timeout seconds must be positive")
		}
		return time.Duration(seconds) * time.Second, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("invalid timeout %q", value)
	}
	return duration, nil
}
