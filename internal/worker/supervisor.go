package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	publicschemas "github.com/rvbernucci/forja-guide/schemas"
)

const (
	workerSchemaVersion = "1.0"
	eventEmitTimeout    = 500 * time.Millisecond
)

var evidenceReferencePattern = regexp.MustCompile(`^([A-Za-z0-9._/-]+)#sha256=([0-9a-f]{64})$`)

// Supervisor owns process authority, runtime budgets, and result validation.
type Supervisor struct {
	registry *contracts.Registry
	adapter  Adapter
	events   EventSink
	environ  []string
	now      func() time.Time
}

// NewSupervisor creates one bounded worker supervisor.
func NewSupervisor(
	registry *contracts.Registry,
	adapter Adapter,
	events EventSink,
	environ []string,
) (*Supervisor, error) {
	if registry == nil {
		return nil, fmt.Errorf("worker supervisor requires a contract registry")
	}
	if adapter == nil {
		return nil, fmt.Errorf("worker supervisor requires an adapter")
	}
	if events == nil {
		events = discardEvents{}
	}
	if environ == nil {
		environ = os.Environ()
	}
	return &Supervisor{
		registry: registry,
		adapter:  adapter,
		events:   events,
		environ:  append([]string(nil), environ...),
		now:      time.Now,
	}, nil
}

