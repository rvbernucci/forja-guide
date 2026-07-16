package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
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
