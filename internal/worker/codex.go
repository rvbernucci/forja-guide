package worker

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

// CodexAdapter builds non-interactive Codex CLI invocations.
type CodexAdapter struct {
	Executable string
}

func (a CodexAdapter) Name() string { return "codex-cli" }

func (a CodexAdapter) Build(
	task contracts.WorkerTask,
	paths ExecutionPaths,
) (Invocation, error) {
	executable := strings.TrimSpace(a.Executable)
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
		"--sandbox", "workspace-write",
		"--cd", task.WorktreePath,
		"--json",
		"--output-schema", paths.ReportSchemaPath,
		"--output-last-message", paths.ReportPath,
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

func codexPrompt(task contracts.WorkerTask) string {
	contextRef := "none"
	if task.ContextPackRef != nil {
		contextRef = *task.ContextPackRef
	}
	return fmt.Sprintf(`You are a bounded Forja %s worker.

Objective:
%s

Read scopes: %s
Write scopes: %s
Context pack reference: %s

Operate only inside the supplied worktree. Do not approve work, change scheduler state, access Forja control services, publish Git changes, or request credentials. Respect the declared write scopes. Finish by returning only the JSON object required by the supplied worker-report schema.
`, task.Role, task.Objective, strings.Join(task.ReadScopes, ", "), strings.Join(task.WriteScopes, ", "), contextRef)
}

func (CodexAdapter) ParseUsage(output []byte) contracts.WorkerUsage {
	var usage contracts.WorkerUsage
	scanner := bufio.NewScanner(bytes.NewReader(output))
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 2*1024*1024)
	for scanner.Scan() {
		var event map[string]any
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
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