// Execute runs one canonical task in an isolated process group.
func (s *Supervisor) Execute(
	ctx context.Context,
	task contracts.WorkerTask,
) (contracts.WorkerResult, error) {
	if err := s.validateTask(task); err != nil {
		return contracts.WorkerResult{}, err
	}
	started := s.now().UTC()
	if task.AttemptOrdinal > task.Budgets.MaxRetries+1 {
		result := s.emptyResult(task, started, "failed_terminal", false, "budget_rejected")
		return result, s.validateResult(result)
	}

	home, err := os.MkdirTemp("", "forja-worker-home-")
	if err != nil {
		return contracts.WorkerResult{}, fmt.Errorf("create worker home: %w", err)
	}
	defer os.RemoveAll(home)
	if err := prepareWorkerHome(home); err != nil {
		return contracts.WorkerResult{}, err
	}
	if err := validateEvidenceDirectory(task.EvidenceOutputDir, task.WorktreePath); err != nil {
		return contracts.WorkerResult{}, err
	}
	if err := validateWriteScopeDirectories(task); err != nil {
		return contracts.WorkerResult{}, err
	}
	if changed, err := gitWorktreeChanges(task.WorktreePath); err != nil {
		return contracts.WorkerResult{}, err
	} else if len(changed) != 0 {
		return contracts.WorkerResult{}, fmt.Errorf("worker worktree must be clean before launch")
	}
	if err := validateGitIndexFlags(task.WorktreePath); err != nil {
		return contracts.WorkerResult{}, err
	}
	ignoredBefore, err := ignoredWorktreeSnapshot(task.WorktreePath)
	if err != nil {
		return contracts.WorkerResult{}, err
	}
	// The report stays outside the model-writable worktree so the worker cannot
	// replace the supervisor-owned target with a symlink during execution.
	reportPath := filepath.Join(home, "worker-report.json")
	schemaPath := filepath.Join(home, "worker-report.schema.json")
	schema, err := publicschemas.FS.ReadFile("worker-report.schema.json")
	if err != nil {
		return contracts.WorkerResult{}, fmt.Errorf("read embedded worker report schema: %w", err)
	}
	if err := os.WriteFile(schemaPath, schema, 0o600); err != nil {
		return contracts.WorkerResult{}, fmt.Errorf("write worker report schema: %w", err)
	}

	invocation, err := s.adapter.Build(task, ExecutionPaths{
		HomeDir:          home,
		ReportPath:       reportPath,
		ReportSchemaPath: schemaPath,
	})
	if err != nil {
		return contracts.WorkerResult{}, fmt.Errorf("build %s invocation: %w", s.adapter.Name(), err)
	}
	if err := validateInvocation(invocation, task.WorktreePath); err != nil {
		return contracts.WorkerResult{}, err
	}
	environment, err := SanitizedEnvironment(s.environ, home)
	if err != nil {
		return contracts.WorkerResult{}, err
	}

	activity := make(chan Event, 128)
	budget := newOutputBudget(task.Budgets.MaxOutputBytes, func(stream string, size int) {
		event := s.event(task, "worker.output")
		event.Stream = stream
		event.Bytes = size
		select {
		case activity <- event:
		default:
		}
	})
	stdout := &boundedStream{budget: budget, name: "stdout"}
	stderr := &boundedStream{budget: budget, name: "stderr"}
	command := exec.Command(invocation.Path, invocation.Args...)
	command.Dir = invocation.Dir
	command.Env = environment
	command.Stdin = strings.NewReader(invocation.Stdin)
	command.Stdout = stdout
	command.Stderr = stderr
	configureProcess(command)

	if err := s.emitEvent(ctx, s.event(task, "worker.starting")); err != nil {
		return contracts.WorkerResult{}, fmt.Errorf("emit worker.starting: %w", err)
	}
	if err := command.Start(); err != nil {
		result := s.emptyResult(task, started, "failed_retryable", true, "start_failure")
		result.Stderr = cleanUTF8([]byte(err.Error()))
		result.StderrSHA256 = digest([]byte(result.Stderr))
		return result, errors.Join(s.validateResult(result), s.emitFinished(ctx, task, result))
	}
	processStarted := time.Now()
	budget.touch()
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	startEventBudget := time.Duration(task.Budgets.WallClockMS)*time.Millisecond - time.Since(processStarted)
	if startEventBudget > eventEmitTimeout {
		startEventBudget = eventEmitTimeout
	}
	if startEventBudget <= 0 {
		waitErr := terminateAndWait(command, waited, task.Budgets.CancellationGraceMS)
		result := s.resultFromExecution(
			task, started, "wall_timeout", waitErr, stdout.Bytes(), stderr.Bytes(),
		)
		s.applyUsageBudgets(task, &result)
		contaminationErr := s.classifyWorktreeContamination(task, ignoredBefore, &result)
		return result, errors.Join(
			fmt.Errorf("worker wall budget elapsed before start telemetry"),
			contaminationErr,
			s.validateResult(result),
			s.emitFinished(context.WithoutCancel(ctx), task, result),
		)
	}
	if err := s.emitEventWithin(ctx, s.event(task, "worker.started"), startEventBudget); err != nil {
		waitErr := terminateAndWait(command, waited, task.Budgets.CancellationGraceMS)
		result := s.resultFromExecution(
			task, started, "telemetry_failure", waitErr, stdout.Bytes(), stderr.Bytes(),
		)
		s.applyUsageBudgets(task, &result)
		contaminationErr := s.classifyWorktreeContamination(task, ignoredBefore, &result)
		return result, errors.Join(
			fmt.Errorf("emit worker.started: %w", err),
			contaminationErr,
			s.validateResult(result),
			s.emitFinished(context.WithoutCancel(ctx), task, result),
		)
	}
	reason, waitErr, eventErr := s.monitor(
		ctx, task, command, budget, activity, waited, processStarted,
	)
	if reason == "completed" {
		if residual, cleanupErr := cleanupResidualProcessGroup(
			command.Process,
			time.Duration(task.Budgets.CancellationGraceMS)*time.Millisecond,
		); residual {
			waitErr = errors.Join(waitErr, fmt.Errorf("worker left a residual process group"), cleanupErr)
		}
	}
	if budget.total.Load() > int64(task.Budgets.MaxOutputBytes) {
		reason = "output_limit"
	}
	stdoutData := stdout.Bytes()
	stderrData := stderr.Bytes()
	result := s.resultFromExecution(task, started, reason, waitErr, stdoutData, stderrData)
	if reason == "completed" {
		s.applyReport(task, reportPath, ignoredBefore, &result)
	}
	s.applyUsageBudgets(task, &result)
	contaminationErr := s.classifyWorktreeContamination(task, ignoredBefore, &result)
	validationErr := s.validateResult(result)
	finishErr := s.emitFinished(context.WithoutCancel(ctx), task, result)
	return result, errors.Join(eventErr, contaminationErr, validationErr, finishErr)
}

