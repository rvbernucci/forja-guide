package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/delivery"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/postgres"
	"github.com/rvbernucci/forja-guide/internal/runstate"
	"github.com/rvbernucci/forja-guide/internal/worker"
)

func TestPipelineApprovedSprintRunsRealWorkerAndPublishes(t *testing.T) {
	databaseURL := os.Getenv("FORJA_TEST_DELIVERY_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("FORJA_TEST_DELIVERY_DATABASE_URL is not set")
	}
	pool, err := postgres.Open(t.Context(), databaseURL, 8)
	if err != nil {
		t.Fatalf("open execution integration database: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(t.Context(), "DROP SCHEMA IF EXISTS forja CASCADE"); err != nil {
		t.Fatalf("reset execution integration database: %v", err)
	}
	if err := postgres.Migrate(t.Context(), pool); err != nil {
		t.Fatalf("migrate execution integration database: %v", err)
	}
	store, err := postgres.NewStore(
		pool, nil, postgres.DefaultTenantID, postgres.DefaultRepositoryID,
	)
	if err != nil {
		t.Fatal(err)
	}

	const objective = "Produce one approved, bounded, independently validated fixture change."
	approved := approveExecutionSprint(t, store, objective)
	runID, err := identity.ParseRunID(approved.Run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	schedulerLease, err := store.AcquireLease(t.Context(), persistence.LeaseKey{
		TenantID: postgres.DefaultTenantID, RepositoryID: postgres.DefaultRepositoryID,
		ResourceType: "scheduler", ResourceID: "execution-pipeline-e2e",
	}, "scheduler-e2e", 2*time.Minute)
	if err != nil {
		t.Fatalf("acquire scheduler fence: %v", err)
	}
	schedulerFence := persistence.LeaseProof{
		LeaseKey: schedulerLease.LeaseKey,
		OwnerID:  schedulerLease.OwnerID, FencingToken: schedulerLease.FencingToken,
	}
	attempt, err := store.CreateAttempt(
		t.Context(), runID, "queued", executionMetadata("create-attempt"), schedulerFence,
	)
	if err != nil {
		t.Fatalf("create approved durable attempt: %v", err)
	}

	repository, worktreeRoot, evidenceRoot, base := executionRepository(t)
	request := executionDeliveryRequest(
		repository, worktreeRoot, base, approved.Run.RunID,
		attempt.AttemptID, attempt.Ordinal, objective,
	)
	runGit(t, repository, "update-ref", request.PublicationRef, base)
	approvalService, err := NewApprovalService(store)
	if err != nil {
		t.Fatal(err)
	}
	deliveryApprover, err := control.NewPrincipal(
		"human", "delivery-approver", control.PermissionDecide,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := approvalService.Approve(
		t.Context(),
		deliveryApprover,
		ApproveDeliveryInput{
			Request: request,
			Command: control.CommandContext{
				IdempotencyKey: "execution-e2e-authorize-delivery",
				CorrelationID:  "execution-e2e",
			},
		},
	); err != nil {
		t.Fatalf("authorize exact delivery request: %v", err)
	}
	if _, err := approvalService.Approve(
		t.Context(),
		deliveryApprover,
		ApproveDeliveryInput{
			Request: request,
			Command: control.CommandContext{
				IdempotencyKey: "execution-e2e-authorize-delivery",
				CorrelationID:  "execution-e2e-replay",
			},
		},
	); err != nil {
		t.Fatalf("replay exact delivery authorization: %v", err)
	}
	var authorizationReplayAudits int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM forja.events
		WHERE tenant_id=$1 AND repository_id=$2
		  AND aggregate_type='audit' AND event_type='mcp.tool.succeeded'
		  AND payload->>'tool_name'='forja.authorize_delivery'
		  AND payload->>'replay'='true'`,
		postgres.DefaultTenantID, postgres.DefaultRepositoryID,
	).Scan(&authorizationReplayAudits); err != nil {
		t.Fatalf("count authorization replay audits: %v", err)
	}
	if authorizationReplayAudits != 1 {
		t.Fatalf("authorization replay audits = %d, want 1", authorizationReplayAudits)
	}

	manager, err := delivery.NewWorktreeManager("git", nil)
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := worker.NewSupervisor(
		schemas,
		executionProcessAdapter{executable: executable},
		executionIsolationPolicy{},
		nil,
		[]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()},
	)
	if err != nil {
		t.Fatal(err)
	}
	validatorRegistry, err := delivery.NewValidatorRegistry([]delivery.ValidatorDefinition{{
		ID: "unit-tests",
		Argv: []string{
			executable, "-test.run=^TestPipelineValidatorHelperProcess$", "--", "pass",
		},
		Timeout: 5 * time.Second, MaxOutputBytes: 4096,
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := validatorRegistry.Close(); err != nil {
			t.Errorf("close validator registry: %v", err)
		}
	})
	validation, err := delivery.NewValidationService(
		manager, validatorRegistry, evidenceRoot, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := delivery.NewPublicationService(
		manager,
		store,
		store,
		delivery.RepositoryAuthority{
			TenantID: request.TenantID, RepositoryID: request.RepositoryID,
			RepositoryPath: request.RepositoryPath,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	pipeline, err := NewPipeline(
		store, manager, supervisor, validation, publication, "delivery-pipeline-e2e",
	)
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := pipeline.Execute(t.Context(), request, schedulerFence)
	if err != nil {
		t.Fatalf("execute approved delivery: %v", err)
	}
	if outcome.Run.State != string(runstate.StateCompleted) ||
		outcome.Attempt.Status != "succeeded" ||
		outcome.Worker.Status != "succeeded" ||
		outcome.Validation.Report.Status != "passed" ||
		outcome.Publication.Receipt.Status != "published" ||
		!outcome.Publication.LeaseReleased ||
		outcome.Quarantine != nil {
		t.Fatalf("unexpected pipeline outcome: %#v", outcome)
	}
	if !slices.Equal(outcome.Commit.ChangedPaths, []string{"internal/generated/value.json"}) {
		t.Fatalf("published changed paths = %q", outcome.Commit.ChangedPaths)
	}
	content := runGit(
		t,
		repository,
		"show",
		outcome.Commit.ResultCommit+":internal/generated/value.json",
	)
	if content != "{\"approved\":true}\n" {
		t.Fatalf("published worker content = %q", content)
	}
	if ref := strings.TrimSpace(runGit(t, repository, "rev-parse", request.PublicationRef)); ref != outcome.Commit.ResultCommit {
		t.Fatalf("published ref = %s, want %s", ref, outcome.Commit.ResultCommit)
	}
	durableRun, err := store.GetRun(t.Context(), runID)
	if err != nil || durableRun.State != string(runstate.StateCompleted) {
		t.Fatalf("durable run = %#v, err=%v", durableRun, err)
	}
	durableAttempt, err := store.GetAttempt(t.Context(), request.AttemptID)
	if err != nil || durableAttempt.Status != "succeeded" || durableAttempt.FinishedAt == nil {
		t.Fatalf("durable attempt = %#v, err=%v", durableAttempt, err)
	}
	publicationRecord, found, err := store.GetDeliveryPublication(
		t.Context(), request.DeliveryID, request.AttemptID,
	)
	if err != nil || !found || publicationRecord.State != "published" {
		t.Fatalf("durable publication = %#v, found=%v, err=%v", publicationRecord, found, err)
	}
	var leaseState string
	if err := pool.QueryRow(t.Context(), `
		SELECT state FROM forja.lease_sets
		WHERE tenant_id=$1 AND repository_id=$2 AND lease_set_id=$3`,
		postgres.DefaultTenantID,
		postgres.DefaultRepositoryID,
		request.AttemptID,
	).Scan(&leaseState); err != nil {
		t.Fatalf("read delivery lease state: %v", err)
	}
	if leaseState != "released" {
		t.Fatalf("delivery lease state = %q, want released", leaseState)
	}
	replayed, err := pipeline.Recover(t.Context(), request, schedulerFence)
	if err != nil {
		t.Fatalf("replay completed pipeline through publisher: %v", err)
	}
	if !replayed.Publication.Replayed || !replayed.Publication.LeaseReleased ||
		replayed.Publication.Receipt.ResultCommit != outcome.Commit.ResultCommit {
		t.Fatalf("completed pipeline replay = %#v", replayed)
	}
	runGit(t, repository, "update-ref", request.PublicationRef, base, outcome.Commit.ResultCommit)
	if _, err := pipeline.Recover(t.Context(), request, schedulerFence); !errors.Is(err, delivery.ErrPublicationConflict) {
		t.Fatalf("completed recovery accepted a moved publication ref: %v", err)
	}
	runGit(t, repository, "update-ref", request.PublicationRef, outcome.Commit.ResultCommit, base)
	if err := postgres.VerifySchema(
		t.Context(), pool, postgres.DefaultTenantID, postgres.DefaultRepositoryID,
	); err != nil {
		t.Fatalf("verify canonical state after execution: %v", err)
	}
	if output, err := verifyExecutionPostgresState(t, databaseURL); err != nil {
		t.Fatalf("verify delivery authorization archive: %v\n%s", err, output)
	}

	if _, err := pool.Exec(t.Context(),
		"ALTER TABLE forja.events DISABLE TRIGGER events_are_append_only",
	); err != nil {
		t.Fatalf("disable event mutation guard: %v", err)
	}
	triggerDisabled := true
	t.Cleanup(func() {
		if triggerDisabled {
			_, _ = pool.Exec(
				context.Background(),
				"ALTER TABLE forja.events ENABLE TRIGGER events_are_append_only",
			)
		}
	})
	if _, err := pool.Exec(t.Context(), `
		UPDATE forja.events
		SET payload=jsonb_set(payload, '{request,objective}', to_jsonb($1::text))
		WHERE tenant_id=$2 AND repository_id=$3
		  AND aggregate_type='approval' AND aggregate_id=$4
		  AND event_type='delivery.authorized'`,
		"tampered delivery authority",
		postgres.DefaultTenantID,
		postgres.DefaultRepositoryID,
		request.DeliveryID+":"+request.AttemptID,
	); err != nil {
		t.Fatalf("corrupt delivery authorization: %v", err)
	}
	if _, err := pool.Exec(t.Context(),
		"ALTER TABLE forja.events ENABLE TRIGGER events_are_append_only",
	); err != nil {
		t.Fatalf("restore event mutation guard: %v", err)
	}
	triggerDisabled = false
	if output, err := verifyExecutionPostgresState(t, databaseURL); err == nil {
		t.Fatalf("archive verification accepted a tampered delivery authorization\n%s", output)
	}
}

func TestDeliveryAuthorizationSupportsNewAttemptForSameDelivery(t *testing.T) {
	databaseURL := os.Getenv("FORJA_TEST_DELIVERY_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("FORJA_TEST_DELIVERY_DATABASE_URL is not set")
	}
	pool, err := postgres.Open(t.Context(), databaseURL, 8)
	if err != nil {
		t.Fatalf("open execution integration database: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(t.Context(), "DROP SCHEMA IF EXISTS forja CASCADE"); err != nil {
		t.Fatalf("reset execution integration database: %v", err)
	}
	if err := postgres.Migrate(t.Context(), pool); err != nil {
		t.Fatalf("migrate execution integration database: %v", err)
	}
	store, err := postgres.NewStore(
		pool, nil, postgres.DefaultTenantID, postgres.DefaultRepositoryID,
	)
	if err != nil {
		t.Fatal(err)
	}

	const objective = "Authorize independent attempts for one stable delivery identity."
	approved := approveExecutionSprint(t, store, objective)
	runID, err := identity.ParseRunID(approved.Run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	leaseKey := persistence.LeaseKey{
		TenantID: postgres.DefaultTenantID, RepositoryID: postgres.DefaultRepositoryID,
		ResourceType: "scheduler", ResourceID: "authorization-retry-e2e",
	}
	firstLease, err := store.AcquireLease(t.Context(), leaseKey, "scheduler-first", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	firstFence := persistence.LeaseProof{
		LeaseKey: leaseKey, OwnerID: firstLease.OwnerID, FencingToken: firstLease.FencingToken,
	}
	firstAttempt, err := store.CreateAttempt(
		t.Context(), runID, "queued", executionMetadata("retry-attempt-first"), firstFence,
	)
	if err != nil {
		t.Fatal(err)
	}
	repository, worktreeRoot, _, base := executionRepository(t)
	firstRequest := executionDeliveryRequest(
		repository, worktreeRoot, base, approved.Run.RunID,
		firstAttempt.AttemptID, firstAttempt.Ordinal, objective,
	)
	approvalService, err := NewApprovalService(store)
	if err != nil {
		t.Fatal(err)
	}
	approver, err := control.NewPrincipal(
		"human", "delivery-retry-approver", control.PermissionDecide,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := approvalService.Approve(t.Context(), approver, ApproveDeliveryInput{
		Request: firstRequest,
		Command: control.CommandContext{
			IdempotencyKey: "authorize-retry-first", CorrelationID: "authorize-retry",
		},
	}); err != nil {
		t.Fatalf("authorize first attempt: %v", err)
	}
	if err := store.ReleaseLease(
		t.Context(), leaseKey, firstLease.OwnerID, firstLease.FencingToken,
	); err != nil {
		t.Fatalf("release first scheduler lease: %v", err)
	}
	secondLease, err := store.AcquireLease(t.Context(), leaseKey, "scheduler-second", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	secondFence := persistence.LeaseProof{
		LeaseKey: leaseKey, OwnerID: secondLease.OwnerID, FencingToken: secondLease.FencingToken,
	}
	if recovered, err := store.ReconcileAbandonedAttempts(
		t.Context(), executionMetadata("retry-reconcile-first"), secondFence,
	); err != nil || len(recovered) != 1 || recovered[0].AttemptID != firstAttempt.AttemptID {
		t.Fatalf("reconcile first attempt = %#v, err=%v", recovered, err)
	}
	secondAttempt, err := store.CreateAttempt(
		t.Context(), runID, "queued", executionMetadata("retry-attempt-second"), secondFence,
	)
	if err != nil {
		t.Fatalf("create second attempt: %v", err)
	}
	secondRequest := executionDeliveryRequest(
		repository, worktreeRoot, base, approved.Run.RunID,
		secondAttempt.AttemptID, secondAttempt.Ordinal, objective,
	)
	if secondRequest.DeliveryID != firstRequest.DeliveryID {
		t.Fatal("retry fixture changed the delivery identity")
	}
	if _, err := approvalService.Approve(t.Context(), approver, ApproveDeliveryInput{
		Request: secondRequest,
		Command: control.CommandContext{
			IdempotencyKey: "authorize-retry-second", CorrelationID: "authorize-retry",
		},
	}); err != nil {
		t.Fatalf("authorize second attempt for same delivery: %v", err)
	}
	for _, request := range []contracts.DeliveryRequest{firstRequest, secondRequest} {
		authorization, found, err := store.GetDeliveryAuthorization(
			t.Context(), request.DeliveryID, request.AttemptID,
		)
		if err != nil || !found || authorization.Request.AttemptID != request.AttemptID {
			t.Fatalf("load attempt authorization = %#v, found=%v, err=%v", authorization, found, err)
		}
	}
	var authorizationCount int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*) FROM forja.events
		WHERE aggregate_type='approval' AND event_type='delivery.authorized'
		  AND aggregate_id LIKE $1`,
		firstRequest.DeliveryID+":%",
	).Scan(&authorizationCount); err != nil {
		t.Fatal(err)
	}
	if authorizationCount != 2 {
		t.Fatalf("delivery authorization events = %d, want 2", authorizationCount)
	}
	if output, err := verifyExecutionPostgresState(t, databaseURL); err != nil {
		t.Fatalf("verify multi-attempt authorization archive: %v\n%s", err, output)
	}
}

func approveExecutionSprint(
	t *testing.T,
	store *postgres.Store,
	objective string,
) control.DecisionResult {
	t.Helper()
	service, err := control.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	planner, err := control.NewPrincipal("agent", "co-architect", control.AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	approver, err := control.NewPrincipal("human", "independent-approver", control.AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := service.PlanSprint(t.Context(), planner, control.PlanSprintInput{
		Title:     "Execution pipeline integration",
		Objective: objective,
		Command: control.CommandContext{
			IdempotencyKey: "execution-e2e-plan", CorrelationID: "execution-e2e",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := service.SubmitSprint(t.Context(), planner, control.SubmitSprintInput{
		SprintID:        planned.Sprint.SprintID,
		ExpectedVersion: planned.Sprint.Version,
		RiskClass:       "medium",
		Command: control.CommandContext{
			IdempotencyKey: "execution-e2e-submit", CorrelationID: "execution-e2e",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := service.ResolveDecision(
		t.Context(),
		approver,
		control.ResolveDecisionInput{
			DecisionID:      submitted.Decision.DecisionID,
			ExpectedVersion: submitted.Decision.Version,
			Reason:          "The bounded scopes and validators are approved.",
			Command: control.CommandContext{
				IdempotencyKey: "execution-e2e-approve", CorrelationID: "execution-e2e",
			},
		},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Run.State != string(runstate.StateQueued) {
		t.Fatalf("approved run state = %q", approved.Run.State)
	}
	return approved
}

func executionRepository(t *testing.T) (string, string, string, string) {
	t.Helper()
	repository := t.TempDir()
	worktreeRoot := t.TempDir()
	evidenceRoot := t.TempDir()
	runGit(t, repository, "init", "--quiet")
	runGit(t, repository, "config", "user.name", "Forja Test")
	runGit(t, repository, "config", "user.email", "forja-test@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("# fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repository, "internal"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "internal", ".keep"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "README.md", "internal/.keep")
	runGit(t, repository, "commit", "--quiet", "-m", "initial")
	base := strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD"))
	return repository, worktreeRoot, evidenceRoot, base
}

func executionDeliveryRequest(
	repository string,
	worktreeRoot string,
	base string,
	runID string,
	attemptID string,
	attemptOrdinal int,
	objective string,
) contracts.DeliveryRequest {
	previous := base
	deliveryID := "delivery_11111111-1111-4111-8111-111111111111"
	return contracts.DeliveryRequest{
		DeliveryID:                deliveryID,
		TenantID:                  "tenant_" + postgres.DefaultTenantID,
		RepositoryID:              "repo_" + postgres.DefaultRepositoryID,
		TaskID:                    "task_11111111-1111-4111-8111-111111111111",
		AttemptID:                 attemptID,
		RunID:                     runID,
		SchemaVersion:             contracts.DeliverySchemaVersion,
		RepositoryPath:            repository,
		WorktreeRoot:              worktreeRoot,
		BaseCommit:                base,
		PublicationRef:            "refs/forja/deliveries/" + deliveryID,
		PublicationPreviousCommit: &previous,
		AuthorID:                  "worker-author",
		ValidatorID:               "independent-validator",
		Role:                      "implementer",
		Objective:                 objective,
		ReadScopes:                []string{"."},
		WriteScopes:               []string{"internal/generated"},
		ArtifactScopes:            []string{"evidence"},
		EvidenceScope:             "evidence",
		AttemptOrdinal:            attemptOrdinal,
		WorkerBudgets: contracts.WorkerBudgets{
			WallClockMS:         5_000,
			InactivityMS:        2_000,
			MaxOutputBytes:      4_096,
			CancellationGraceMS: 100,
			MaxRetries:          2,
		},
		MechanicalValidatorIDs: []string{"unit-tests"},
		LeaseTTLMS:             120_000,
	}
}

type executionProcessAdapter struct {
	executable string
}

func (executionProcessAdapter) Name() string { return "execution-test-process" }

func (executionProcessAdapter) IsolationCapability() worker.IsolationCapability {
	return worker.IsolationCapability{
		PolicyID: "execution-test-process-v1", Version: "1.0",
		ReadBoundary: "full-worktree", WriteBoundary: "declared-roots",
		NetworkBoundary: "denied",
	}
}

func (a executionProcessAdapter) Build(
	task contracts.WorkerTask,
	paths worker.ExecutionPaths,
) (worker.Invocation, error) {
	return worker.Invocation{
		Path: a.executable,
		Args: []string{
			"-test.run=^TestPipelineWorkerHelperProcess$", "--", paths.ReportPath,
		},
		Dir:   task.WorktreePath,
		Stdin: task.Objective,
	}, nil
}

func (executionProcessAdapter) ParseUsage([]byte) contracts.WorkerUsage {
	return contracts.WorkerUsage{}
}

func (executionProcessAdapter) RetryableFailure(int, string) bool { return false }

type executionIsolationPolicy struct{}

func (executionIsolationPolicy) ID() string { return "execution-test-process-v1" }

func (executionIsolationPolicy) Verify(
	task contracts.WorkerTask,
	paths worker.ExecutionPaths,
	invocation worker.Invocation,
) error {
	if invocation.Dir != task.WorktreePath || invocation.Path == "" ||
		!slices.Contains(invocation.Args, paths.ReportPath) {
		return fmt.Errorf("execution test invocation escaped its approved boundary")
	}
	return nil
}

func TestPipelineWorkerHelperProcess(t *testing.T) {
	separator := slices.Index(os.Args, "--")
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	reportPath := os.Args[separator+1]
	if err := os.WriteFile(
		filepath.Join("internal", "generated", "value.json"),
		[]byte("{\"approved\":true}\n"),
		0o600,
	); err != nil {
		os.Exit(10)
	}
	report, err := json.Marshal(contracts.WorkerReport{
		Status:       "completed",
		Summary:      "approved execution fixture completed",
		ChangedPaths: []string{"internal/generated/value.json"},
		EvidenceRefs: []string{},
		Risks:        []string{},
	})
	if err != nil || os.WriteFile(reportPath, report, 0o600) != nil {
		os.Exit(11)
	}
}

func TestPipelineValidatorHelperProcess(t *testing.T) {
	separator := slices.Index(os.Args, "--")
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	if os.Args[separator+1] != "pass" {
		os.Exit(12)
	}
	fmt.Println("passed")
}

func executionMetadata(stage string) runstate.CommandMetadata {
	return runstate.CommandMetadata{
		IdempotencyKey: "execution-e2e-" + stage,
		ActorType:      "system",
		ActorID:        "scheduler-e2e",
		CorrelationID:  "execution-e2e",
	}
}

func verifyExecutionPostgresState(t *testing.T, databaseURL string) ([]byte, error) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "../../scripts/postgres_verify.sh")
	command.Env = append(os.Environ(), "FORJA_DATABASE_URL="+databaseURL)
	return command.CombinedOutput()
}

func runGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(
		t.Context(), "git", append([]string{"-C", directory}, arguments...)...,
	)
	command.Env = append(
		os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

var _ worker.Adapter = executionProcessAdapter{}
var _ worker.InvocationIsolationPolicy = executionIsolationPolicy{}
