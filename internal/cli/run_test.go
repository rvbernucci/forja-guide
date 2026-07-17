package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/clock"
	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/daemon"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

func TestRunCreateAndInspect(t *testing.T) {
	t.Parallel()
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	id := identity.RunID("run_00010203-0405-4607-8809-0a0b0c0d0e0f")
	api, err := daemon.New(
		runstate.NewStore(clock.Fixed{
			Time: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
		}),
		registry,
		func() (identity.RunID, error) { return id, nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	var output bytes.Buffer
	var errors bytes.Buffer
	code := Run(
		context.Background(),
		[]string{
			"run", "create",
			"--endpoint", server.URL,
			"--objective", "Synthetic CLI run",
		},
		&output,
		&errors,
	)
	if code != 0 {
		t.Fatalf("create code=%d stderr=%s", code, errors.String())
	}
	var created contracts.Run
	if err := json.Unmarshal(output.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	output.Reset()
	errors.Reset()
	code = Run(
		context.Background(),
		[]string{"run", "get", "--endpoint", server.URL, "--id", created.RunID},
		&output,
		&errors,
	)
	if code != 0 {
		t.Fatalf("get code=%d stderr=%s", code, errors.String())
	}
	var inspected contracts.Run
	if err := json.Unmarshal(output.Bytes(), &inspected); err != nil {
		t.Fatal(err)
	}
	if inspected != created {
		t.Fatalf("created=%#v inspected=%#v", created, inspected)
	}
}

func TestRunRejectsIncompleteCommands(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	var errors bytes.Buffer
	if code := Run(context.Background(), nil, &output, &errors); code != 2 {
		t.Fatalf("unexpected no-args code: %d", code)
	}
	errors.Reset()
	if code := Run(
		context.Background(),
		[]string{"run", "create"},
		&output,
		&errors,
	); code != 2 {
		t.Fatalf("unexpected missing-objective code: %d", code)
	}
}

func TestMutationOutputFailureReportsReusableIdempotencyKey(t *testing.T) {
	t.Parallel()
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	runID := identity.RunID("run_00010203-0405-4607-8809-0a0b0c0d0e0f")
	store := runstate.NewStore(clock.Fixed{
		Time: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	})
	api, err := daemon.New(
		store,
		registry,
		func() (identity.RunID, error) { return runID, nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	cases := []struct {
		name string
		key  string
		args []string
	}{
		{
			name: "create",
			key:  "cli-create-output-failure",
			args: []string{
				"run", "create",
				"--endpoint", server.URL,
				"--objective", "Retain the retry key after output failure",
			},
		},
		{
			name: "transition",
			key:  "cli-transition-output-failure",
			args: []string{
				"run", "transition",
				"--endpoint", server.URL,
				"--id", runID.String(),
				"--expected-version", "1",
				"--to", "awaiting_approval",
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var stderr bytes.Buffer
			args := append(
				append([]string(nil), testCase.args...),
				"--idempotency-key", testCase.key,
			)
			code := Run(t.Context(), args, failingWriter{}, &stderr)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if !strings.Contains(stderr.String(), testCase.key) {
				t.Fatalf("stderr %q omitted retry key %q", stderr.String(), testCase.key)
			}
		})
	}
	committed, err := store.Get(runID)
	if err != nil {
		t.Fatalf("read committed run after output failures: %v", err)
	}
	if committed.State != string(runstate.StateAwaitingApproval) ||
		committed.Version != 2 {
		t.Fatalf("committed run = %#v", committed)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("synthetic output failure")
}

func TestParseTimeout(t *testing.T) {
	t.Parallel()
	cases := map[string]time.Duration{
		"":     30 * time.Second,
		"5":    5 * time.Second,
		"2.5s": 2500 * time.Millisecond,
	}
	for input, expected := range cases {
		actual, err := ParseTimeout(input)
		if err != nil || actual != expected {
			t.Fatalf("input=%q actual=%s expected=%s err=%v", input, actual, expected, err)
		}
	}
	for _, invalid := range []string{"0", "-1s", "bad"} {
		if _, err := ParseTimeout(invalid); err == nil {
			t.Fatalf("expected %q to fail", invalid)
		}
	}
}

func TestDefaultClientDefersDeadlineToCommandContext(t *testing.T) {
	t.Parallel()
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient("http://127.0.0.1:8080", nil, registry)
	if err != nil {
		t.Fatal(err)
	}
	if client.httpClient.Timeout != 0 {
		t.Fatalf("unexpected hidden client timeout: %s", client.httpClient.Timeout)
	}
}

func TestClientReusesCallerCommandMetadata(t *testing.T) {
	t.Parallel()
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	ids := []identity.RunID{
		"run_00010203-0405-4607-8809-0a0b0c0d0e0f",
		"run_11111111-2222-4333-8444-555555555555",
		"run_66666666-7777-4888-8999-aaaaaaaaaaaa",
	}
	next := 0
	api, err := daemon.New(
		runstate.NewStore(clock.Fixed{
			Time: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
		}),
		registry,
		func() (identity.RunID, error) {
			id := ids[next]
			next++
			return id, nil
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.Handler())
	defer server.Close()
	client, err := NewClient(server.URL, server.Client(), registry)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := PrepareCommand("client-retry-0001")
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.CreateRunWithCommand(
		t.Context(),
		"Replay one logical client command",
		metadata,
	)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := client.CreateRunWithCommand(
		t.Context(),
		"Replay one logical client command",
		metadata,
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayed != first || replayed.RunID != ids[0].String() {
		t.Fatalf("replay = %#v, want %#v", replayed, first)
	}
	if _, err := client.CreateRunWithCommand(
		t.Context(),
		"Reuse the key for a different command",
		metadata,
	); err == nil {
		t.Fatal("client accepted an idempotency key with a different command")
	}
}

func TestPrepareCommandValidatesCallerKey(t *testing.T) {
	t.Parallel()
	metadata, err := PrepareCommand("")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.IdempotencyKey == "" ||
		metadata.CorrelationID != metadata.IdempotencyKey {
		t.Fatalf("unexpected generated metadata: %#v", metadata)
	}
	if _, err := PrepareCommand("short"); err == nil {
		t.Fatal("short caller key was accepted")
	}
	longKey := string(bytes.Repeat([]byte("x"), 200))
	longMetadata, err := PrepareCommand(longKey)
	if err != nil {
		t.Fatalf("valid 200-character key rejected: %v", err)
	}
	if longMetadata.IdempotencyKey != longKey ||
		len(longMetadata.CorrelationID) > 160 {
		t.Fatalf("long key metadata is invalid: %#v", longMetadata)
	}
}