func (s *Supervisor) monitor(
	ctx context.Context,
	task contracts.WorkerTask,
	command *exec.Cmd,
	budget *outputBudget,
	activity <-chan Event,
	waited <-chan error,
	processStarted time.Time,
) (string, error, error) {
	wallRemaining := time.Duration(task.Budgets.WallClockMS)*time.Millisecond - time.Since(processStarted)
	if wallRemaining <= 0 {
		waitErr := terminateAndWait(command, waited, task.Budgets.CancellationGraceMS)
		return "wall_timeout", waitErr, nil
	}
	wall := time.NewTimer(wallRemaining)
	defer wall.Stop()
	interval := time.Duration(task.Budgets.InactivityMS) * time.Millisecond / 4
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	emitContext, cancelEmits := context.WithCancel(ctx)
	defer cancelEmits()
	var eventResult <-chan error
	for {
		activityInput := activity
		if eventResult != nil {
			activityInput = nil
		}
		select {
		case waitErr := <-waited:
			return "completed", waitErr, nil
		case <-ctx.Done():
			waitErr := terminateAndWait(command, waited, task.Budgets.CancellationGraceMS)
			return "cancelled", waitErr, nil
		case <-wall.C:
			waitErr := terminateAndWait(command, waited, task.Budgets.CancellationGraceMS)
			return "wall_timeout", waitErr, nil
		case <-budget.exceeded:
			waitErr := terminateAndWait(command, waited, task.Budgets.CancellationGraceMS)
			return "output_limit", waitErr, nil
		case now := <-ticker.C:
			if budget.inactiveFor(now) >= time.Duration(task.Budgets.InactivityMS)*time.Millisecond {
				waitErr := terminateAndWait(command, waited, task.Budgets.CancellationGraceMS)
				return "inactivity_timeout", waitErr, nil
			}
		case event := <-activityInput:
			completed := make(chan error, 1)
			go func() { completed <- s.emitEvent(emitContext, event) }()
			eventResult = completed
		case err := <-eventResult:
			eventResult = nil
			if err != nil {
				waitErr := terminateAndWait(command, waited, task.Budgets.CancellationGraceMS)
				return "telemetry_failure", waitErr, fmt.Errorf("emit worker output event: %w", err)
			}
		}
	}
}

func terminateAndWait(command *exec.Cmd, waited <-chan error, graceMS int) error {
	_ = signalProcessTree(command.Process, syscall.SIGTERM)
	grace := time.Duration(graceMS) * time.Millisecond
	timer := time.NewTimer(grace)
	defer timer.Stop()
	var waitErr error
	select {
	case err := <-waited:
		waitErr = err
	case <-timer.C:
		_ = signalProcessTree(command.Process, syscall.SIGKILL)
		waitErr = boundedWait(waited, grace)
	}
	_, cleanupErr := cleanupResidualProcessGroup(command.Process, grace)
	return errors.Join(waitErr, cleanupErr)
}

func boundedWait(waited <-chan error, limit time.Duration) error {
	if limit <= 0 {
		limit = 10 * time.Millisecond
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case err := <-waited:
		return err
	case <-timer.C:
		return fmt.Errorf("worker process did not exit within the bounded reap deadline")
	}
}

