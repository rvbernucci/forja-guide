package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

func TestRunExecutesOneShotWorker(t *testing.T) {
	repository := t.TempDir()
	initializeGitRepository(t, repository)
	evidence := filepath.Join(repository, "evidence")
	if err := os.Mkdir(evidence, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repository, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(t.TempDir(), "fake-codex")
	script := `#!/bin/sh
set -eu
report=''
write_scope=''
while [ "$#" -gt 0 ]; do
  if [ "$1" = '--output-last-message' ]; then report="$2"; shift 2
  elif [ "$1" = '--add-dir' ] && [ -z "$write_scope" ]; then write_scope="$2"; shift 2
  else shift
  fi
done
test -n "$report"
test -n "$write_scope"
printf '%s\n' 'fake result' >"$write_scope/result.md"
cat >"$report" <<'JSON'
{"status":"completed","summary":"fake completed","changed_paths":["docs/result.md"],"evidence_refs":[],"risks":[]}
JSON
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":2,"cached_input_tokens":0,"output_tokens":1}}'
`
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	task := validCLITask(repository, evidence)
	taskData, _ := json.Marshal(task)
	taskPath := filepath.Join(t.TempDir(), "task.json")
	resultPath := filepath.Join(t.TempDir(), "result.json")
	if err := os.WriteFile(taskPath, taskData, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(
		context.Background(),
		[]string{"--task", taskPath, "--result", resultPath, "--codex", fake},
		&stdout,
		&stderr,
		[]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()},
	)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	var result contracts.WorkerResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || result.Report == nil || result.Usage.InputTokens != 2 {
		t.Fatalf("result=%#v", result)
	}
	if !bytes.Contains(stderr.Bytes(), []byte(`"kind":"worker.started"`)) {
		t.Fatalf("structured lifecycle events missing: %s", stderr.String())
	}
}

func initializeGitRepository(t *testing.T, repository string) {
	t.Helper()
	command := exec.Command("git", "-C", repository, "init", "--quiet")
	command.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize Git repository: %v: %s", err, output)
	}
}

func TestRunRejectsInvalidAndOversizedTasks(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"invalid", []byte(`{"objective":"missing contract"}`)},
		{"oversized", bytes.Repeat([]byte("x"), maximumTaskBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "task.json")
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), []string{"--task", path}, &stdout, &stderr, nil); code != 2 {
				t.Fatalf("exit=%d stderr=%s", code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("invalid task produced output: %s", stdout.String())
			}
		})
	}
}

func TestEnvironmentLookupUsesLastExactAssignment(t *testing.T) {
	t.Parallel()
	lookup := environmentLookup([]string{"A=first", "AB=wrong", "A=second", "invalid"})
	if value, ok := lookup("A"); !ok || value != "second" {
		t.Fatalf("A = %q, %v", value, ok)
	}
	if _, ok := lookup("missing"); ok {
		t.Fatal("missing environment key was invented")
	}
}

func validCLITask(repository, evidence string) contracts.WorkerTask {
	return contracts.WorkerTask{
		TaskID:            "task_00000000-0000-4000-8000-000000000001",
		AttemptID:         "attempt_00000000-0000-4000-8000-000000000002",
		RunID:             "run_00000000-0000-4000-8000-000000000003",
		SchemaVersion:     "1.0",
		Role:              "implementer",
		Objective:         "Execute a one-shot bounded worker",
		RepositoryPath:    repository,
		WorktreePath:      repository,
		ReadScopes:        []string{"."},
		WriteScopes:       []string{"docs"},
		ResultSchemaRef:   contracts.WorkerReportSchemaRef,
		EvidenceOutputDir: evidence,
		AttemptOrdinal:    1,
		Budgets: contracts.WorkerBudgets{
			// This is an integration fixture, not a timeout-behavior test. Leave
			// enough headroom for process startup under a concurrent full suite.
			WallClockMS:         15000,
			InactivityMS:        7500,
			MaxOutputBytes:      4096,
			CancellationGraceMS: 50,
			MaxRetries:          1,
		},
	}
}
