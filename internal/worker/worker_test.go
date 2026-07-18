package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

type processAdapter struct {
	mode       string
	executable string
}

func (a processAdapter) Name() string { return "test-process" }

func (a processAdapter) Build(
	task contracts.WorkerTask,
	paths ExecutionPaths,
) (Invocation, error) {
	executable := a.executable
	if executable == "" {
		executable = os.Args[0]
	}
	return Invocation{
		Path: executable,
		Args: []string{
			"-test.run=^TestWorkerProcess$", "--", a.mode, paths.ReportPath,
		},
		Dir:   task.WorktreePath,
		Stdin: task.Objective,
	}, nil
}

func (processAdapter) ParseUsage(output []byte) contracts.WorkerUsage {
	return CodexAdapter{}.ParseUsage(output)
}

func (processAdapter) RetryableFailure(exitCode int, stderr string) bool {
	return CodexAdapter{}.RetryableFailure(exitCode, stderr)
}

type memoryEvents struct {
	mu     sync.Mutex
	events []Event
	failOn string
}

type blockingEvents struct {
	blockOn string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingEvents) Emit(_ context.Context, event Event) error {
	if event.Kind != s.blockOn {
		return nil
	}
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return nil
}

func (s *memoryEvents) Emit(_ context.Context, event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Kind == s.failOn {
		return errors.New("injected event sink failure")
	}
	s.events = append(s.events, event)
	return nil
}

func TestWorkerProcess(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+2 >= len(os.Args) {
		return
	}
	mode := os.Args[separator+1]
	reportPath := os.Args[separator+2]
	report := func(status string, changed []string) {
		if changed == nil {
			changed = []string{}
		}
		data, _ := json.Marshal(contracts.WorkerReport{
			Status:       status,
			Summary:      "bounded worker completed",
			ChangedPaths: changed,
			EvidenceRefs: []string{},
			Risks:        []string{},
		})
		_ = os.WriteFile(reportPath, data, 0o600)
	}
	switch mode {
	case "success":
		report("completed", []string{"docs/result.md"})
		fmt.Print("SECRET_OUTPUT\n")
		fmt.Print(`{"type":"turn.completed","usage":{"input_tokens":11,"cached_input_tokens":3,"output_tokens":5}}` + "\n")
		fmt.Print(`{"type":"item.completed","item":{"type":"command_execution"}}` + "\n")
	case "blocked":
		report("blocked", nil)
	case "invalid-report":
		_ = os.WriteFile(reportPath, []byte(`{"status":"completed"}`), 0o600)
	case "scope-escape":
		report("completed", []string{"secrets/result.md"})
	case "hidden-scope-escape":
		_ = os.MkdirAll("secrets", 0o700)
		_ = os.WriteFile("secrets/hidden.txt", []byte("omitted from report\n"), 0o600)
		report("completed", []string{"docs/result.md"})
	case "ignored-scope-escape":
		_ = os.MkdirAll("ignored", 0o700)
		_ = os.WriteFile("ignored/escape.txt", []byte("mutated outside scope\n"), 0o600)
		report("completed", []string{"docs/result.md"})
	case "poison-evidence-root":
		_ = os.RemoveAll("evidence")
		_ = os.Symlink(".", "evidence")
		report("completed", []string{"evidence"})
	case "invalid-evidence":
		data, _ := json.Marshal(contracts.WorkerReport{
			Status: "completed", Summary: "invalid evidence claim",
			ChangedPaths: []string{},
			EvidenceRefs: []string{"evidence/missing.txt#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			Risks:        []string{},
		})
		_ = os.WriteFile(reportPath, data, 0o600)
	case "valid-evidence":
		content := []byte("bounded evidence\n")
		_ = os.MkdirAll("evidence", 0o700)
		_ = os.WriteFile("evidence/proof.txt", content, 0o600)
		data, _ := json.Marshal(contracts.WorkerReport{
			Status: "completed", Summary: "valid evidence",
			ChangedPaths: []string{"evidence/proof.txt"},
			EvidenceRefs: []string{"evidence/proof.txt#sha256=" + digest(content)},
			Risks:        []string{},
		})
		_ = os.WriteFile(reportPath, data, 0o600)
	case "retryable":
		fmt.Fprint(os.Stderr, "temporarily unavailable: status 503")
		os.Exit(1)
	case "silent":
		time.Sleep(5 * time.Second)
	case "busy":
		for index := 0; index < 100; index++ {
			fmt.Print(".")
			time.Sleep(20 * time.Millisecond)
		}
	case "flood":
		fmt.Print(strings.Repeat("x", 8192))
	case "child":
		child := exec.Command("sleep", "30")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		fmt.Printf("CHILD_PID=%d\n", child.Process.Pid)
		_ = child.Wait()
	case "residual-child":
		child := exec.Command("sleep", "30")
		child.Stdout = nil
		child.Stderr = nil
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		fmt.Printf("CHILD_PID=%d\n", child.Process.Pid)
		report("completed", []string{})
	case "residual-child-nonzero":
		child := exec.Command("sleep", "30")
		child.Stdout = nil
		child.Stderr = nil
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		fmt.Printf("CHILD_PID=%d\n", child.Process.Pid)
		os.Exit(1)
	case "term-resistant-child":
		child := exec.Command(os.Args[0], "-test.run=^TestTermIgnoringChildProcess$")
		child.Env = append(os.Environ(), "FORJA_TERM_IGNORE_HELPER=1")
		child.Stdout = nil
		child.Stderr = nil
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		fmt.Printf("CHILD_PID=%d\n", child.Process.Pid)
		time.Sleep(100 * time.Millisecond)
		_ = child.Wait()
	case "invalid-utf8":
		report("completed", []string{})
		_, _ = os.Stdout.Write([]byte{'o', 'k', ':', 0xff, '\n'})
	}
}

func TestTermIgnoringChildProcess(t *testing.T) {
	if os.Getenv("FORJA_TERM_IGNORE_HELPER") != "1" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	time.Sleep(30 * time.Second)
}

func TestSanitizedEnvironmentDropsAuthorityAndSecrets(t *testing.T) {
	home := t.TempDir()
	environment, err := SanitizedEnvironment([]string{
		"PATH=/usr/bin", "HOME=/home/operator", "CODEX_HOME=/safe/codex",
		"FORJA_DATABASE_URL=postgres://secret", "DATABASE_URL=postgres://secret",
		"GITHUB_TOKEN=secret", "OPENAI_API_KEY=secret", "SSH_AUTH_SOCK=/secret",
		"LANG=en_US.UTF-8",
	}, home)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(environment, "\n")
	for _, forbidden := range []string{
		"FORJA_", "DATABASE_URL", "GITHUB_TOKEN", "OPENAI_API_KEY", "SSH_AUTH_SOCK",
		"postgres://secret",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("sanitized environment contains %q: %s", forbidden, joined)
		}
	}
	for _, required := range []string{
		"HOME=" + home, "CODEX_HOME=/safe/codex", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "PATH=/usr/bin",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("sanitized environment lacks %q: %s", required, joined)
		}
	}
}