func cleanupResidualProcessGroup(process *os.Process, grace time.Duration) (bool, error) {
	if err := signalProcessTree(process, 0); errors.Is(err, syscall.ESRCH) {
		return false, nil
	} else if err != nil {
		return true, fmt.Errorf("inspect residual worker process group: %w", err)
	}
	_ = signalProcessTree(process, syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if err := signalProcessTree(process, 0); errors.Is(err, syscall.ESRCH) {
			return true, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := signalProcessTree(process, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return true, fmt.Errorf("kill residual worker process group: %w", err)
	}
	deadline = time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if err := signalProcessTree(process, 0); errors.Is(err, syscall.ESRCH) {
			return true, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := signalProcessTree(process, 0); errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	return true, fmt.Errorf("worker process group survived SIGKILL beyond the bounded cleanup deadline")
}

func (s *Supervisor) resultFromExecution(
	task contracts.WorkerTask,
	started time.Time,
	reason string,
	waitErr error,
	stdout []byte,
	stderr []byte,
) contracts.WorkerResult {
	result := s.emptyResult(task, started, "failed_terminal", false, reason)
	result.Stdout = cleanUTF8(stdout)
	result.Stderr = cleanUTF8(stderr)
	result.StdoutSHA256 = digest([]byte(result.Stdout))
	result.StderrSHA256 = digest([]byte(result.Stderr))
	result.Usage = s.adapter.ParseUsage(stdout)
	result.ExitCode = processExitCode(waitErr)
	switch reason {
	case "cancelled":
		result.Status = "cancelled"
	case "wall_timeout", "inactivity_timeout", "telemetry_failure":
		result.Status = "failed_retryable"
		result.Retryable = true
	case "output_limit":
		result.Status = "failed_terminal"
	case "completed":
		if waitErr == nil {
			result.Status = "succeeded"
			result.TerminationReason = "completed"
		} else {
			result.Status = "failed_terminal"
			result.TerminationReason = "process_failure"
			if result.ExitCode != nil {
				result.Retryable = s.adapter.RetryableFailure(*result.ExitCode, result.Stderr)
			}
			if result.Retryable {
				result.Status = "failed_retryable"
			}
		}
	}
	return result
}

func (s *Supervisor) applyReport(
	task contracts.WorkerTask,
	path string,
	ignoredBefore map[string]string,
	result *contracts.WorkerResult,
) {
	if result.Status != "succeeded" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		result.Status = "failed_retryable"
		result.Retryable = true
		result.TerminationReason = "invalid_report"
		return
	}
	report, err := contracts.DecodeStrict[contracts.WorkerReport](
		s.registry,
		"worker-report.schema.json",
		data,
	)
	actualPaths, actualErr := gitWorktreeChanges(task.WorktreePath)
	ignoredAfter, ignoredErr := ignoredWorktreeSnapshot(task.WorktreePath)
	if ignoredErr == nil {
		actualPaths = mergeChangedPaths(
			actualPaths,
			changedSnapshotPaths(ignoredBefore, ignoredAfter),
		)
	}
	allowedScopes := effectiveWriteScopes(task)
	evidenceDirectoryErr := validateEvidenceDirectory(task.EvidenceOutputDir, task.WorktreePath)
	if err != nil || actualErr != nil || ignoredErr != nil || evidenceDirectoryErr != nil ||
		!pathsWithinScopes(report.ChangedPaths, allowedScopes) ||
		!pathsWithinScopes(actualPaths, allowedScopes) ||
		!samePaths(actualPaths, report.ChangedPaths) ||
		!validEvidenceReferences(task, report.EvidenceRefs, actualPaths) {
		result.Status = "failed_retryable"
		result.Retryable = true
		result.TerminationReason = "invalid_report"
		return
	}
	result.Report = &report
	result.EvidenceRefs = append([]string{}, report.EvidenceRefs...)
	if report.Status == "blocked" {
		result.Status = "blocked"
		result.Retryable = false
		result.TerminationReason = "worker_blocked"
	}
}

func effectiveWriteScopes(task contracts.WorkerTask) []string {
	scopes := append([]string{}, task.WriteScopes...)
	evidenceScope, err := filepath.Rel(task.WorktreePath, task.EvidenceOutputDir)
	if err == nil && validScope(evidenceScope) {
		scopes = append(scopes, evidenceScope)
	}
	return scopes
}

func samePaths(actual []string, reported []string) bool {
	if len(actual) != len(reported) {
		return false
	}
	known := make(map[string]struct{}, len(actual))
	for _, path := range actual {
		known[path] = struct{}{}
	}
	for _, path := range reported {
		if _, exists := known[path]; !exists {
			return false
		}
	}
	return true
}

func (s *Supervisor) classifyWorktreeContamination(
	task contracts.WorkerTask,
	ignoredBefore map[string]string,
	result *contracts.WorkerResult,
) error {
	if result.Status == "succeeded" {
		return nil
	}
	actualPaths, actualErr := gitWorktreeChanges(task.WorktreePath)
	ignoredAfter, ignoredErr := ignoredWorktreeSnapshot(task.WorktreePath)
	if actualErr == nil && ignoredErr == nil {
		actualPaths = mergeChangedPaths(
			actualPaths,
			changedSnapshotPaths(ignoredBefore, ignoredAfter),
		)
		if len(actualPaths) == 0 {
			return nil
		}
	}
	result.Status = "failed_terminal"
	result.Retryable = false
	result.TerminationReason = "worktree_contaminated"
	result.Report = nil
	result.EvidenceRefs = []string{}
	if actualErr != nil || ignoredErr != nil {
		return fmt.Errorf(
			"verify failed worker worktree cleanliness: %w",
			errors.Join(actualErr, ignoredErr),
		)
	}
	return nil
}

func (s *Supervisor) applyUsageBudgets(
	task contracts.WorkerTask,
	result *contracts.WorkerResult,
) {
	if task.Budgets.MaxTokens != nil &&
		result.Usage.InputTokens+result.Usage.OutputTokens > *task.Budgets.MaxTokens {
		result.Status = "failed_terminal"
		result.Retryable = false
		result.TerminationReason = "budget_rejected"
		result.Report = nil
		result.EvidenceRefs = []string{}
	}
	if task.Budgets.MaxCommands != nil && result.Usage.ToolCalls > *task.Budgets.MaxCommands {
		result.Status = "failed_terminal"
		result.Retryable = false
		result.TerminationReason = "budget_rejected"
		result.Report = nil
		result.EvidenceRefs = []string{}
	}
}

func (s *Supervisor) emptyResult(
	task contracts.WorkerTask,
	started time.Time,
	status string,
	retryable bool,
	reason string,
) contracts.WorkerResult {
	finished := s.now().UTC()
	if finished.Before(started) {
		finished = started
	}
	return contracts.WorkerResult{
		TaskID:            task.TaskID,
		AttemptID:         task.AttemptID,
		RunID:             task.RunID,
		SchemaVersion:     workerSchemaVersion,
		Adapter:           s.adapter.Name(),
		Status:            status,
		Retryable:         retryable,
		TerminationReason: reason,
		StartedAt:         started,
		FinishedAt:        finished,
		DurationMS:        max(0, finished.Sub(started).Milliseconds()),
		Stdout:            "",
		Stderr:            "",
		StdoutSHA256:      digest(nil),
		StderrSHA256:      digest(nil),
		Usage:             contracts.WorkerUsage{},
		EvidenceRefs:      []string{},
	}
}

func (s *Supervisor) event(task contracts.WorkerTask, kind string) Event {
	return Event{
		Kind:       kind,
		TaskID:     task.TaskID,
		AttemptID:  task.AttemptID,
		Adapter:    s.adapter.Name(),
		OccurredAt: s.now().UTC(),
	}
}

func (s *Supervisor) emitFinished(
	ctx context.Context,
	task contracts.WorkerTask,
	result contracts.WorkerResult,
) error {
	event := s.event(task, "worker.finished")
	event.Reason = result.TerminationReason
	event.ExitCode = result.ExitCode
	return s.emitEvent(ctx, event)
}

func (s *Supervisor) emitEvent(ctx context.Context, event Event) error {
	return s.emitEventWithin(ctx, event, eventEmitTimeout)
}

func (s *Supervisor) emitEventWithin(ctx context.Context, event Event, limit time.Duration) error {
	emitContext, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	completed := make(chan error, 1)
	go func() { completed <- s.events.Emit(emitContext, event) }()
	select {
	case err := <-completed:
		return err
	case <-emitContext.Done():
		return fmt.Errorf("event delivery deadline: %w", emitContext.Err())
	}
}

func (s *Supervisor) validateTask(task contracts.WorkerTask) error {
	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("encode worker task: %w", err)
	}
	if err := s.registry.ValidateJSON("worker-task.schema.json", data); err != nil {
		return err
	}
	if task.ResultSchemaRef != contracts.WorkerReportSchemaRef {
		return fmt.Errorf("unsupported worker result schema")
	}
	if err := validateAbsoluteDirectory(task.RepositoryPath, "repository"); err != nil {
		return err
	}
	if err := validateAbsoluteDirectory(task.WorktreePath, "worktree"); err != nil {
		return err
	}
	if err := validateGitWorktreeBinding(task.RepositoryPath, task.WorktreePath); err != nil {
		return err
	}
	if !pathWithin(task.EvidenceOutputDir, task.WorktreePath) {
		return fmt.Errorf("evidence output directory must be inside the worktree")
	}
	if filepath.Clean(task.EvidenceOutputDir) == filepath.Clean(task.WorktreePath) {
		return fmt.Errorf("evidence output directory must be a proper worktree descendant")
	}
	if len(task.ReadScopes) != 1 || task.ReadScopes[0] != "." {
		return fmt.Errorf("Sprint 04 supports only full-worktree read scope")
	}
	for _, scope := range append(append([]string{}, task.ReadScopes...), task.WriteScopes...) {
		if !validScope(scope) {
			return fmt.Errorf("invalid worker scope %q", scope)
		}
	}
	return nil
}

func validEvidenceReferences(
	task contracts.WorkerTask,
	references []string,
	actualPaths []string,
) bool {
	resolvedEvidenceRoot, err := filepath.EvalSymlinks(task.EvidenceOutputDir)
	if err != nil {
		return false
	}
	changed := make(map[string]struct{}, len(actualPaths))
	for _, path := range actualPaths {
		changed[path] = struct{}{}
	}
	for _, reference := range references {
		matches := evidenceReferencePattern.FindStringSubmatch(reference)
		if len(matches) != 3 || !validScope(matches[1]) || matches[1] == "." {
			return false
		}
		if _, ok := changed[matches[1]]; !ok {
			return false
		}
		fullPath := filepath.Join(task.WorktreePath, filepath.FromSlash(matches[1]))
		if !pathWithin(fullPath, task.EvidenceOutputDir) {
			return false
		}
		resolved, err := filepath.EvalSymlinks(fullPath)
		if err != nil || !pathWithin(resolved, resolvedEvidenceRoot) {
			return false
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > 64<<20 {
			return false
		}
		data, err := os.ReadFile(resolved)
		if err != nil || digest(data) != matches[2] {
			return false
		}
	}
	return true
}

func validateWriteScopeDirectories(task contracts.WorkerTask) error {
	resolvedRoot, err := filepath.EvalSymlinks(task.WorktreePath)
	if err != nil {
		return fmt.Errorf("resolve worktree path: %w", err)
	}
	for _, scope := range task.WriteScopes {
		path := task.WorktreePath
		if scope != "." {
			path = filepath.Join(task.WorktreePath, filepath.FromSlash(scope))
		}
		if err := validateAbsoluteDirectory(path, "worker write scope "+scope); err != nil {
			return err
		}
		if err := validateScopeDirectoryPosition(
			path, task.WorktreePath, resolvedRoot, scope,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateScopeDirectoryPosition(
	path string,
	worktree string,
	resolvedRoot string,
	scope string,
) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve worker write scope %q: %w", scope, err)
	}
	if !pathWithin(resolved, resolvedRoot) {
		return fmt.Errorf("worker write scope %q escapes the worktree", scope)
	}
	logicalPosition, logicalErr := filepath.Rel(worktree, path)
	resolvedPosition, resolvedErr := filepath.Rel(resolvedRoot, resolved)
	if logicalErr != nil || resolvedErr != nil ||
		filepath.Clean(logicalPosition) != filepath.Clean(resolvedPosition) {
		return fmt.Errorf("worker write scope %q must not traverse symlinks", scope)
	}
	return nil
}

func validateGitWorktreeBinding(repository string, worktree string) error {
	repositoryTop, repositoryCommon, err := gitWorktreeIdentity(repository)
	if err != nil {
		return fmt.Errorf("inspect repository Git identity: %w", err)
	}
	worktreeTop, worktreeCommon, err := gitWorktreeIdentity(worktree)
	if err != nil {
		return fmt.Errorf("inspect worktree Git identity: %w", err)
	}
	resolvedRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		return fmt.Errorf("resolve repository path: %w", err)
	}
	resolvedWorktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return fmt.Errorf("resolve worktree path: %w", err)
	}
	if repositoryTop != resolvedRepository {
		return fmt.Errorf("repository path must be its Git worktree root")
	}
	if worktreeTop != resolvedWorktree {
		return fmt.Errorf("worktree path must be its Git worktree root")
	}
	if repositoryCommon != worktreeCommon {
		return fmt.Errorf("worktree does not belong to the declared repository")
	}
	return nil
}

func gitWorktreeIdentity(path string) (string, string, error) {
	command := exec.Command(
		"git",
		"-C", path,
		"rev-parse", "--show-toplevel", "--git-common-dir",
	)
	command.Env = gitInspectionEnvironment(os.Environ())
	output, err := command.Output()
	if err != nil {
		return "", "", fmt.Errorf("git rev-parse failed: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 2 || strings.TrimSpace(lines[0]) == "" || strings.TrimSpace(lines[1]) == "" {
		return "", "", fmt.Errorf("git rev-parse returned an invalid identity")
	}
	top, err := canonicalGitPath(path, lines[0])
	if err != nil {
		return "", "", fmt.Errorf("resolve Git worktree root: %w", err)
	}
	common, err := canonicalGitPath(path, lines[1])
	if err != nil {
		return "", "", fmt.Errorf("resolve Git common directory: %w", err)
	}
	return top, common, nil
}

func canonicalGitPath(base string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}
	value, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(filepath.Clean(value))
}

func gitInspectionEnvironment(source []string) []string {
	result := make([]string, 0, len(source)+4)
	for _, entry := range source {
		key, _, ok := strings.Cut(entry, "=")
		if ok && (strings.HasPrefix(key, "GIT_") || key == "LC_ALL") {
			continue
		}
		result = append(result, entry)
	}
	return append(
		result,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	)
}

func validateGitIndexFlags(path string) error {
	const maximumIndexBytes = 16 << 20
	command := exec.Command("git", "-C", path, "ls-files", "-v", "-z")
	command.Env = gitInspectionEnvironment(os.Environ())
	budget := newOutputBudget(maximumIndexBytes, nil)
	stdout := &boundedStream{budget: budget, name: "stdout"}
	stderr := &boundedStream{budget: budget, name: "stderr"}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("inspect worker Git index flags: %w", err)
	}
	if budget.total.Load() > maximumIndexBytes {
		return fmt.Errorf("worker Git index listing exceeds %d bytes", maximumIndexBytes)
	}
	for _, entry := range bytes.Split(stdout.Bytes(), []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		if len(entry) < 3 || entry[1] != ' ' || entry[0] != 'H' {
			return fmt.Errorf("worker Git index contains unsupported hidden path flags")
		}
		if err := addGitStatusPath(map[string]struct{}{}, string(entry[2:])); err != nil {
			return err
		}
	}
	return nil
}

