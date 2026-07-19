package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/rvbernucci/forja-guide/internal/clock"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/observability"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

const (
	mcpTestSprintID   = identity.SprintID("sprint_00010203-0405-4607-8809-0a0b0c0d0e0f")
	mcpTestDecisionID = identity.DecisionID("decision_11111111-2222-4333-8444-555555555555")
	mcpTestRunID      = identity.RunID("run_aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
)

func TestMCPGovernedLifecycleAndAudit(t *testing.T) {
	t.Parallel()
	session, repository := newTestSession(t)

	planned := callTool[control.PlanResult](t, session, ToolPlanSprint, map[string]any{
		"title": "Synthetic Sprint", "objective": "Exercise the governed control surface",
		"idempotency_key": "plan-mcp-0001", "correlation_id": "corr-plan-mcp-0001",
	})
	replayed := callTool[control.PlanResult](t, session, ToolPlanSprint, map[string]any{
		"title": "Synthetic Sprint", "objective": "Exercise the governed control surface",
		"idempotency_key": "plan-mcp-0001", "correlation_id": "corr-plan-mcp-0001",
	})
	if !reflect.DeepEqual(planned, replayed) {
		t.Fatalf("replayed plan changed: %#v != %#v", replayed, planned)
	}

	submitted := callTool[control.SubmissionResult](t, session, ToolSubmitSprint, map[string]any{
		"sprint_id": planned.Sprint.SprintID, "expected_version": 1, "risk_class": "medium",
		"idempotency_key": "submit-mcp-0001", "correlation_id": "corr-submit-mcp-0001",
	})
	invalid := callRaw(t, session, ToolApproveDecision, map[string]any{
		"decision_id": "yes", "expected_version": 1, "reason": "Conversational assent",
		"idempotency_key": "invalid-approval-0001", "correlation_id": "corr-invalid-approval",
	})
	if !invalid.IsError {
		t.Fatal("free-form approval unexpectedly succeeded")
	}
	pending := callTool[sprintOutput](t, session, ToolGetSprint, map[string]any{
		"sprint_id":       planned.Sprint.SprintID,
		"idempotency_key": "get-sprint-mcp-0001", "correlation_id": "corr-get-sprint-mcp-0001",
	})
	if pending.Sprint.Status != string(control.SprintAwaitingApproval) || pending.Sprint.PendingDecisionID == nil {
		t.Fatalf("invalid approval changed pending Sprint: %#v", pending.Sprint)
	}
	approved := callTool[control.DecisionResult](t, session, ToolApproveDecision, map[string]any{
		"decision_id": submitted.Decision.DecisionID, "expected_version": 1,
		"reason": "Bounded change is ready", "idempotency_key": "approve-mcp-0001",
		"correlation_id": "corr-approve-mcp-0001",
	})
	if approved.Run.State != "queued" {
		t.Fatalf("approved run state = %s", approved.Run.State)
	}
	cancelled := callTool[runOutput](t, session, ToolCancelRun, map[string]any{
		"run_id": approved.Run.RunID, "expected_version": approved.Run.Version,
		"idempotency_key": "cancel-mcp-0001", "correlation_id": "corr-cancel-mcp-0001",
	})
	data, err := json.Marshal(cancelled.Run)
	if err != nil {
		t.Fatal(err)
	}
	var run struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(data, &run); err != nil {
		t.Fatal(err)
	}
	if run.State != "cancelling" {
		t.Fatalf("cancelled run state = %s", run.State)
	}
	cancelling := callTool[sprintOutput](t, session, ToolGetSprint, map[string]any{
		"sprint_id": planned.Sprint.SprintID, "idempotency_key": "get-cancelling-sprint",
		"correlation_id": "corr-get-cancelling-sprint",
	})
	if cancelling.Sprint.Status != string(control.SprintCancelling) {
		t.Fatalf("cancelled Sprint status = %s", cancelling.Sprint.Status)
	}

	audits := repository.AuditRecords()
	if len(audits) != 8 {
		t.Fatalf("audit count = %d, want 8: %#v", len(audits), audits)
	}
	if audits[3].ToolName != ToolApproveDecision || audits[3].Outcome != "failed" {
		t.Fatalf("invalid approval audit was not preserved: %#v", audits[3])
	}
}

func TestMCPToolProducesContentFreeTrace(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	session, _ := newTestSession(t, observability.NewObserver(provider, nil))
	secretCorrelation := "corr-mcp-must-not-appear"
	callTool[control.PlanResult](t, session, ToolPlanSprint, map[string]any{
		"title": "Observable Sprint", "objective": "Prove the MCP trace boundary",
		"idempotency_key": "observable-plan-0001", "correlation_id": secretCorrelation,
	})
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "forja.mcp.plan_sprint" {
		t.Fatalf("MCP spans = %#v", spans)
	}
	if strings.Contains(spans[0].Name, secretCorrelation) {
		t.Fatal("raw MCP correlation identity leaked into span name")
	}
	for _, attribute := range spans[0].Attributes {
		if strings.Contains(attribute.Value.String(), secretCorrelation) {
			t.Fatal("raw MCP correlation identity leaked into span attributes")
		}
	}
}

func TestPendingDecisionMustResolveBeforeCancellation(t *testing.T) {
	t.Parallel()
	session, repository := newTestSession(t)
	planned := callTool[control.PlanResult](t, session, ToolPlanSprint, map[string]any{
		"title": "Pending cancellation", "objective": "Prevent a stranded pending decision",
		"idempotency_key": "pending-cancel-plan", "correlation_id": "corr-pending-cancel-plan",
	})
	proposedRunID, err := identity.ParseRunID(planned.Run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.TransitionRun(
		t.Context(),
		proposedRunID,
		planned.Run.Version,
		runstate.StateAwaitingApproval,
		runstate.CommandMetadata{
			IdempotencyKey: "proposed-generic-transition", ActorType: "system",
			ActorID: "legacy-api", CorrelationID: "corr-proposed-generic-transition",
		},
	)
	if !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("generic transition bypassed Sprint submission: %v", err)
	}
	submitted := callTool[control.SubmissionResult](t, session, ToolSubmitSprint, map[string]any{
		"sprint_id": planned.Sprint.SprintID, "expected_version": 1, "risk_class": "high",
		"idempotency_key": "pending-cancel-submit", "correlation_id": "corr-pending-cancel-submit",
	})
	cancelled := callRaw(t, session, ToolCancelRun, map[string]any{
		"run_id": submitted.Run.RunID, "expected_version": submitted.Run.Version,
		"idempotency_key": "pending-cancel-command", "correlation_id": "corr-pending-cancel-command",
	})
	if !cancelled.IsError {
		t.Fatal("run with a pending decision was cancelled")
	}
	runID, err := identity.ParseRunID(submitted.Run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.TransitionRun(t.Context(), runID, submitted.Run.Version, runstate.StateQueued, runstate.CommandMetadata{
		IdempotencyKey: "pending-generic-transition", ActorType: "system", ActorID: "scheduler",
		CorrelationID: "corr-pending-generic-transition",
	})
	if !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("generic transition bypassed pending decision: %v", err)
	}
	approved := callTool[control.DecisionResult](t, session, ToolApproveDecision, map[string]any{
		"decision_id": submitted.Decision.DecisionID, "expected_version": 1,
		"reason": "The decision remains resolvable", "idempotency_key": "pending-cancel-approve",
		"correlation_id": "corr-pending-cancel-approve",
	})
	if approved.Run.State != string(runstate.StateQueued) {
		t.Fatalf("pending decision was stranded in state %s", approved.Run.State)
	}
}

func TestReadToolsRejectMissingCommandIdentity(t *testing.T) {
	t.Parallel()
	session, repository := newTestSession(t)
	planned := callTool[control.PlanResult](t, session, ToolPlanSprint, map[string]any{
		"title": "Read identity", "objective": "Require caller supplied audit identity",
		"idempotency_key": "read-identity-plan", "correlation_id": "corr-read-identity-plan",
	})
	result := callRaw(t, session, ToolGetSprint, map[string]any{
		"sprint_id":       planned.Sprint.SprintID,
		"idempotency_key": "",
		"correlation_id":  "",
	})
	if !result.IsError {
		t.Fatal("read without command identity succeeded")
	}
	audits := repository.AuditRecords()
	if len(audits) != 2 || audits[1].Outcome != "failed" {
		t.Fatalf("rejected read audit = %#v", audits)
	}
}

func TestToolCompatibilityFixture(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t)
	result, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := append([]*mcp.Tool(nil), result.Tools...)
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	data, err := json.MarshalIndent(tools, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	fixture := filepath.Join("testdata", "tools-v1.json")
	if os.Getenv("UPDATE_TOOL_FIXTURES") == "1" {
		if err := os.MkdirAll(filepath.Dir(fixture), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixture, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read compatibility fixture (run with UPDATE_TOOL_FIXTURES=1 once): %v", err)
	}
	if string(data) != string(want) {
		t.Fatal("MCP tool schemas changed without an explicit compatibility fixture update")
	}
}

func TestAllControlToolsExposeCanonicalBehavior(t *testing.T) {
	t.Parallel()
	session, _ := newTestSession(t)
	planned := callTool[control.PlanResult](t, session, ToolPlanSprint, map[string]any{
		"title": "All tools", "objective": "Exercise every canonical MCP tool",
		"idempotency_key": "all-tools-plan-0001", "correlation_id": "corr-all-tools-plan",
	})
	inspected := callTool[runOutput](t, session, ToolGetRun, map[string]any{
		"run_id": planned.Run.RunID, "idempotency_key": "all-tools-get-run",
		"correlation_id": "corr-all-tools-get-run",
	})
	if inspected.Run != planned.Run {
		t.Fatalf("get_run changed run: %#v != %#v", inspected.Run, planned.Run)
	}
	submitted := callTool[control.SubmissionResult](t, session, ToolSubmitSprint, map[string]any{
		"sprint_id": planned.Sprint.SprintID, "expected_version": 1, "risk_class": "low",
		"idempotency_key": "all-tools-submit-0001", "correlation_id": "corr-all-tools-submit",
	})
	rejected := callTool[control.DecisionResult](t, session, ToolRejectDecision, map[string]any{
		"decision_id": submitted.Decision.DecisionID, "expected_version": 1,
		"reason": "Synthetic rejection path", "idempotency_key": "all-tools-reject-0001",
		"correlation_id": "corr-all-tools-reject",
	})
	if rejected.Decision.Status != "rejected" || rejected.Run.State != "cancelling" {
		t.Fatalf("unexpected rejection: %#v", rejected)
	}
}

func TestInvalidCommandIdentityStillEmitsFailedAudit(t *testing.T) {
	t.Parallel()
	session, repository := newTestSession(t)
	result := callRaw(t, session, ToolPlanSprint, map[string]any{
		"title": "Invalid identity", "objective": "Preserve evidence for rejected commands",
		"idempotency_key": "", "correlation_id": "",
	})
	if !result.IsError {
		t.Fatal("invalid command identity unexpectedly succeeded")
	}
	audits := repository.AuditRecords()
	if len(audits) != 1 || audits[0].Outcome != "failed" ||
		!strings.HasPrefix(audits[0].CorrelationID, "invalid-action:") {
		t.Fatalf("rejected command audit = %#v", audits)
	}
}

func TestResumeRunUsesGovernedStateMachine(t *testing.T) {
	t.Parallel()
	session, repository := newTestSession(t)
	planned := callTool[control.PlanResult](t, session, ToolPlanSprint, map[string]any{
		"title": "Resume path", "objective": "Exercise governed retry resumption",
		"idempotency_key": "resume-path-plan-0001", "correlation_id": "corr-resume-plan",
	})
	submitted := callTool[control.SubmissionResult](t, session, ToolSubmitSprint, map[string]any{
		"sprint_id": planned.Sprint.SprintID, "expected_version": 1, "risk_class": "low",
		"idempotency_key": "resume-path-submit", "correlation_id": "corr-resume-submit",
	})
	approved := callTool[control.DecisionResult](t, session, ToolApproveDecision, map[string]any{
		"decision_id": submitted.Decision.DecisionID, "expected_version": 1,
		"reason": "Prepare synthetic failure", "idempotency_key": "resume-path-approve",
		"correlation_id": "corr-resume-approve",
	})
	runID, err := identity.ParseRunID(approved.Run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	metadata := runstate.CommandMetadata{
		IdempotencyKey: "prepare-synthetic-run", ActorType: "system", ActorID: "test",
		CorrelationID: "corr-prepare-synthetic-run",
	}
	preparing, err := repository.TransitionRun(t.Context(), runID, approved.Run.Version, runstate.StatePreparing, metadata)
	if err != nil {
		t.Fatal(err)
	}
	metadata.IdempotencyKey = "fail-synthetic-run"
	metadata.CorrelationID = "corr-fail-synthetic-run"
	failed, err := repository.TransitionRun(t.Context(), runID, preparing.Version, runstate.StateFailedRetryable, metadata)
	if err != nil {
		t.Fatal(err)
	}
	resumed := callTool[runOutput](t, session, ToolResumeRun, map[string]any{
		"run_id": failed.RunID, "expected_version": failed.Version,
		"idempotency_key": "resume-path-command", "correlation_id": "corr-resume-command",
	})
	if resumed.Run.State != "queued" {
		t.Fatalf("resumed state = %s", resumed.Run.State)
	}
	metadata.IdempotencyKey = "advance-after-resume"
	metadata.CorrelationID = "corr-advance-after-resume"
	if _, err := repository.TransitionRun(
		t.Context(),
		runID,
		resumed.Run.Version,
		runstate.StatePreparing,
		metadata,
	); err != nil {
		t.Fatal(err)
	}
	replayed := callTool[runOutput](t, session, ToolResumeRun, map[string]any{
		"run_id": failed.RunID, "expected_version": failed.Version,
		"idempotency_key": "resume-path-command", "correlation_id": "corr-resume-command",
	})
	if replayed.Run != resumed.Run {
		t.Fatalf("resume replay changed result: %#v != %#v", replayed.Run, resumed.Run)
	}
}

func TestCrossScopeDenialIsNotRecastAsUnavailable(t *testing.T) {
	t.Parallel()
	repository := control.NewMemoryRepository(nil)
	service, err := control.NewService(repository)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := control.NewScopedPrincipal(
		"agent",
		"foreign-agent",
		control.LocalTenantID,
		"00000000-0000-4000-8000-000000000099",
		control.AllPermissions...,
	)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(service, FixedPrincipalResolver{Principal: principal}, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = adapter.planSprint(t.Context(), nil, planSprintInput{
		Title: "Foreign command", Objective: "Remain outside the bound repository",
		IdempotencyKey: "foreign-command-0001", CorrelationID: "corr-foreign-command",
	})
	if !fault.IsCode(err, fault.CodePermissionDenied) {
		t.Fatalf("cross-scope denial was recast as %v", err)
	}
	audits := repository.AuditRecords()
	if len(audits) != 1 || audits[0].Outcome != "denied" ||
		audits[0].ErrorCode != string(fault.CodePermissionDenied) {
		t.Fatalf("cross-scope denial audit = %#v", audits)
	}
}

type failingAuditRepository struct {
	control.Repository
}

func (failingAuditRepository) RecordToolAudit(context.Context, control.AuditRecord) error {
	return errors.New("audit store unavailable")
}

func TestRejectedToolSurfacesAuditPersistenceFailure(t *testing.T) {
	t.Parallel()
	repository := failingAuditRepository{Repository: control.NewMemoryRepository(nil)}
	service, err := control.NewService(repository)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := control.NewPrincipal("agent", "audit-failure-test", control.AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(service, FixedPrincipalResolver{Principal: principal}, "test")
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.finishAudit(
		t.Context(),
		principal,
		ToolPlanSprint,
		commandFields{IdempotencyKey: "audit-failure-command", CorrelationID: "corr-audit-failure"},
		fault.New(fault.CodeConflict, "test", "synthetic rejected action"),
	)
	if !fault.IsCode(err, fault.CodeUnavailable) {
		t.Fatalf("audit failure did not produce retryable unavailability: %v", err)
	}
}

func TestNormalizeAuditIdentityTrimsValidCommandMetadata(t *testing.T) {
	t.Parallel()
	idempotencyKey := strings.Repeat("i", 200)
	correlationID := strings.Repeat("c", 160)
	causationID := strings.Repeat("a", 160)
	command := normalizeAuditIdentity(
		ToolPlanSprint,
		control.Principal{ActorType: "agent", ActorID: "trim-test"},
		commandFields{
			IdempotencyKey: "  " + idempotencyKey + "  ",
			CorrelationID:  "  " + correlationID + "  ",
			CausationID:    pointerTo("  " + causationID + "  "),
		},
	)
	if command.IdempotencyKey != idempotencyKey {
		t.Fatalf("idempotency key was not normalized: %q", command.IdempotencyKey)
	}
	if command.CorrelationID != correlationID {
		t.Fatalf("correlation ID was not normalized: %q", command.CorrelationID)
	}
	if command.CausationID == nil || *command.CausationID != causationID {
		t.Fatalf("causation ID was not normalized: %#v", command.CausationID)
	}
}

func TestStdioCommandTransport(t *testing.T) {
	if os.Getenv("FORJA_MCP_TEST_HELPER") == "1" {
		repository := control.NewMemoryRepository(nil)
		service, err := control.NewService(repository)
		if err != nil {
			t.Fatal(err)
		}
		principal, err := control.NewPrincipal("agent", "stdio-helper", control.AllPermissions...)
		if err != nil {
			t.Fatal(err)
		}
		adapter, err := New(service, FixedPrincipalResolver{Principal: principal}, "stdio-test")
		if err != nil {
			t.Fatal(err)
		}
		if err := adapter.Server().Run(t.Context(), &mcp.StdioTransport{}); err != nil {
			t.Fatal(err)
		}
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestStdioCommandTransport$")
	command.Env = append(os.Environ(), "FORJA_MCP_TEST_HELPER=1")
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-fake-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	result, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != len(ToolNames) {
		t.Fatalf("stdio tool count = %d, want %d", len(result.Tools), len(ToolNames))
	}
}

func newTestSession(
	t *testing.T,
	observers ...*observability.Observer,
) (*mcp.ClientSession, *control.MemoryRepository) {
	t.Helper()
	repository := control.NewMemoryRepository(clock.Fixed{Time: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)})
	service, err := control.NewService(repository)
	if err != nil {
		t.Fatal(err)
	}
	service.WithIDGenerators(
		func() (identity.SprintID, error) { return mcpTestSprintID, nil },
		func() (identity.DecisionID, error) { return mcpTestDecisionID, nil },
		func() (identity.RunID, error) { return mcpTestRunID, nil },
	)
	principal, err := control.NewPrincipal("agent", "co-architect", control.AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(service, FixedPrincipalResolver{Principal: principal}, "test", observers...)
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := adapter.Server().Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "deterministic-fake-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, repository
}

func callRaw(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return result
}

func callTool[T any](t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) T {
	t.Helper()
	result := callRaw(t, session, name, arguments)
	if result.IsError {
		content, _ := json.Marshal(result.Content)
		t.Fatalf("tool %s failed: %s", name, content)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output T
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("decode %s output: %v\n%s", name, err, data)
	}
	return output
}

func pointerTo(value string) *string {
	return &value
}