func TestCodexAdapterBuildsArgumentSafeInvocation(t *testing.T) {
	task := workerTask(t)
	task.Objective = "fix docs; touch /tmp/escaped"
	model := "gpt-test"
	task.Model = &model
	invocation, err := (CodexAdapter{Executable: "/usr/bin/codex"}).Build(task, ExecutionPaths{
		HomeDir:          "/tmp/home",
		ReportPath:       "/tmp/report.json",
		ReportSchemaPath: "/tmp/schema.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Path != "/usr/bin/codex" || invocation.Dir != task.WorktreePath {
		t.Fatalf("unexpected invocation: %#v", invocation)
	}
	joined := strings.Join(invocation.Args, " ")
	for _, required := range []string{
		"--ignore-user-config", "--strict-config", `approval_policy="never"`,
		"sandbox_workspace_write.network_access=false", "--ephemeral",
		"--sandbox workspace-write", "--output-schema /tmp/schema.json",
		"--output-last-message /tmp/report.json", "--model gpt-test",
		`shell_environment_policy.inherit="all"`,
		"shell_environment_policy.ignore_default_excludes=false",
		`shell_environment_policy.include_only=["PATH","HOME","LANG","LC_ALL","TMPDIR","TERM","SSL_CERT_FILE","SSL_CERT_DIR"]`,
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("invocation lacks %q: %s", required, joined)
		}
	}
	if strings.Contains(joined, task.Objective) || invocation.Args[len(invocation.Args)-1] != "-" {
		t.Fatalf("objective escaped stdin boundary: %#v", invocation)
	}
	if !strings.Contains(invocation.Stdin, task.Objective) {
		t.Fatal("objective missing from stdin prompt")
	}
}