func gitWorktreeChanges(path string) ([]string, error) {
	const maximumStatusBytes = 16 << 20
	command := exec.Command(
		"git", "-C", path, "status", "--porcelain=v1", "-z", "--untracked-files=all",
	)
	command.Env = gitInspectionEnvironment(os.Environ())
	budget := newOutputBudget(maximumStatusBytes, nil)
	stdout := &boundedStream{budget: budget, name: "stdout"}
	stderr := &boundedStream{budget: budget, name: "stderr"}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("inspect worker worktree changes: %w", err)
	}
	if budget.total.Load() > maximumStatusBytes {
		return nil, fmt.Errorf("worker worktree status exceeds %d bytes", maximumStatusBytes)
	}
	entries := bytes.Split(stdout.Bytes(), []byte{0})
	paths := make(map[string]struct{})
	for index := 0; index < len(entries); index++ {
		entry := entries[index]
		if len(entry) == 0 {
			continue
		}
		if len(entry) < 4 || entry[2] != ' ' {
			return nil, fmt.Errorf("worker worktree status is malformed")
		}
		if err := addGitStatusPath(paths, string(entry[3:])); err != nil {
			return nil, err
		}
		if entry[0] == 'R' || entry[0] == 'C' || entry[1] == 'R' || entry[1] == 'C' {
			index++
			if index >= len(entries) || len(entries[index]) == 0 {
				return nil, fmt.Errorf("worker worktree rename status is malformed")
			}
			if err := addGitStatusPath(paths, string(entries[index])); err != nil {
				return nil, err
			}
		}
	}
	if len(paths) > 2048 {
		return nil, fmt.Errorf("worker worktree changed path count exceeds 2048")
	}
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

