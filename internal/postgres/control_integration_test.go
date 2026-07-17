package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rvbernucci/forja-guide/internal/clock"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/mcpserver"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

func TestConcurrentDecisionResolutionHasOneWinner(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(pool, nil, DefaultTenantID, DefaultRepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	service, err := control.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := control.NewPrincipal("agent", "concurrent-decider", control.AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.PlanSprint(t.Context(), principal, control.PlanSprintInput{
		Title: "Concurrent decision", Objective: "Prove that one decision resolution wins",
		Command: control.CommandContext{IdempotencyKey: "concurrent-plan-0001", CorrelationID: "corr-concurrent-plan"},
	})
	if err != nil {
		t.Fatal(err)
	}
	proposedRunID, err := identity.ParseRunID(planned.Run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.TransitionRun(
		t.Context(),
		proposedRunID,
		planned.Run.Version,
		runstate.StateAwaitingApproval,
		runstate.CommandMetadata{
			IdempotencyKey: "generic-proposed-transition", ActorType: "system",
			ActorID: "legacy-api", CorrelationID: "corr-generic-proposed-transition",
		},
	)
	if !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("generic transition bypassed Sprint submission: %v", err)
	}
	submitted, err := service.SubmitSprint(t.Context(), principal, control.SubmitSprintInput{
		SprintID: planned.Sprint.SprintID, ExpectedVersion: 1, RiskClass: "medium",
		Command: control.CommandContext{IdempotencyKey: "concurrent-submit-0001", CorrelationID: "corr-concurrent-submit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errorsFound := make(chan error, 2)
	for index := range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, resolveErr := service.ResolveDecision(t.Context(), principal, control.ResolveDecisionInput{
				DecisionID: submitted.Decision.DecisionID, ExpectedVersion: 1,
				Reason: "Concurrent resolution must serialize",
				Command: control.CommandContext{
					IdempotencyKey: fmt.Sprintf("concurrent-decide-%04d", index),
					CorrelationID:  fmt.Sprintf("corr-concurrent-decide-%04d", index),
				},
			}, index == 0)
			errorsFound <- resolveErr
		}()
	}
	wait.Wait()
	close(errorsFound)
	successes, conflicts := 0, 0
	for resolveErr := range errorsFound {
		switch {
		case resolveErr == nil:
			successes++
		case fault.IsCode(resolveErr, fault.CodeConflict):
			conflicts++
		default:
			t.Fatalf("unexpected resolution result: %v", resolveErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestPendingDecisionBlocksGenericRunTransition(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(pool, nil, DefaultTenantID, DefaultRepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	service, err := control.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := control.NewPrincipal("agent", "generic-transition-test", control.AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.PlanSprint(t.Context(), principal, control.PlanSprintInput{
		Title: "Protected transition", Objective: "Prevent generic APIs from bypassing approval",
		Command: control.CommandContext{IdempotencyKey: "generic-guard-plan", CorrelationID: "corr-generic-guard-plan"},
	})
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := service.SubmitSprint(t.Context(), principal, control.SubmitSprintInput{
		SprintID: planned.Sprint.SprintID, ExpectedVersion: planned.Sprint.Version, RiskClass: "high",
		Command: control.CommandContext{IdempotencyKey: "generic-guard-submit", CorrelationID: "corr-generic-guard-submit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := identity.ParseRunID(submitted.Run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.TransitionRun(t.Context(), runID, submitted.Run.Version, "queued", runstate.CommandMetadata{
		IdempotencyKey: "generic-guard-transition", ActorType: "system", ActorID: "legacy-api",
		CorrelationID: "corr-generic-guard-transition",
	})
	if !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("generic transition bypassed a pending decision: %v", err)
	}
	resolved, err := service.ResolveDecision(t.Context(), principal, control.ResolveDecisionInput{
		DecisionID: submitted.Decision.DecisionID, ExpectedVersion: submitted.Decision.Version,
		Reason:  "The protected decision remains resolvable",
		Command: control.CommandContext{IdempotencyKey: "generic-guard-resolve", CorrelationID: "corr-generic-guard-resolve"},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Run.State != "queued" {
		t.Fatalf("resolved run state = %s", resolved.Run.State)
	}
}

func TestTransitionRunLocksSprintBeforeRun(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(pool, nil, DefaultTenantID, DefaultRepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	service, err := control.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := control.NewPrincipal("agent", "lock-order-test", control.AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.PlanSprint(t.Context(), principal, control.PlanSprintInput{
		Title: "Consistent lock order", Objective: "Prevent Sprint and Run transactions from deadlocking",
		Command: control.CommandContext{IdempotencyKey: "lock-order-plan", CorrelationID: "corr-lock-order-plan"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sprintID, err := identity.ParseSprintID(planned.Sprint.SprintID)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := identity.ParseRunID(planned.Run.RunID)
	if err != nil {
		t.Fatal(err)
	}

	blocker, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(t.Context()) }()
	if _, err := blocker.Exec(t.Context(), `
		SELECT 1 FROM forja.sprints
		WHERE tenant_id=$1 AND repository_id=$2 AND sprint_id=$3
		FOR UPDATE`, DefaultTenantID, DefaultRepositoryID, sprintID.UUID()); err != nil {
		t.Fatal(err)
	}

	transitionContext, cancelTransition := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelTransition()
	transitionResult := make(chan error, 1)
	go func() {
		_, transitionErr := store.TransitionRun(
			transitionContext,
			runID,
			planned.Run.Version,
			runstate.StateCancelling,
			runstate.CommandMetadata{
				IdempotencyKey: "lock-order-transition", ActorType: "agent",
				ActorID: "lock-order-test", CorrelationID: "corr-lock-order-transition",
			},
		)
		transitionResult <- transitionErr
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		err := pool.QueryRow(t.Context(), `
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE pid <> pg_backend_pid()
				  AND datname=current_database()
				  AND wait_event_type='Lock'
				  AND query LIKE '%FROM forja.sprints AS sp%'
				  AND query LIKE '%FOR UPDATE OF sp%'
			)`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("transition did not block while acquiring the Sprint row")
		}
		time.Sleep(10 * time.Millisecond)
	}

	probe, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Exec(t.Context(), `
		SELECT 1 FROM forja.runs
		WHERE tenant_id=$1 AND repository_id=$2 AND run_id=$3
		FOR UPDATE NOWAIT`, DefaultTenantID, DefaultRepositoryID, runID.String()); err != nil {
		_ = probe.Rollback(t.Context())
		t.Fatalf("Run was locked before Sprint: %v", err)
	}
	if err := probe.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-transitionResult; err != nil {
		t.Fatalf("transition after lock release: %v", err)
	}
	verify := postgresScriptCommand(t, "../../scripts/postgres_verify.sh")
	if output, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("verify generic Sprint cancellation evidence: %v\n%s", err, output)
	}
}

func TestDurableResumeReplaysAfterRunAdvances(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(pool, nil, DefaultTenantID, DefaultRepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	runID := mustRunID(t)
	metadata := runstate.CommandMetadata{
		IdempotencyKey: "durable-resume-create", ActorType: "system", ActorID: "scheduler",
		CorrelationID: "corr-durable-resume-create",
	}
	run, err := store.CreateRun(t.Context(), runID, "Prove durable resume replay", metadata)
	if err != nil {
		t.Fatal(err)
	}
	for _, transition := range []struct {
		state runstate.State
		key   string
	}{
		{runstate.StateAwaitingApproval, "durable-resume-awaiting"},
		{runstate.StateQueued, "durable-resume-queued"},
		{runstate.StatePreparing, "durable-resume-preparing"},
		{runstate.StateFailedRetryable, "durable-resume-failed"},
	} {
		metadata.IdempotencyKey = transition.key
		metadata.CorrelationID = "corr-" + transition.key
		run, err = store.TransitionRun(t.Context(), runID, run.Version, transition.state, metadata)
		if err != nil {
			t.Fatal(err)
		}
	}
	resumeMetadata := runstate.CommandMetadata{
		IdempotencyKey: "durable-resume-command", ActorType: "agent", ActorID: "co-architect",
		CorrelationID: "corr-durable-resume-command",
	}
	if _, err := store.TransitionRun(
		t.Context(), runID, run.Version, runstate.StateQueued,
		runstate.CommandMetadata{
			IdempotencyKey: "durable-generic-resume", ActorType: "system",
			ActorID: "legacy-api", CorrelationID: "corr-durable-generic-resume",
		},
	); !fault.IsCode(err, fault.CodePermissionDenied) {
		t.Fatalf("generic resume error = %v, want permission denied", err)
	}
	resumed, err := store.ResumeRun(t.Context(), runID, run.Version, resumeMetadata)
	if err != nil {
		t.Fatal(err)
	}
	metadata.IdempotencyKey = "durable-resume-advanced"
	metadata.CorrelationID = "corr-durable-resume-advanced"
	if _, err := store.TransitionRun(
		t.Context(),
		runID,
		resumed.Version,
		runstate.StatePreparing,
		metadata,
	); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.ResumeRun(t.Context(), runID, run.Version, resumeMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if replayed != resumed {
		t.Fatalf("durable resume replay changed: %#v != %#v", replayed, resumed)
	}
}

func TestMCPControlLifecycleIsDurableAndAudited(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(
		pool,
		clock.Fixed{Time: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)},
		DefaultTenantID,
		DefaultRepositoryID,
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := control.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	const (
		sprintID   = identity.SprintID("sprint_00010203-0405-4607-8809-0a0b0c0d0e0f")
		decisionID = identity.DecisionID("decision_11111111-2222-4333-8444-555555555555")
		runID      = identity.RunID("run_aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	)
	service.WithIDGenerators(
		func() (identity.SprintID, error) { return sprintID, nil },
		func() (identity.DecisionID, error) { return decisionID, nil },
		func() (identity.RunID, error) { return runID, nil },
	)
	principal, err := control.NewPrincipal("agent", "durable-co-architect", control.AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := mcpserver.New(service, mcpserver.FixedPrincipalResolver{Principal: principal}, "integration")
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := adapter.Server().Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "postgres-fake-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	planArgs := map[string]any{
		"title": "Durable MCP Sprint", "objective": "Prove the durable governed MCP lifecycle",
		"idempotency_key": "pg-plan-mcp-0001", "correlation_id": "corr-pg-plan-mcp-0001",
	}
	planned := callMCPTool[control.PlanResult](t, session, mcpserver.ToolPlanSprint, planArgs)
	replayed := callMCPTool[control.PlanResult](t, session, mcpserver.ToolPlanSprint, planArgs)
	if replayed.Sprint != planned.Sprint || replayed.Run != planned.Run {
		t.Fatal("durable replay changed the plan result")
	}
	var originalAudits, replayAudits int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*) FILTER (WHERE payload->>'replay'='false'),
		       count(*) FILTER (WHERE payload->>'replay'='true')
		FROM forja.events
		WHERE aggregate_type='audit'
		  AND event_type='mcp.tool.succeeded'
		  AND idempotency_key='pg-plan-mcp-0001'`).Scan(&originalAudits, &replayAudits); err != nil {
		t.Fatal(err)
	}
	if originalAudits != 1 || replayAudits != 1 {
		t.Fatalf("plan audits original=%d replay=%d, want 1/1", originalAudits, replayAudits)
	}
	submitted := callMCPTool[control.SubmissionResult](t, session, mcpserver.ToolSubmitSprint, map[string]any{
		"sprint_id": planned.Sprint.SprintID, "expected_version": 1, "risk_class": "high",
		"idempotency_key": "pg-submit-mcp-0001", "correlation_id": "corr-pg-submit-mcp-0001",
	})
	invalid := callMCPRaw(t, session, mcpserver.ToolApproveDecision, map[string]any{
		"decision_id": "yes", "expected_version": 1, "reason": "Free form text",
		"idempotency_key": "pg-invalid-approval", "correlation_id": "corr-pg-invalid-approval",
	})
	if !invalid.IsError {
		t.Fatal("free-form text approved a durable decision")
	}
	_ = callMCPTool[map[string]any](t, session, mcpserver.ToolGetSprint, map[string]any{
		"sprint_id": sprintID.String(), "idempotency_key": "pg-get-sprint-0001",
		"correlation_id": "corr-pg-get-sprint-0001",
	})
	approved := callMCPTool[control.DecisionResult](t, session, mcpserver.ToolApproveDecision, map[string]any{
		"decision_id": submitted.Decision.DecisionID, "expected_version": 1,
		"reason": "Independent evidence is sufficient", "idempotency_key": "pg-approve-mcp-0001",
		"correlation_id": "corr-pg-approve-mcp-0001",
	})
	_ = callMCPTool[map[string]any](t, session, mcpserver.ToolCancelRun, map[string]any{
		"run_id": approved.Run.RunID, "expected_version": approved.Run.Version,
		"idempotency_key": "pg-cancel-mcp-0001", "correlation_id": "corr-pg-cancel-mcp-0001",
	})

	var domainEvents, auditEvents, outboxRows int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*) FILTER (WHERE aggregate_type <> 'audit'),
		       count(*) FILTER (WHERE aggregate_type = 'audit')
		FROM forja.events`).Scan(&domainEvents, &auditEvents); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM forja.outbox").Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if domainEvents != 10 || auditEvents != 7 || outboxRows != 17 {
		t.Fatalf("events domain=%d audit=%d outbox=%d", domainEvents, auditEvents, outboxRows)
	}
	var state, sprintStatus, decisionStatus string
	if err := pool.QueryRow(t.Context(), `
		SELECT r.state, sp.status, d.status
		FROM forja.runs AS r
		JOIN forja.sprints AS sp
		  ON sp.tenant_id=r.tenant_id
		 AND sp.repository_id=r.repository_id
		 AND sp.sprint_id=r.sprint_id
		 AND sp.run_id=r.run_id
		JOIN forja.decisions AS d
		  ON d.tenant_id=r.tenant_id
		 AND d.repository_id=r.repository_id
		 AND d.sprint_id=r.sprint_id
		 AND d.run_id=r.run_id
		WHERE r.run_id=$1`, runID.String()).Scan(&state, &sprintStatus, &decisionStatus); err != nil {
		t.Fatal(err)
	}
	if state != "cancelling" || sprintStatus != "cancelling" || decisionStatus != "approved" {
		t.Fatalf("durable state run=%s sprint=%s decision=%s", state, sprintStatus, decisionStatus)
	}
}

func TestReceiptVerifierAcceptsReplayWithFreshCorrelation(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(pool, nil, DefaultTenantID, DefaultRepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	service, err := control.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := control.NewPrincipal("agent", "retry-correlation", control.AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	input := control.PlanSprintInput{
		Title:     "Fresh retry correlation",
		Objective: "Verify stable command identity across retry attempts",
		Command: control.CommandContext{
			IdempotencyKey: "retry-correlation-plan",
			CorrelationID:  "corr-retry-original",
		},
	}
	planned, err := service.PlanSprint(t.Context(), principal, input)
	if err != nil {
		t.Fatal(err)
	}
	input.Command.CorrelationID = "corr-retry-second-attempt"
	replayed, err := service.PlanSprint(t.Context(), principal, input)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Sprint != planned.Sprint || replayed.Run != planned.Run {
		t.Fatal("retry with a fresh correlation changed the replayed result")
	}
	verify := postgresScriptCommand(t, "../../scripts/postgres_verify.sh")
	if output, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("verify retry with fresh correlation: %v\n%s", err, output)
	}
}

func TestMutationRollsBackWhenSuccessAuditCannotPersist(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		CREATE FUNCTION forja.reject_test_audit()
		RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'synthetic audit failure';
		END
		$$;
		CREATE TRIGGER reject_test_audit
		BEFORE INSERT ON forja.events
		FOR EACH ROW WHEN (NEW.aggregate_type='audit')
		EXECUTE FUNCTION forja.reject_test_audit()`); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(pool, nil, DefaultTenantID, DefaultRepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	service, err := control.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := control.NewPrincipal("agent", "atomic-audit-test", control.AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PlanSprint(t.Context(), principal, control.PlanSprintInput{
		Title: "Atomic audit", Objective: "Rollback the mutation when its success audit cannot persist",
		Command: control.CommandContext{
			IdempotencyKey: "atomic-audit-plan", CorrelationID: "corr-atomic-audit-plan",
		},
	}); err == nil {
		t.Fatal("plan committed without its required success audit")
	}
	var sprints, runs, events, receipts int
	if err := pool.QueryRow(t.Context(), `
		SELECT
			(SELECT count(*) FROM forja.sprints),
			(SELECT count(*) FROM forja.runs),
			(SELECT count(*) FROM forja.events),
			(SELECT count(*) FROM forja.idempotency_keys)`).Scan(
		&sprints,
		&runs,
		&events,
		&receipts,
	); err != nil {
		t.Fatal(err)
	}
	if sprints != 0 || runs != 0 || events != 0 || receipts != 0 {
		t.Fatalf(
			"partial mutation survived audit failure: sprints=%d runs=%d events=%d receipts=%d",
			sprints,
			runs,
			events,
			receipts,
		)
	}
}

func TestGovernedMigrationRollsBackAfterFeatureUse(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(pool, nil, DefaultTenantID, DefaultRepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	service, err := control.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := control.NewPrincipal("agent", "rollback-validator", control.AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.PlanSprint(t.Context(), principal, control.PlanSprintInput{
		Title: "Rollback after use", Objective: "Prove rollback after durable control events",
		Command: control.CommandContext{IdempotencyKey: "rollback-plan-0001", CorrelationID: "corr-rollback-plan"},
	})
	if err != nil {
		t.Fatal(err)
	}
	submitInput := control.SubmitSprintInput{
		SprintID: planned.Sprint.SprintID, ExpectedVersion: 1, RiskClass: "medium",
		Command: control.CommandContext{IdempotencyKey: "rollback-submit-0001", CorrelationID: "corr-rollback-submit"},
	}
	submitted, err := service.SubmitSprint(t.Context(), principal, submitInput)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := service.ResolveDecision(t.Context(), principal, control.ResolveDecisionInput{
		DecisionID: submitted.Decision.DecisionID, ExpectedVersion: submitted.Decision.Version,
		Reason:  "Resolve governance before schema rollback",
		Command: control.CommandContext{IdempotencyKey: "rollback-resolve-0001", CorrelationID: "corr-rollback-resolve"},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CancelRun(t.Context(), principal, control.TransitionInput{
		RunID:           approved.Run.RunID,
		ExpectedVersion: approved.Run.Version,
		Command: control.CommandContext{
			IdempotencyKey: "rollback-cancel-0001",
			CorrelationID:  "corr-rollback-cancel",
		},
	}); err != nil {
		t.Fatal(err)
	}
	genericRunID := mustRunID(t)
	genericRun, err := store.CreateRun(
		t.Context(),
		genericRunID,
		"Preserve a generic transition with a colliding governed key",
		testMetadata("rollback-generic-create"),
	)
	if err != nil {
		t.Fatal(err)
	}
	genericTransitionMetadata := runstate.CommandMetadata{
		IdempotencyKey: "rollback-cancel-0001",
		ActorType:      "agent",
		ActorID:        "rollback-validator",
		CorrelationID:  "corr-rollback-cancel",
	}
	genericRun, err = store.TransitionRun(
		t.Context(),
		genericRunID,
		genericRun.Version,
		runstate.StateAwaitingApproval,
		genericTransitionMetadata,
	)
	if err != nil {
		t.Fatal(err)
	}
	resumableID := mustRunID(t)
	resumable, err := store.CreateRun(
		t.Context(),
		resumableID,
		"Resume across governed migration rollback",
		testMetadata("rollback-resume-create"),
	)
	if err != nil {
		t.Fatal(err)
	}
	for index, target := range []runstate.State{
		runstate.StateAwaitingApproval,
		runstate.StateQueued,
		runstate.StatePreparing,
		runstate.StateFailedRetryable,
	} {
		resumable, err = store.TransitionRun(
			t.Context(),
			resumableID,
			resumable.Version,
			target,
			testMetadata(fmt.Sprintf("rollback-resume-transition-%d", index)),
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.ResumeRun(t.Context(), principal, control.TransitionInput{
		RunID:           resumable.RunID,
		ExpectedVersion: resumable.Version,
		Command: control.CommandContext{
			IdempotencyKey: "rollback-resume-0001",
			CorrelationID:  "corr-rollback-resume",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordToolAudit(t.Context(), principal, control.AuditRecord{
		ToolName: "forja.get_sprint", Outcome: "succeeded",
		CorrelationID: "corr-rollback-audit", IdempotencyKey: "rollback-audit-0001",
	}); err != nil {
		t.Fatal(err)
	}
	var governedReceipts int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*) FROM forja.idempotency_keys
		WHERE idempotency_key IN (
			'rollback-plan-0001', 'rollback-submit-0001',
			'rollback-resolve-0001', 'rollback-cancel-0001',
			'rollback-resume-0001'
		)`).Scan(&governedReceipts); err != nil {
		t.Fatal(err)
	}
	if governedReceipts != 6 {
		t.Fatalf("governed and colliding generic receipts before rollback = %d, want 6", governedReceipts)
	}
	if err := RollbackLast(t.Context(), pool); err != nil {
		t.Fatalf("rollback governed migration after use: %v", err)
	}
	var unsupportedEvents int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM forja.events
		WHERE aggregate_type IN ('audit', 'decision')`).Scan(&unsupportedEvents); err != nil {
		t.Fatal(err)
	}
	if unsupportedEvents != 0 {
		t.Fatalf("unsupported events after rollback = %d", unsupportedEvents)
	}
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*) FROM forja.idempotency_keys
		WHERE idempotency_key IN (
			'rollback-plan-0001', 'rollback-submit-0001',
			'rollback-resolve-0001', 'rollback-cancel-0001',
			'rollback-resume-0001'
		)`).Scan(&governedReceipts); err != nil {
		t.Fatal(err)
	}
	if governedReceipts != 1 {
		t.Fatalf("matching receipts after rollback = %d, want the one generic receipt", governedReceipts)
	}
	var preservedGenericReceipts int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM forja.idempotency_keys
		WHERE scope=$1 AND idempotency_key=$2`,
		"transition_run:"+DefaultRepositoryID+":"+genericRun.RunID,
		genericTransitionMetadata.IdempotencyKey,
	).Scan(&preservedGenericReceipts); err != nil {
		t.Fatal(err)
	}
	if preservedGenericReceipts != 1 {
		t.Fatalf("preserved generic transition receipts = %d, want 1", preservedGenericReceipts)
	}
	var invalidationMarkers int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM forja.events
		WHERE aggregate_type='projection'
		  AND event_type='idempotency.receipt_invalidated'`).Scan(&invalidationMarkers); err != nil {
		t.Fatal(err)
	}
	if invalidationMarkers != 9 {
		t.Fatalf("receipt invalidation markers = %d, want 9 event-specific markers", invalidationMarkers)
	}
	if _, err := pool.Exec(t.Context(), "DELETE FROM forja.events"); err == nil {
		t.Fatal("append-only trigger was not restored by rollback")
	}
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatalf("reapply governed migration: %v", err)
	}
	if err := VerifySchema(t.Context(), pool, DefaultTenantID, DefaultRepositoryID); err != nil {
		t.Fatalf("verify schema after rollback and reapply: %v", err)
	}
	verify := postgresScriptCommand(t, "../../scripts/postgres_verify.sh")
	if output, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("verify rollback/re-upgrade state: %v\n%s", err, output)
	}
	restoredSprint, err := store.GetSprint(t.Context(), identity.SprintID(planned.Sprint.SprintID))
	if err != nil {
		t.Fatalf("read Sprint after rollback and re-upgrade: %v", err)
	}
	if restoredSprint.Objective != planned.Sprint.Objective {
		t.Fatalf(
			"Sprint objective after re-upgrade = %q, want %q",
			restoredSprint.Objective,
			planned.Sprint.Objective,
		)
	}
	replayedGeneric, err := store.TransitionRun(
		t.Context(),
		genericRunID,
		1,
		runstate.StateAwaitingApproval,
		genericTransitionMetadata,
	)
	if err != nil {
		t.Fatalf("replay preserved generic transition: %v", err)
	}
	if replayedGeneric != genericRun {
		t.Fatalf("generic replay changed across rollback: got %#v want %#v", replayedGeneric, genericRun)
	}
	if _, err := service.SubmitSprint(t.Context(), principal, submitInput); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("stale submit receipt survived rollback/re-upgrade: %v", err)
	}
	if _, err := pool.Exec(
		t.Context(),
		"ALTER TABLE forja.events DISABLE TRIGGER events_are_append_only",
	); err != nil {
		t.Fatalf("disable event mutation guard: %v", err)
	}
	corruptScope := "resume_run:" + DefaultRepositoryID + ":" + planned.Run.RunID
	if _, err := pool.Exec(t.Context(), `
		UPDATE forja.events AS marker
		SET payload=jsonb_set(marker.payload, '{scope}', to_jsonb($1::text))
		WHERE marker.event_id=(
			SELECT candidate.event_id
			FROM forja.events AS candidate
			JOIN forja.events AS domain
			  ON domain.event_id=candidate.payload->>'domain_event_id'
			WHERE candidate.event_type='idempotency.receipt_invalidated'
			  AND domain.event_type='sprint.planned'
			LIMIT 1
		)`, corruptScope); err != nil {
		t.Fatalf("corrupt invalidation scope: %v", err)
	}
	if _, err := pool.Exec(
		t.Context(),
		"ALTER TABLE forja.events ENABLE TRIGGER events_are_append_only",
	); err != nil {
		t.Fatalf("restore event mutation guard: %v", err)
	}
	verify = postgresScriptCommand(t, "../../scripts/postgres_verify.sh")
	if output, err := verify.CombinedOutput(); err == nil {
		t.Fatalf("verification accepted a scope-swapped receipt invalidation\n%s", output)
	}
}

func TestGovernedRollbackSupportsScopedIdempotencyKeyReuse(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(pool, nil, DefaultTenantID, DefaultRepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	service, err := control.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := control.NewPrincipal("agent", "scoped-rollback", control.AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	for index := range 2 {
		planned, planErr := service.PlanSprint(t.Context(), principal, control.PlanSprintInput{
			Title:     fmt.Sprintf("Scoped rollback %d", index),
			Objective: "Keep legal aggregate-scoped idempotency reuse unambiguous",
			Command: control.CommandContext{
				IdempotencyKey: fmt.Sprintf("scoped-rollback-plan-%d", index),
				CorrelationID:  fmt.Sprintf("corr-scoped-rollback-plan-%d", index),
			},
		})
		if planErr != nil {
			t.Fatal(planErr)
		}
		submitted, submitErr := service.SubmitSprint(t.Context(), principal, control.SubmitSprintInput{
			SprintID: planned.Sprint.SprintID, ExpectedVersion: planned.Sprint.Version, RiskClass: "low",
			Command: control.CommandContext{
				IdempotencyKey: "shared-submit-key",
				CorrelationID:  "shared-submit-correlation",
			},
		})
		if submitErr != nil {
			t.Fatal(submitErr)
		}
		if _, resolveErr := service.ResolveDecision(t.Context(), principal, control.ResolveDecisionInput{
			DecisionID: submitted.Decision.DecisionID, ExpectedVersion: submitted.Decision.Version,
			Reason: "Resolve before exercising rollback",
			Command: control.CommandContext{
				IdempotencyKey: "shared-submit-key",
				CorrelationID:  "shared-submit-correlation",
			},
		}, true); resolveErr != nil {
			t.Fatal(resolveErr)
		}
	}
	if err := RollbackLast(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var sharedMarkers, ambiguousDomainEvents int
	if err := pool.QueryRow(t.Context(), `
		WITH markers AS (
			SELECT payload->>'domain_event_id' AS domain_event_id
			FROM forja.events
			WHERE event_type='idempotency.receipt_invalidated'
			  AND idempotency_key='shared-submit-key'
		), counts AS (
			SELECT domain_event_id, count(*) AS marker_count
			FROM markers
			GROUP BY domain_event_id
		)
		SELECT (SELECT count(*) FROM markers),
		       (SELECT count(*) FROM counts WHERE marker_count <> 1)`).Scan(
		&sharedMarkers,
		&ambiguousDomainEvents,
	); err != nil {
		t.Fatal(err)
	}
	if sharedMarkers != 8 || ambiguousDomainEvents != 0 {
		t.Fatalf("shared-key markers=%d ambiguous events=%d, want 8 and 0", sharedMarkers, ambiguousDomainEvents)
	}
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	verify := postgresScriptCommand(t, "../../scripts/postgres_verify.sh")
	if output, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("verify scoped idempotency reuse after rollback: %v\n%s", err, output)
	}
	if _, err := pool.Exec(
		t.Context(),
		"ALTER TABLE forja.events DISABLE TRIGGER events_are_append_only",
	); err != nil {
		t.Fatalf("disable event mutation guard: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		WITH submit_markers AS (
			SELECT event_id, payload->>'scope' AS scope
			FROM forja.events
			WHERE event_type='idempotency.receipt_invalidated'
			  AND idempotency_key='shared-submit-key'
			  AND payload->>'scope' LIKE 'submit_sprint:%'
		), target AS (
			SELECT event_id, scope
			FROM submit_markers
			ORDER BY event_id
			LIMIT 1
		), replacement AS (
			SELECT scope
			FROM submit_markers
			WHERE scope <> (SELECT scope FROM target)
			ORDER BY event_id
			LIMIT 1
		)
		UPDATE forja.events AS marker
		SET payload=jsonb_set(
			marker.payload,
			'{scope}',
			to_jsonb((SELECT scope FROM replacement))
		)
		WHERE marker.event_id=(SELECT event_id FROM target)`); err != nil {
		t.Fatalf("swap same-command invalidation scope: %v", err)
	}
	if _, err := pool.Exec(
		t.Context(),
		"ALTER TABLE forja.events ENABLE TRIGGER events_are_append_only",
	); err != nil {
		t.Fatalf("restore event mutation guard: %v", err)
	}
	verify = postgresScriptCommand(t, "../../scripts/postgres_verify.sh")
	if output, err := verify.CombinedOutput(); err == nil {
		t.Fatalf("verification accepted a same-command scope swap\n%s", output)
	}
}

func TestGovernedMigrationRollbackRefusesPendingDecision(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(pool, nil, DefaultTenantID, DefaultRepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	service, err := control.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := control.NewPrincipal("agent", "rollback-guard", control.AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.PlanSprint(t.Context(), principal, control.PlanSprintInput{
		Title: "Pending rollback guard", Objective: "Keep pending governance durable across rollback attempts",
		Command: control.CommandContext{IdempotencyKey: "rollback-guard-plan", CorrelationID: "corr-rollback-guard-plan"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SubmitSprint(t.Context(), principal, control.SubmitSprintInput{
		SprintID: planned.Sprint.SprintID, ExpectedVersion: planned.Sprint.Version, RiskClass: "critical",
		Command: control.CommandContext{IdempotencyKey: "rollback-guard-submit", CorrelationID: "corr-rollback-guard-submit"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := RollbackLast(t.Context(), pool); err == nil {
		t.Fatal("rollback discarded a pending decision")
	}
	if err := VerifySchema(t.Context(), pool, DefaultTenantID, DefaultRepositoryID); err != nil {
		t.Fatalf("failed rollback changed the current schema: %v", err)
	}
	var pending int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM forja.decisions WHERE status='pending'").Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("pending decisions after refused rollback = %d", pending)
	}
}

func TestGovernedRollbackWaitsAtCommandBarrierBeforeParentLocks(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(pool, nil, DefaultTenantID, DefaultRepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	service, err := control.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := control.NewPrincipal(
		"agent",
		"rollback-lock-order",
		control.AllPermissions...,
	)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.PlanSprint(t.Context(), principal, control.PlanSprintInput{
		Title:     "Rollback lock order",
		Objective: "Make the command barrier precede parent-table rollback locks",
		Command: control.CommandContext{
			IdempotencyKey: "rollback-lock-order-plan",
			CorrelationID:  "corr-rollback-lock-order-plan",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sprintID, err := identity.ParseSprintID(planned.Sprint.SprintID)
	if err != nil {
		t.Fatal(err)
	}

	writer, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	writerOpen := true
	defer func() {
		if writerOpen {
			_ = writer.Rollback(t.Context())
		}
	}()
	if _, err := writer.Exec(
		t.Context(),
		"SELECT count(*) FROM forja.idempotency_keys",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(t.Context(), `
		SELECT 1 FROM forja.sprints
		WHERE tenant_id=$1 AND repository_id=$2 AND sprint_id=$3::uuid
		FOR UPDATE`, DefaultTenantID, DefaultRepositoryID, sprintID.UUID()); err != nil {
		t.Fatal(err)
	}

	rollbackContext, cancelRollback := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelRollback()
	rollbackResult := make(chan error, 1)
	go func() { rollbackResult <- RollbackLast(rollbackContext, pool) }()
	waitForLockQuery(t, pool, "%LOCK TABLE%forja.idempotency_keys%")

	writerContext, cancelWriter := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancelWriter()
	if _, err := writer.Exec(writerContext, `
		INSERT INTO forja.events (
			event_id, tenant_id, repository_id, aggregate_type, aggregate_id,
			aggregate_version, event_type, schema_version, occurred_at,
			actor_type, actor_id, correlation_id, causation_id,
			idempotency_key, payload
		) VALUES (
			'event_rollback_lock_order_probe', $1, $2, 'projection',
			'rollback_lock_order_probe', 1, 'rollback.lock_order.probed', '1.0',
			clock_timestamp(), 'system', 'rollback-lock-order-test',
			'corr-rollback-lock-order-probe', NULL,
			'rollback-lock-order-probe', '{}'::jsonb
		)`, DefaultTenantID, DefaultRepositoryID); err != nil {
		t.Fatalf("writer deadlocked behind rollback parent locks: %v", err)
	}
	if err := writer.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	writerOpen = false
	if err := <-rollbackResult; err != nil {
		t.Fatalf("rollback after command barrier release: %v", err)
	}
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if err := VerifySchema(
		t.Context(),
		pool,
		DefaultTenantID,
		DefaultRepositoryID,
	); err != nil {
		t.Fatal(err)
	}
}

func TestGovernedRollbackLocksCommandWritersBeforeSafetyCheck(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(pool, nil, DefaultTenantID, DefaultRepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	service, err := control.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := control.NewPrincipal("agent", "rollback-writer-race", control.AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.PlanSprint(t.Context(), principal, control.PlanSprintInput{
		Title: "Rollback writer barrier", Objective: "Prove rollback checks safety only after command writers quiesce",
		Command: control.CommandContext{IdempotencyKey: "rollback-race-plan", CorrelationID: "corr-rollback-race-plan"},
	})
	if err != nil {
		t.Fatal(err)
	}

	blocker, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	blockerOpen := true
	defer func() {
		if blockerOpen {
			_ = blocker.Rollback(t.Context())
		}
	}()
	if _, err := blocker.Exec(t.Context(), `
		LOCK TABLE forja.projection_checkpoints IN ACCESS SHARE MODE`); err != nil {
		t.Fatal(err)
	}

	rollbackContext, cancelRollback := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelRollback()
	rollbackResult := make(chan error, 1)
	go func() { rollbackResult <- RollbackLast(rollbackContext, pool) }()
	waitForLockQuery(t, pool, "%LOCK TABLE%forja.projection_checkpoints%")

	submitResult := make(chan error, 1)
	go func() {
		_, submitErr := service.SubmitSprint(rollbackContext, principal, control.SubmitSprintInput{
			SprintID: planned.Sprint.SprintID, ExpectedVersion: planned.Sprint.Version, RiskClass: "high",
			Command: control.CommandContext{IdempotencyKey: "rollback-race-submit", CorrelationID: "corr-rollback-race-submit"},
		})
		submitResult <- submitErr
	}()
	waitForLockQuery(
		t,
		pool,
		"%LOCK TABLE forja.idempotency_keys IN ACCESS SHARE MODE%",
	)

	if err := blocker.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	blockerOpen = false
	if err := <-rollbackResult; err != nil {
		t.Fatalf("rollback after writer barrier: %v", err)
	}
	if err := <-submitResult; err == nil {
		t.Fatal("command writer committed through governed rollback")
	}
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if err := VerifySchema(t.Context(), pool, DefaultTenantID, DefaultRepositoryID); err != nil {
		t.Fatal(err)
	}
}

func waitForLockQuery(t *testing.T, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, pattern string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(t.Context(), `
			SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity
				WHERE pid <> pg_backend_pid()
				  AND datname=current_database()
				  AND wait_event_type='Lock'
				  AND query LIKE $1
			)`, pattern).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("query matching %q did not block on the rollback barrier", pattern)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func callMCPRaw(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return result
}

func callMCPTool[T any](t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) T {
	t.Helper()
	result := callMCPRaw(t, session, name, arguments)
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
		t.Fatalf("decode %s: %v", name, err)
	}
	return output
}