func TestSupervisorSuccessProducesValidatedResult(t *testing.T) {
	events := &memoryEvents{}
	result, err := executeTask(t, processAdapter{mode: "success"}, events, func(*contracts.WorkerTask) {})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || result.TerminationReason != "completed" || result.Report == nil {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Usage.InputTokens != 11 || result.Usage.OutputTokens != 5 || result.Usage.ToolCalls != 1 {
		t.Fatalf("usage = %#v", result.Usage)
	}
	encoded, _ := json.Marshal(events.events)
	if strings.Contains(string(encoded), "SECRET_OUTPUT") {
		t.Fatalf("structured events leaked raw output: %s", encoded)
	}
}

func TestSupervisorAcceptsHashVerifiedEvidence(t *testing.T) {
	result, err := executeTask(t, processAdapter{mode: "valid-evidence"}, nil, func(*contracts.WorkerTask) {})
	if err != nil || result.Status != "succeeded" || len(result.EvidenceRefs) != 1 {
		t.Fatalf("evidence result=%#v err=%v", result, err)
	}
}

func TestSupervisorClassifications(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		status    string
		reason    string
		retryable bool
		mutate    func(*contracts.WorkerTask)
	}{
		{"blocked", "blocked", "blocked", "worker_blocked", false, func(*contracts.WorkerTask) {}},
		{"invalid report", "invalid-report", "failed_retryable", "invalid_report", true, func(*contracts.WorkerTask) {}},
		{"scope escape", "scope-escape", "failed_retryable", "invalid_report", true, func(*contracts.WorkerTask) {}},
		{"hidden scope escape", "hidden-scope-escape", "failed_retryable", "invalid_report", true, func(*contracts.WorkerTask) {}},
		{"evidence root poisoning", "poison-evidence-root", "failed_retryable", "invalid_report", true, func(*contracts.WorkerTask) {}},
		{"invalid evidence", "invalid-evidence", "failed_retryable", "invalid_report", true, func(*contracts.WorkerTask) {}},
		{"retryable process", "retryable", "failed_retryable", "process_failure", true, func(*contracts.WorkerTask) {}},
		{"residual process", "residual-child", "failed_terminal", "process_failure", false, func(*contracts.WorkerTask) {}},
		{"inactivity", "silent", "failed_retryable", "inactivity_timeout", true, func(task *contracts.WorkerTask) {
			task.Budgets.InactivityMS = 120
		}},
		{"wall timeout", "busy", "failed_retryable", "wall_timeout", true, func(task *contracts.WorkerTask) {
			task.Budgets.WallClockMS = 150
			task.Budgets.InactivityMS = 500
		}},
		{"output limit", "flood", "failed_terminal", "output_limit", false, func(task *contracts.WorkerTask) {
			task.Budgets.MaxOutputBytes = 1024
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := executeTask(t, processAdapter{mode: test.mode}, nil, test.mutate)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != test.status || result.TerminationReason != test.reason || result.Retryable != test.retryable {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestSupervisorCancellationKillsProcessGroup(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("process-group assertion requires Unix")
	}
	repository := t.TempDir()
	mustInitGitRepository(t, repository)
	task := workerTaskAt(repository)
	supervisor, err := NewSupervisor(
		mustRegistry(t),
		processAdapter{mode: "child"},
		nil,
		[]string{"PATH=" + os.Getenv("PATH")},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 800*time.Millisecond)
	defer cancel()
	result, err := supervisor.Execute(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "cancelled" || result.TerminationReason != "cancelled" {
		t.Fatalf("result = %#v", result)
	}
	marker := "CHILD_PID="
	index := strings.Index(result.Stdout, marker)
	if index < 0 {
		t.Fatalf("child PID missing from output: %q", result.Stdout)
	}
	pidText := strings.TrimSpace(strings.Split(result.Stdout[index+len(marker):], "\n")[0])
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child process %d survived process-group cancellation", pid)
}

func TestSupervisorCancellationKillsTermIgnoringDescendant(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("process-group assertion requires Unix")
	}
	repository := t.TempDir()
	mustInitGitRepository(t, repository)
	task := workerTaskAt(repository)
	task.Budgets.CancellationGraceMS = 100
	supervisor, err := NewSupervisor(
		mustRegistry(t),
		processAdapter{mode: "term-resistant-child"},
		nil,
		[]string{"PATH=" + os.Getenv("PATH")},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	result, err := supervisor.Execute(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "cancelled" || result.TerminationReason != "cancelled" {
		t.Fatalf("result = %#v", result)
	}
	pid := childPIDFromOutput(t, result.Stdout)
	assertProcessGone(t, pid)
}

func TestSupervisorNonzeroExitKillsResidualDescendant(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("process-group assertion requires Unix")
	}
	result, err := executeTask(t, processAdapter{mode: "residual-child-nonzero"}, nil, func(task *contracts.WorkerTask) {
		task.Budgets.CancellationGraceMS = 100
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed_retryable" || result.TerminationReason != "process_failure" {
		t.Fatalf("result = %#v", result)
	}
	assertProcessGone(t, childPIDFromOutput(t, result.Stdout))
}

func TestSupervisorHashesCanonicalUTF8Output(t *testing.T) {
	result, err := executeTask(t, processAdapter{mode: "invalid-utf8"}, nil, func(*contracts.WorkerTask) {})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Stdout, "ok:\ufffd") {
		t.Fatalf("stdout was not canonicalized: %q", result.Stdout)
	}
	if result.StdoutSHA256 != digest([]byte(result.Stdout)) {
		t.Fatalf("stdout digest does not represent persisted UTF-8")
	}
}

func childPIDFromOutput(t *testing.T, output string) int {
	t.Helper()
	marker := "CHILD_PID="
	index := strings.Index(output, marker)
	if index < 0 {
		t.Fatalf("child PID missing from output: %q", output)
	}
	pidText := strings.TrimSpace(strings.Split(output[index+len(marker):], "\n")[0])
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child process %d survived process-group cancellation", pid)
}

func TestSupervisorRejectsRetryAndTokenBudgets(t *testing.T) {
	t.Run("attempt ordinal", func(t *testing.T) {
		result, err := executeTask(t, processAdapter{mode: "success"}, nil, func(task *contracts.WorkerTask) {
			task.AttemptOrdinal = 3
			task.Budgets.MaxRetries = 1
		})
		if err != nil || result.TerminationReason != "budget_rejected" {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})
	t.Run("token usage", func(t *testing.T) {
		result, err := executeTask(t, processAdapter{mode: "success"}, nil, func(task *contracts.WorkerTask) {
			limit := 10
			task.Budgets.MaxTokens = &limit
		})
		if err != nil || result.TerminationReason != "budget_rejected" || result.Report != nil {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})
}

func TestSupervisorStartAndEventFailuresFailClosed(t *testing.T) {
	t.Run("start failure", func(t *testing.T) {
		result, err := executeTask(t, processAdapter{mode: "success", executable: "/does/not/exist"}, nil, func(*contracts.WorkerTask) {})
		if err != nil || result.TerminationReason != "start_failure" || !result.Retryable {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})
	t.Run("event failure", func(t *testing.T) {
		_, err := executeTask(t, processAdapter{mode: "success"}, &memoryEvents{failOn: "worker.starting"}, func(*contracts.WorkerTask) {})
		if err == nil || !strings.Contains(err.Error(), "injected event sink failure") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("output telemetry failure", func(t *testing.T) {
		result, err := executeTask(t, processAdapter{mode: "busy"}, &memoryEvents{failOn: "worker.output"}, func(*contracts.WorkerTask) {})
		if err == nil || result.Status != "failed_retryable" || result.TerminationReason != "telemetry_failure" {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})
}

func TestSupervisorTelemetryCannotBlockRuntimeBudgets(t *testing.T) {
	events := &blockingEvents{
		blockOn: "worker.output",
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(events.release)
	started := time.Now()
	result, err := executeTask(t, processAdapter{mode: "busy"}, events, func(task *contracts.WorkerTask) {
		task.Budgets.WallClockMS = 150
		task.Budgets.InactivityMS = 500
	})
	if err != nil || result.TerminationReason != "wall_timeout" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	select {
	case <-events.entered:
	default:
		t.Fatal("blocking telemetry sink was not exercised")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("blocked telemetry bypassed wall budget: %v", elapsed)
	}
}

func TestStartedTelemetryCannotOutliveWallBudget(t *testing.T) {
	events := &blockingEvents{
		blockOn: "worker.started",
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(events.release)
	started := time.Now()
	result, err := executeTask(t, processAdapter{mode: "silent"}, events, func(task *contracts.WorkerTask) {
		task.Budgets.WallClockMS = 120
		task.Budgets.InactivityMS = 500
		task.Budgets.CancellationGraceMS = 30
	})
	if err == nil || result.TaskID != "" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	select {
	case <-events.entered:
	default:
		t.Fatal("blocking started telemetry was not exercised")
	}
	if elapsed := time.Since(started); elapsed > 400*time.Millisecond {
		t.Fatalf("started telemetry exceeded wall boundary: %v", elapsed)
	}
}

func TestSupervisorRequiresDeclaredRepositoryBinding(t *testing.T) {
	repository := t.TempDir()
	worktree := t.TempDir()
	mustInitGitRepository(t, repository)
	mustInitGitRepository(t, worktree)
	task := workerTaskAt(worktree)
	task.RepositoryPath = repository
	supervisor, err := NewSupervisor(
		mustRegistry(t),
		processAdapter{mode: "success"},
		nil,
		[]string{"PATH=" + os.Getenv("PATH")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Execute(t.Context(), task); err == nil ||
		!strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("unrelated worktree error=%v", err)
	}
}

func TestSupervisorRejectsUnsupportedReadScope(t *testing.T) {
	_, err := executeTask(t, processAdapter{mode: "success"}, nil, func(task *contracts.WorkerTask) {
		task.ReadScopes = []string{"docs"}
	})
	if err == nil || !strings.Contains(err.Error(), "full-worktree read scope") {
		t.Fatalf("read scope error=%v", err)
	}
}

func TestSupervisorRejectsEvidenceAtWorktreeRoot(t *testing.T) {
	_, err := executeTask(t, processAdapter{mode: "success"}, nil, func(task *contracts.WorkerTask) {
		task.EvidenceOutputDir = task.WorktreePath
	})
	if err == nil || !strings.Contains(err.Error(), "proper worktree descendant") {
		t.Fatalf("evidence root error=%v", err)
	}
}

func TestSupervisorRejectsEvidenceSymlinkResolvingToWorktreeRoot(t *testing.T) {
	repository := t.TempDir()
	mustInitGitRepository(t, repository)
	link := filepath.Join(repository, "evidence-root-link")
	if err := os.Symlink(".", link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	task := workerTaskAt(repository)
	task.EvidenceOutputDir = link
	supervisor, err := NewSupervisor(
		mustRegistry(t),
		processAdapter{mode: "success"},
		nil,
		[]string{"PATH=" + os.Getenv("PATH")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Execute(t.Context(), task); err == nil ||
		!strings.Contains(err.Error(), "proper worktree descendant") {
		t.Fatalf("root-equivalent evidence symlink error=%v", err)
	}
}

func TestSupervisorRejectsHiddenGitIndexFlags(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			repository := t.TempDir()
			mustInitGitRepository(t, repository)
			runGit(t, repository, "config", "user.name", "Forja Test")
			runGit(t, repository, "config", "user.email", "forja@example.invalid")
			if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("tracked\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runGit(t, repository, "add", "tracked.txt")
			runGit(t, repository, "commit", "--quiet", "-m", "tracked fixture")
			runGit(t, repository, "update-index", flag, "tracked.txt")
			_, err := executePreparedTask(t, workerTaskAt(repository), processAdapter{mode: "success"})
			if err == nil || !strings.Contains(err.Error(), "unsupported hidden path flags") {
				t.Fatalf("hidden index flag error=%v", err)
			}
		})
	}
}

func TestSupervisorRejectsIgnoredOutOfScopeMutation(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "create"
		if existing {
			name = "modify"
		}
		t.Run(name, func(t *testing.T) {
			repository := t.TempDir()
			mustInitGitRepository(t, repository)
			runGit(t, repository, "config", "user.name", "Forja Test")
			runGit(t, repository, "config", "user.email", "forja@example.invalid")
			if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte("ignored/\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runGit(t, repository, "add", ".gitignore")
			runGit(t, repository, "commit", "--quiet", "-m", "ignore test fixture")
			if existing {
				if err := os.MkdirAll(filepath.Join(repository, "ignored"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repository, "ignored", "escape.txt"), []byte("initial\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := executePreparedTask(t, workerTaskAt(repository), processAdapter{mode: "ignored-scope-escape"})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "failed_retryable" || result.TerminationReason != "invalid_report" {
				t.Fatalf("ignored scope escape result=%#v", result)
			}
		})
	}
}

func TestSupervisorRejectsEvidenceSymlinkBeforeMutation(t *testing.T) {
	repository := t.TempDir()
	outside := t.TempDir()
	mustInitGitRepository(t, repository)
	link := filepath.Join(repository, "external")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	task := workerTaskAt(repository)
	task.EvidenceOutputDir = filepath.Join(link, "must-not-exist")
	supervisor, err := NewSupervisor(
		mustRegistry(t),
		processAdapter{mode: "success"},
		nil,
		[]string{"PATH=" + os.Getenv("PATH")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Execute(t.Context(), task); err == nil ||
		!strings.Contains(err.Error(), "ancestor escapes") {
		t.Fatalf("evidence symlink error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "must-not-exist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("validation mutated external path: %v", err)
	}
}

func TestSupervisorAcceptsLinkedGitWorktree(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	mustInitGitRepository(t, repository)
	runGit(t, repository, "config", "user.name", "Forja Test")
	runGit(t, repository, "config", "user.email", "forja@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "seed.txt"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "seed.txt")
	runGit(t, repository, "commit", "--quiet", "-m", "seed")
	runGit(t, repository, "worktree", "add", "--quiet", "--detach", worktree, "HEAD")

	task := workerTaskAt(worktree)
	task.RepositoryPath = repository
	result, err := executePreparedTask(t, task, processAdapter{mode: "success"})
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("linked worktree result=%#v err=%v", result, err)
	}
}

func executeTask(
	t *testing.T,
	adapter Adapter,
	events EventSink,
	mutate func(*contracts.WorkerTask),
) (contracts.WorkerResult, error) {
	t.Helper()
	repository := t.TempDir()
	mustInitGitRepository(t, repository)
	task := workerTaskAt(repository)
	mutate(&task)
	return executePreparedTaskWithEvents(t, task, adapter, events)
}

func executePreparedTask(
	t *testing.T,
	task contracts.WorkerTask,
	adapter Adapter,
) (contracts.WorkerResult, error) {
	t.Helper()
	return executePreparedTaskWithEvents(t, task, adapter, nil)
}

func executePreparedTaskWithEvents(
	t *testing.T,
	task contracts.WorkerTask,
	adapter Adapter,
	events EventSink,
) (contracts.WorkerResult, error) {
	t.Helper()
	supervisor, err := NewSupervisor(
		mustRegistry(t),
		adapter,
		events,
		[]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()},
	)
	if err != nil {
		t.Fatal(err)
	}
	return supervisor.Execute(t.Context(), task)
}

func mustInitGitRepository(t *testing.T, path string) {
	t.Helper()
	runGit(t, path, "init", "--quiet")
}

func runGit(t *testing.T, path string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", path}, arguments...)...)
	command.Env = gitInspectionEnvironment(os.Environ())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
}

func workerTask(t *testing.T) contracts.WorkerTask {
	t.Helper()
	return workerTaskAt(t.TempDir())
}

func workerTaskAt(repository string) contracts.WorkerTask {
	return contracts.WorkerTask{
		TaskID:            "task_00000000-0000-4000-8000-000000000001",
		AttemptID:         "attempt_00000000-0000-4000-8000-000000000002",
		RunID:             "run_00000000-0000-4000-8000-000000000003",
		SchemaVersion:     "1.0",
		Role:              "implementer",
		Objective:         "Produce one bounded test result",
		RepositoryPath:    repository,
		WorktreePath:      repository,
		ReadScopes:        []string{"."},
		WriteScopes:       []string{"docs"},
		ResultSchemaRef:   contracts.WorkerReportSchemaRef,
		EvidenceOutputDir: filepath.Join(repository, "evidence"),
		AttemptOrdinal:    1,
		Budgets: contracts.WorkerBudgets{
			WallClockMS:         4000,
			InactivityMS:        1500,
			MaxOutputBytes:      16384,
			CancellationGraceMS: 50,
			MaxRetries:          2,
		},
	}
}

func mustRegistry(t *testing.T) *contracts.Registry {
	t.Helper()
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