func ignoredWorktreeSnapshot(path string) (map[string]string, error) {
	const (
		maximumPathBytes   = 16 << 20
		maximumContentRead = 64 << 20
		maximumPaths       = 2048
	)
	command := exec.Command(
		"git", "-C", path, "ls-files", "--others", "--ignored", "--exclude-standard", "-z",
	)
	command.Env = gitInspectionEnvironment(os.Environ())
	budget := newOutputBudget(maximumPathBytes, nil)
	stdout := &boundedStream{budget: budget, name: "stdout"}
	stderr := &boundedStream{budget: budget, name: "stderr"}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("inspect ignored worker paths: %w", err)
	}
	if budget.total.Load() > maximumPathBytes {
		return nil, fmt.Errorf("ignored worker path list exceeds %d bytes", maximumPathBytes)
	}

	result := make(map[string]string)
	var contentRead int64
	for _, entry := range bytes.Split(stdout.Bytes(), []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		entryPath := string(entry)
		if err := addGitStatusPath(map[string]struct{}{}, entryPath); err != nil {
			return nil, err
		}
		if len(result) >= maximumPaths {
			return nil, fmt.Errorf("ignored worker path count exceeds %d", maximumPaths)
		}
		fingerprint, consumed, err := fingerprintIgnoredPath(
			path,
			entryPath,
			maximumContentRead-contentRead,
		)
		if err != nil {
			return nil, err
		}
		contentRead += consumed
		if contentRead > maximumContentRead {
			return nil, fmt.Errorf("ignored worker content exceeds %d bytes", maximumContentRead)
		}
		result[entryPath] = fingerprint
	}
	return result, nil
}

