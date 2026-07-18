package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

// CodexAdapter builds non-interactive Codex CLI invocations.
type CodexAdapter struct {
	Executable string
}

func (a CodexAdapter) Name() string { return "codex-cli" }

// IsolationCapability declares the Codex CLI sandbox contract verified from
// each generated invocation before execution.
func (CodexAdapter) IsolationCapability() IsolationCapability {
	return IsolationCapability{
		PolicyID:        "codex-cli-v1",
		Version:         "1.0",
		ReadBoundary:    "full-worktree",
		WriteBoundary:   "declared-roots",
		NetworkBoundary: "denied",
	}
}

func (a CodexAdapter) Build(
	task contracts.WorkerTask,
	paths ExecutionPaths,
) (Invocation, error) {
	return buildCodexInvocation(a.Executable, task, paths)
}

func buildCodexInvocation(
	executable string,
	task contracts.WorkerTask,
	paths ExecutionPaths,
) (Invocation, error) {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		executable = "codex"
	}
	args := []string{
		"exec",
		"--ignore-user-config",
		"--strict-config",
		"--config", `approval_policy="never"`,
		"--config", "sandbox_workspace_write.network_access=false",
		"--config", `shell_environment_policy.inherit="all"`,
		"--config", `shell_environment_policy.ignore_default_excludes=false`,
		"--config", `shell_environment_policy.include_only=["PATH","HOME","LANG","LC_ALL","TMPDIR","TERM","SSL_CERT_FILE","SSL_CERT_DIR"]`,
		"--ephemeral",
		"--ignore-rules",
		"--sandbox", "workspace-write",
		"--cd", task.EvidenceOutputDir,
		"--json",
		"--output-schema", paths.ReportSchemaPath,
		"--output-last-message", paths.ReportPath,
	}
	for _, root := range codexWritableRoots(task) {
		args = append(args, "--add-dir", root)
	}
	if task.Model != nil && strings.TrimSpace(*task.Model) != "" {
		args = append(args, "--model", strings.TrimSpace(*task.Model))
	}
	args = append(args, "-")
	return Invocation{
		Path:  executable,
		Args:  args,
		Dir:   task.WorktreePath,
		Stdin: codexPrompt(task),
	}, nil
}

// CodexIsolationPolicy rebuilds the canonical invocation independently from
// the adapter and is the supervisor-owned authority for Codex containment.
type CodexIsolationPolicy struct {
	Executable string
}

func (CodexIsolationPolicy) ID() string { return "codex-cli-v1" }

// Verify rejects any invocation that differs from the canonical isolated form.
func (p CodexIsolationPolicy) Verify(
	task contracts.WorkerTask,
	paths ExecutionPaths,
	invocation Invocation,
) error {
	expected, err := buildCodexInvocation(p.Executable, task, paths)
	if err != nil {
		return err
	}
	if invocation.Path != expected.Path || invocation.Dir != expected.Dir ||
		invocation.Stdin != expected.Stdin || !slices.Equal(invocation.Args, expected.Args) {
		return fmt.Errorf("codex invocation differs from the canonical isolated invocation")
	}
	return nil
}

func codexPrompt(task contracts.WorkerTask) string {
	contextRef := "none"
	if task.ContextPackRef != nil {
		contextRef = *task.ContextPackRef
	}
	return fmt.Sprintf(`You are a bounded Forja %s worker.

Objective:
%s

Repository path: %s
Read scopes: %s
Write scopes: %s
Context pack reference: %s

Read the repository through its absolute path. Write only inside the declared write scopes and evidence directory exposed by the sandbox. Do not approve work, change scheduler state, access Forja control services, publish Git changes, or request credentials. Finish by returning only the JSON object required by the supplied worker-report schema.
`, task.Role, task.Objective, task.WorktreePath, strings.Join(task.ReadScopes, ", "), strings.Join(task.WriteScopes, ", "), contextRef)
}

func (CodexAdapter) ParseUsage(output []byte) contracts.WorkerUsage {
	var usage contracts.WorkerUsage
	// Supervisor output is already bounded, so splitting the complete capture
	// avoids Scanner's token ceiling silently discarding later usage events.
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		var event map[string]any
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		typeName, _ := event["type"].(string)
		if typeName == "turn.completed" {
			if item, ok := event["usage"].(map[string]any); ok {
				usage.InputTokens = integerField(item, "input_tokens")
				usage.CachedInputTokens = integerField(item, "cached_input_tokens")
				usage.OutputTokens = integerField(item, "output_tokens")
			}
		}
		if typeName == "item.completed" {
			if item, ok := event["item"].(map[string]any); ok {
				switch item["type"] {
				case "command_execution", "mcp_tool_call", "web_search":
					usage.ToolCalls++
				}
			}
		}
	}
	return usage
}

func codexWritableRoots(task contracts.WorkerTask) []string {
	evidence := filepath.Clean(task.EvidenceOutputDir)
	seen := make(map[string]struct{}, len(task.WriteScopes))
	for _, scope := range task.WriteScopes {
		root := task.WorktreePath
		if scope != "." {
			root = filepath.Join(task.WorktreePath, filepath.FromSlash(scope))
		}
		root = filepath.Clean(root)
		if root != evidence {
			seen[root] = struct{}{}
		}
	}
	roots := make([]string, 0, len(seen))
	for root := range seen {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

func integerField(value map[string]any, key string) int {
	number, ok := value[key].(float64)
	if !ok || number < 0 {
		return 0
	}
	return int(number)
}

func (CodexAdapter) RetryableFailure(exitCode int, stderr string) bool {
	if exitCode == 75 {
		return true
	}
	message := strings.ToLower(stderr)
	for _, marker := range []string{
		"rate limit", "temporarily unavailable", "connection reset",
		"timed out", "timeout", "status 429", "status 502", "status 503",
		"status 504",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return exitCode == 1
}
