package control

import (
	"fmt"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/clock"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

const (
	testSprintID   = identity.SprintID("sprint_00010203-0405-4607-8809-0a0b0c0d0e0f")
	testDecisionID = identity.DecisionID("decision_11111111-2222-4333-8444-555555555555")
	testRunID      = identity.RunID("run_aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
)

func TestServiceChecksAuthorizationBeforePersistence(t *testing.T) {
	t.Parallel()
	repository := NewMemoryRepository(clock.Fixed{Time: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)})
	service, err := NewService(repository)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := NewPrincipal("agent", "read-only", PermissionRead)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.PlanSprint(t.Context(), principal, PlanSprintInput{
		Title: "Unauthorized plan", Objective: "This must never be persisted",
		Command: testCommand("authorization-before-persistence"),
	})
	if !fault.IsCode(err, fault.CodePermissionDenied) {
		t.Fatalf("expected permission denial, got %v", err)
	}
	if _, err := repository.GetSprint(t.Context(), testSprintID); !fault.IsCode(err, fault.CodeNotFound) {
		t.Fatalf("unexpected persisted sprint: %v", err)
	}
}

func TestMemoryPromotionPermissionRejectsAgentAndWorkerPrincipals(t *testing.T) {
	t.Parallel()
	for _, actorType := range []string{"agent", "worker"} {
		if _, err := NewPrincipal(actorType, actorType+"-memory-promoter", PermissionMemoryPromote); !fault.IsCode(err, fault.CodePermissionDenied) {
			t.Fatalf("%s memory promotion permission error = %v", actorType, err)
		}
	}
}

func TestServiceRejectsCrossRepositoryPrincipalBeforePersistence(t *testing.T) {
	t.Parallel()
	repository := NewMemoryRepository(nil)
	service, err := NewService(repository)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := NewScopedPrincipal(
		"agent",
		"foreign-co-architect",
		LocalTenantID,
		"00000000-0000-4000-8000-000000000099",
		AllPermissions...,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.PlanSprint(t.Context(), principal, PlanSprintInput{
		Title: "Foreign plan", Objective: "This scope must never be persisted",
		Command: testCommand("cross-repository-before-persistence"),
	})
	if !fault.IsCode(err, fault.CodePermissionDenied) {
		t.Fatalf("expected scope denial, got %v", err)
	}
}

func TestServiceRejectsForgedAtomicOrReplayAudit(t *testing.T) {
	t.Parallel()
	repository := NewMemoryRepository(nil)
	service, err := NewService(repository)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := NewPrincipal("human", "audit-boundary", AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	base := AuditRecord{
		ToolName: auditToolPlanSprint, Outcome: "succeeded",
		CorrelationID: "forged-atomic-audit", IdempotencyKey: "forged-atomic-audit",
	}
	if err := service.RecordToolAudit(t.Context(), principal, base); !fault.IsCode(err, fault.CodeInvalidArgument) {
		t.Fatalf("forged atomic audit error = %v, want invalid argument", err)
	}
	base.ToolName = "forja.get_sprint"
	base.Replay = true
	if err := service.RecordToolAudit(t.Context(), principal, base); !fault.IsCode(err, fault.CodeInvalidArgument) {
		t.Fatalf("forged replay audit error = %v, want invalid argument", err)
	}
	if audits := repository.AuditRecords(); len(audits) != 0 {
		t.Fatalf("forged audits reached persistence: %#v", audits)
	}
}

func TestServiceGovernedLifecycleAndReplay(t *testing.T) {
	t.Parallel()
	repository := NewMemoryRepository(clock.Fixed{Time: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)})
	service, err := NewService(repository)
	if err != nil {
		t.Fatal(err)
	}
	service.WithIDGenerators(
		func() (identity.SprintID, error) { return testSprintID, nil },
		func() (identity.DecisionID, error) { return testDecisionID, nil },
		func() (identity.RunID, error) { return testRunID, nil },
	)
	principal, err := NewPrincipal("agent", "co-architect", AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	planInput := PlanSprintInput{
		Title: "MCP control surface", Objective: "Create the governed MCP control surface",
		Command: testCommand("plan-sprint-replay-0001"),
	}
	planned, err := service.PlanSprint(t.Context(), principal, planInput)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.PlanSprint(t.Context(), principal, planInput)
	if err != nil {
		t.Fatal(err)
	}
	if replayed != planned {
		t.Fatalf("plan replay changed result: %#v != %#v", replayed, planned)
	}
	audits := repository.AuditRecords()
	if len(audits) != 2 || audits[0].Replay || !audits[1].Replay {
		t.Fatalf("plan audit replay markers = %#v, want original then replay", audits)
	}
	submitted, err := service.SubmitSprint(t.Context(), principal, SubmitSprintInput{
		SprintID: planned.Sprint.SprintID, ExpectedVersion: 1, RiskClass: "medium",
		Command: testCommand("submit-sprint-replay-0001"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Sprint.Status != string(SprintAwaitingApproval) ||
		submitted.Decision.Status != string(DecisionPending) {
		t.Fatalf("unexpected submitted result: %#v", submitted)
	}
	approved, err := service.ResolveDecision(t.Context(), principal, ResolveDecisionInput{
		DecisionID: submitted.Decision.DecisionID, ExpectedVersion: 1,
		Reason: "The bounded change is ready", Command: testCommand("approve-decision-0001"),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Sprint.Status != string(SprintApproved) || approved.Run.State != "queued" {
		t.Fatalf("unexpected approval result: %#v", approved)
	}
}

func TestMemoryRepositoryReservesResumeTransitionsForResumeRun(t *testing.T) {
	t.Parallel()
	repository := NewMemoryRepository(clock.Fixed{Time: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)})
	service, err := NewService(repository)
	if err != nil {
		t.Fatal(err)
	}
	service.WithIDGenerators(
		func() (identity.SprintID, error) { return testSprintID, nil },
		func() (identity.DecisionID, error) { return testDecisionID, nil },
		func() (identity.RunID, error) { return testRunID, nil },
	)
	principal, err := NewPrincipal("human", "resume-approver", AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.PlanSprint(t.Context(), principal, PlanSprintInput{
		Title: "Governed resume", Objective: "Reserve resumable state pairs for the authorized command",
		Command: testCommand("memory-resume-plan"),
	})
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := service.SubmitSprint(t.Context(), principal, SubmitSprintInput{
		SprintID: planned.Sprint.SprintID, ExpectedVersion: planned.Sprint.Version,
		RiskClass: "medium", Command: testCommand("memory-resume-submit"),
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := service.ResolveDecision(t.Context(), principal, ResolveDecisionInput{
		DecisionID: submitted.Decision.DecisionID, ExpectedVersion: submitted.Decision.Version,
		Reason: "Resume guard test is bounded", Command: testCommand("memory-resume-approve"),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := identity.ParseRunID(approved.Run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	run := approved.Run
	for index, target := range []runstate.State{
		runstate.StatePreparing,
		runstate.StateFailedRetryable,
	} {
		run, err = repository.TransitionRun(
			t.Context(), runID, run.Version, target,
			memoryCommandMetadata(fmt.Sprintf("memory-resume-prepare-%d", index)),
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repository.TransitionRun(
		t.Context(), runID, run.Version, runstate.StateQueued,
		memoryCommandMetadata("memory-generic-retry-resume"),
	); !fault.IsCode(err, fault.CodePermissionDenied) {
		t.Fatalf("generic failed_retryable resume error = %v, want permission denied", err)
	}
	run, err = service.ResumeRun(t.Context(), principal, TransitionInput{
		RunID: run.RunID, ExpectedVersion: run.Version,
		Command: testCommand("memory-governed-retry-resume"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, target := range []runstate.State{
		runstate.StatePreparing,
		runstate.StateRunning,
		runstate.StateValidating,
		runstate.StateAwaitingDecision,
	} {
		run, err = repository.TransitionRun(
			t.Context(), runID, run.Version, target,
			memoryCommandMetadata(fmt.Sprintf("memory-awaiting-decision-%d", index)),
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repository.TransitionRun(
		t.Context(), runID, run.Version, runstate.StateQueued,
		memoryCommandMetadata("memory-generic-decision-resume"),
	); !fault.IsCode(err, fault.CodePermissionDenied) {
		t.Fatalf("generic awaiting_decision resume error = %v, want permission denied", err)
	}
	resumedDecision, err := service.ResumeRun(t.Context(), principal, TransitionInput{
		RunID: run.RunID, ExpectedVersion: run.Version,
		Command: testCommand("memory-governed-decision-resume"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumedDecision.State != string(runstate.StateQueued) {
		t.Fatalf("resumed decision state = %q, want queued", resumedDecision.State)
	}
}

func memoryCommandMetadata(key string) runstate.CommandMetadata {
	return runstate.CommandMetadata{
		IdempotencyKey: key,
		ActorType:      "system",
		ActorID:        "memory-test",
		CorrelationID:  "corr-" + key,
	}
}

func TestServiceRejectsWhitespaceOnlyDecisionReason(t *testing.T) {
	t.Parallel()
	repository := NewMemoryRepository(nil)
	service, err := NewService(repository)
	if err != nil {
		t.Fatal(err)
	}
	service.WithIDGenerators(
		func() (identity.SprintID, error) { return testSprintID, nil },
		func() (identity.DecisionID, error) { return testDecisionID, nil },
		func() (identity.RunID, error) { return testRunID, nil },
	)
	principal, err := NewPrincipal("agent", "co-architect", AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.PlanSprint(t.Context(), principal, PlanSprintInput{
		Title: "Reason validation", Objective: "Reject empty normalized reasons",
		Command: testCommand("reason-plan-0001"),
	})
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := service.SubmitSprint(t.Context(), principal, SubmitSprintInput{
		SprintID: planned.Sprint.SprintID, ExpectedVersion: 1, RiskClass: "low",
		Command: testCommand("reason-submit-0001"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ResolveDecision(t.Context(), principal, ResolveDecisionInput{
		DecisionID: submitted.Decision.DecisionID, ExpectedVersion: 1,
		Reason: "   ", Command: testCommand("reason-resolve-0001"),
	}, true)
	if !fault.IsCode(err, fault.CodeInvalidArgument) {
		t.Fatalf("whitespace-only reason error = %v", err)
	}
}

func testCommand(key string) CommandContext {
	return CommandContext{IdempotencyKey: key, CorrelationID: "corr-" + key}
}