func fingerprintIgnoredPath(root string, relative string, maximumBytes int64) (string, int64, error) {
	fullPath := filepath.Join(root, filepath.FromSlash(relative))
	info, err := os.Lstat(fullPath)
	if err != nil {
		return "", 0, fmt.Errorf("inspect ignored worker path %q: %w", relative, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(fullPath)
		if err != nil {
			return "", 0, fmt.Errorf("read ignored worker symlink %q: %w", relative, err)
		}
		if int64(len(target)) > maximumBytes {
			return "", 0, fmt.Errorf("ignored worker content exceeds its bounded inspection budget")
		}
		return "symlink:" + digest([]byte(target)), int64(len(target)), nil
	}
	if !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("ignored worker path %q is not a regular file or symlink", relative)
	}
	if info.Size() < 0 || info.Size() > maximumBytes {
		return "", 0, fmt.Errorf("ignored worker content exceeds its bounded inspection budget")
	}
	file, err := os.Open(fullPath)
	if err != nil {
		return "", 0, fmt.Errorf("open ignored worker path %q: %w", relative, err)
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maximumBytes+1))
	if err != nil {
		return "", 0, fmt.Errorf("hash ignored worker path %q: %w", relative, err)
	}
	if written != info.Size() {
		return "", 0, fmt.Errorf("ignored worker path %q changed during inspection", relative)
	}
	return fmt.Sprintf("regular:%d:%s", written, hex.EncodeToString(hash.Sum(nil))), written, nil
}

func changedSnapshotPaths(before map[string]string, after map[string]string) []string {
	changed := make(map[string]struct{})
	for path, fingerprint := range before {
		if after[path] != fingerprint {
			changed[path] = struct{}{}
		}
	}
	for path, fingerprint := range after {
		if before[path] != fingerprint {
			changed[path] = struct{}{}
		}
	}
	result := make([]string, 0, len(changed))
	for path := range changed {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func mergeChangedPaths(groups ...[]string) []string {
	merged := make(map[string]struct{})
	for _, group := range groups {
		for _, path := range group {
			merged[path] = struct{}{}
		}
	}
	result := make([]string, 0, len(merged))
	for path := range merged {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func addGitStatusPath(paths map[string]struct{}, path string) error {
	if !utf8.ValidString(path) || !validScope(path) || path == "." {
		return fmt.Errorf("worker worktree contains an invalid changed path")
	}
	paths[path] = struct{}{}
	return nil
}

func (s *Supervisor) validateResult(result contracts.WorkerResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode worker result: %w", err)
	}
	return s.registry.ValidateJSON("worker-result.schema.json", data)
}

func validateAbsoluteDirectory(path string, label string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("%s path must be absolute and clean", label)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat %s path: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s path must not be a symlink", label)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s path must be a directory", label)
	}
	return nil
}

func validateInvocation(invocation Invocation, worktree string) error {
	if strings.TrimSpace(invocation.Path) == "" {
		return fmt.Errorf("worker invocation executable is required")
	}
	if invocation.Dir != worktree {
		return fmt.Errorf("worker invocation directory must equal the worktree")
	}
	if strings.ContainsRune(invocation.Path, '\x00') || strings.ContainsRune(invocation.Stdin, '\x00') {
		return fmt.Errorf("worker invocation contains a NUL byte")
	}
	for _, arg := range invocation.Args {
		if strings.ContainsRune(arg, '\x00') {
			return fmt.Errorf("worker invocation argument contains a NUL byte")
		}
	}
	return nil
}

func validScope(scope string) bool {
	if scope == "." {
		return true
	}
	return scope != "" && !filepath.IsAbs(scope) && filepath.Clean(scope) == scope &&
		scope != ".." && !strings.HasPrefix(scope, ".."+string(filepath.Separator))
}

func pathsWithinScopes(paths []string, scopes []string) bool {
	for _, path := range paths {
		if !validScope(path) {
			return false
		}
		allowed := false
		for _, scope := range scopes {
			if scope == "." || path == scope || strings.HasPrefix(path, scope+string(filepath.Separator)) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	return true
}

func pathWithin(path string, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validateEvidenceDirectory(path string, root string) error {
	if err := validateAbsoluteDirectory(path, "evidence output"); err != nil {
		return err
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve evidence output directory: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve worktree path: %w", err)
	}
	if !pathStrictlyWithin(resolvedPath, resolvedRoot) {
		return fmt.Errorf("resolved evidence output directory must be a proper worktree descendant")
	}
	logicalPosition, logicalErr := filepath.Rel(root, path)
	resolvedPosition, resolvedErr := filepath.Rel(resolvedRoot, resolvedPath)
	if logicalErr != nil || resolvedErr != nil ||
		filepath.Clean(logicalPosition) != filepath.Clean(resolvedPosition) {
		return fmt.Errorf("evidence output directory must not traverse symlinks")
	}
	return nil
}

func pathStrictlyWithin(path string, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func processExitCode(err error) *int {
	if err == nil {
		code := 0
		return &code
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		code := exitError.ExitCode()
		return &code
	}
	return nil
}

func cleanUTF8(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	return strings.ToValidUTF8(string(data), "\uFFFD")
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
