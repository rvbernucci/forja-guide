package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/delivery"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/observability"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

func TestPipelineRejectsUnapprovedOrMismatchedAuthorityBeforeSideEffects(t *testing.T) {
	request := pipelineRequestFixture(t)
	fence := persistence.LeaseProof{
		LeaseKey: persistence.LeaseKey{
			TenantID: control.LocalTenantID, RepositoryID: control.LocalRepositoryID,
			ResourceType: "scheduler", ResourceID: "scheduler-test",
		},
		OwnerID: "scheduler-owner", FencingToken: 7,
	}
	baseRun := contracts.Run{
		RunID: request.RunID, Objective: request.Objective,
		State: string(runstate.StateQueued), Version: 3,
	}
	baseAttempt := persistence.Attempt{
		AttemptID: request.AttemptID, RunID: request.RunID, Ordinal: request.AttemptOrdinal,
		Status: "queued", Version: 1,
		LeaseResourceType: fence.ResourceType, LeaseResourceID: fence.ResourceID,
		WorkerID: fence.OwnerID, FencingToken: fence.FencingToken,
	}
	digest, err := deliveryRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	baseAuthorization := persistence.DeliveryAuthorization{
		Request: request, RequestSHA256: digest,
		ApprovedBy: "independent-human", ApprovedAt: time.Now().UTC(),
	}

	tests := []struct {
		name      string
		configure func(*pipelineRepositoryStub)
		want      string
	}{
		{
			name: "run awaits approval",
			configure: func(repository *pipelineRepositoryStub) {
				repository.run.State = string(runstate.StateAwaitingApproval)
			},
			want: "approved queued run",
		},
		{
			name: "objective differs from approval",
			configure: func(repository *pipelineRepositoryStub) {
				repository.run.Objective = "A different objective"
			},
			want: "approved queued run",
		},
		{
			name: "attempt belongs to another scheduler fence",
			configure: func(repository *pipelineRepositoryStub) {
				repository.attempt.FencingToken++
			},
			want: "exact queued scheduler attempt",
		},
		{
			name: "repository authority differs",
			configure: func(repository *pipelineRepositoryStub) {
				repository.authority.RepositoryID = "00000000-0000-4000-8000-000000000099"
			},
			want: "bound repository",
		},
		{
			name: "author approved their own delivery",
			configure: func(repository *pipelineRepositoryStub) {
				repository.authorization.ApprovedBy = request.AuthorID
			},
			want: "immutable human approval",
		},
		{
			name: "validator approved their own delivery",
			configure: func(repository *pipelineRepositoryStub) {
				repository.authorization.ApprovedBy = request.ValidatorID
			},
			want: "immutable human approval",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &pipelineRepositoryStub{
				authority: control.Authority{
					TenantID: control.LocalTenantID, RepositoryID: control.LocalRepositoryID,
				},
				run: baseRun, attempt: baseAttempt, authorization: baseAuthorization,
			}
			test.configure(repository)
			sideEffects := &pipelineSideEffectStub{}
			pipeline, err := NewPipeline(
				repository, sideEffects, sideEffects, sideEffects, sideEffects,
				"delivery-pipeline-test",
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = pipeline.Execute(t.Context(), request, fence)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("authorization error = %v, want %q", err, test.want)
			}
			if repository.transitions != 0 || repository.leaseAcquisitions != 0 ||
				sideEffects.calls != 0 {
				t.Fatalf(
					"unauthorized request reached side effects: transitions=%d leases=%d services=%d",
					repository.transitions, repository.leaseAcquisitions, sideEffects.calls,
				)
			}
		})
	}
}

func TestPipelineRejectsSchemaInvalidOrPostApprovalMutationBeforeSideEffects(t *testing.T) {
	approved := pipelineRequestFixture(t)
	fence := persistence.LeaseProof{
		LeaseKey: persistence.LeaseKey{
			TenantID: control.LocalTenantID, RepositoryID: control.LocalRepositoryID,
			ResourceType: "scheduler", ResourceID: "scheduler-test",
		},
		OwnerID: "scheduler-owner", FencingToken: 7,
	}
	digest, err := deliveryRequestDigest(approved)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*contracts.DeliveryRequest)
		want   string
	}{
		{
			name: "schema-invalid role",
			mutate: func(request *contracts.DeliveryRequest) {
				request.Role = "unapproved-role"
			},
			want: "schema",
		},
		{
			name: "write scope changed after approval",
			mutate: func(request *contracts.DeliveryRequest) {
				request.WriteScopes = []string{"internal/expanded"}
			},
			want: "immutable human approval",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := approved
			test.mutate(&request)
			repository := &pipelineRepositoryStub{
				authority: control.Authority{
					TenantID: control.LocalTenantID, RepositoryID: control.LocalRepositoryID,
				},
				run: contracts.Run{
					RunID: approved.RunID, Objective: approved.Objective,
					State: string(runstate.StateQueued), Version: 3,
				},
				attempt: persistence.Attempt{
					AttemptID: approved.AttemptID, RunID: approved.RunID,
					Ordinal: approved.AttemptOrdinal, Status: "queued", Version: 1,
					LeaseResourceType: fence.ResourceType, LeaseResourceID: fence.ResourceID,
					WorkerID: fence.OwnerID, FencingToken: fence.FencingToken,
				},
				authorization: persistence.DeliveryAuthorization{
					Request: approved, RequestSHA256: digest,
					ApprovedBy: "independent-human", ApprovedAt: time.Now().UTC(),
				},
			}
			sideEffects := &pipelineSideEffectStub{}
			pipeline, err := NewPipeline(
				repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = pipeline.Execute(t.Context(), request, fence)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if repository.transitions != 0 || repository.leaseAcquisitions != 0 ||
				sideEffects.calls != 0 {
				t.Fatalf("rejected request reached side effects")
			}
		})
	}
}

func TestPipelinePersistsCanonicalFailureWhenWorkerReturnsNoResult(t *testing.T) {
	request := pipelineRequestFixture(t)
	fence := persistence.LeaseProof{
		LeaseKey: persistence.LeaseKey{
			TenantID: control.LocalTenantID, RepositoryID: control.LocalRepositoryID,
			ResourceType: "scheduler", ResourceID: "scheduler-test",
		},
		OwnerID: "scheduler-owner", FencingToken: 7,
	}
	digest, err := deliveryRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	repository := &pipelineRepositoryStub{
		authority: control.Authority{
			TenantID: control.LocalTenantID, RepositoryID: control.LocalRepositoryID,
		},
		run: contracts.Run{
			RunID: request.RunID, Objective: request.Objective,
			State: string(runstate.StateQueued), Version: 3,
		},
		attempt: persistence.Attempt{
			AttemptID: request.AttemptID, RunID: request.RunID,
			Ordinal: request.AttemptOrdinal, Status: "queued", Version: 1,
			LeaseResourceType: fence.ResourceType, LeaseResourceID: fence.ResourceID,
			WorkerID: fence.OwnerID, FencingToken: fence.FencingToken,
		},
		authorization: persistence.DeliveryAuthorization{
			Request: request, RequestSHA256: digest,
			ApprovedBy: "independent-human", ApprovedAt: time.Now().UTC(),
		},
	}
	sideEffects := &pipelineSideEffectStub{workerErr: context.DeadlineExceeded}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := pipeline.Execute(t.Context(), request, fence)
	if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("worker failure error = %v", err)
	}
	if repository.attempt.Status != "failed_retryable" ||
		repository.run.State != string(runstate.StateFailedRetryable) ||
		outcome.Worker.TerminationReason != "telemetry_failure" ||
		outcome.Worker.Adapter != "execution-pipeline" ||
		sideEffects.quarantines != 1 || repository.releases != 1 {
		t.Fatalf(
			"failure was not durably closed: run=%#v attempt=%#v outcome=%#v quarantines=%d releases=%d",
			repository.run, repository.attempt, outcome, sideEffects.quarantines, repository.releases,
		)
	}
}

func TestPipelineKeepsQueuedAttemptNonResumableBeforeStart(t *testing.T) {
	for _, test := range []struct {
		name            string
		configure       func(*pipelineRepositoryStub, *pipelineSideEffectStub)
		wantQuarantine  int
		wantLeaseRetire int
	}{
		{
			name: "lease acquisition",
			configure: func(repository *pipelineRepositoryStub, _ *pipelineSideEffectStub) {
				repository.acquireErr = errors.New("delivery leases unavailable")
			},
		},
		{
			name: "delivery lease refresh",
			configure: func(repository *pipelineRepositoryStub, _ *pipelineSideEffectStub) {
				repository.leaseSetRenewErrAfter = 1
				repository.leaseSetRenewErr = errors.New("delivery lease refresh failed")
			},
			wantLeaseRetire: 1,
		},
		{
			name: "scheduler refresh",
			configure: func(repository *pipelineRepositoryStub, _ *pipelineSideEffectStub) {
				repository.schedulerRenewHook = func(_ context.Context, ordinal int) error {
					if ordinal == 2 {
						return errors.New("scheduler refresh failed")
					}
					return nil
				}
			},
			wantLeaseRetire: 1,
		},
		{
			name: "worktree preparation",
			configure: func(repository *pipelineRepositoryStub, sideEffects *pipelineSideEffectStub) {
				sideEffects.prepareErr = errors.New("pre-existing attempt path is contaminated")
				repository.releaseHook = func() error {
					if sideEffects.quarantines != 1 {
						return errors.New("lease release preceded quarantine")
					}
					return nil
				}
			},
			wantQuarantine:  1,
			wantLeaseRetire: 1,
		},
		{
			name: "attempt start",
			configure: func(repository *pipelineRepositoryStub, _ *pipelineSideEffectStub) {
				repository.startErr = errors.New("attempt start transaction unavailable")
			},
			wantQuarantine:  1,
			wantLeaseRetire: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := pipelineRequestFixture(t)
			fence, repository := authorizedPipelineFixture(t, request)
			sideEffects := &pipelineSideEffectStub{workerResult: successfulWorkerResult(request)}
			test.configure(repository, sideEffects)
			pipeline, err := NewPipeline(
				repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
			)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := pipeline.Execute(t.Context(), request, fence)
			if err == nil {
				t.Fatal("pre-start failure unexpectedly succeeded")
			}
			if repository.run.State != string(runstate.StatePreparing) ||
				repository.attempt.Status != "queued" ||
				outcome.Run.State != string(runstate.StatePreparing) {
				t.Fatalf(
					"pre-start failure exposed resumable state: run=%#v attempt=%#v outcome=%#v",
					repository.run, repository.attempt, outcome,
				)
			}
			if sideEffects.quarantines != test.wantQuarantine ||
				repository.releases != test.wantLeaseRetire {
				t.Fatalf(
					"pre-start cleanup: quarantines=%d releases=%d, want %d and %d",
					sideEffects.quarantines, repository.releases,
					test.wantQuarantine, test.wantLeaseRetire,
				)
			}
		})
	}
}

func TestPipelineRequiresPreparationQuarantineBeforeLeaseRelease(t *testing.T) {
	request := pipelineRequestFixture(t)
	fence, repository := authorizedPipelineFixture(t, request)
	sideEffects := &pipelineSideEffectStub{
		prepareErr:    errors.New("pre-existing attempt path is contaminated"),
		quarantineErr: errors.New("quarantine authority marker is invalid"),
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Execute(t.Context(), request, fence); err == nil ||
		!strings.Contains(err.Error(), "quarantine authority marker is invalid") {
		t.Fatalf("preparation quarantine failure = %v", err)
	}
	if repository.run.State != string(runstate.StatePreparing) ||
		repository.attempt.Status != "queued" || repository.releases != 0 ||
		sideEffects.quarantines < 1 {
		t.Fatalf(
			"unquarantined preparation released authority: run=%#v attempt=%#v quarantines=%d releases=%d",
			repository.run, repository.attempt, sideEffects.quarantines, repository.releases,
		)
	}
}

func TestPipelineAcceptsProvenAbsenceAfterPreparationFailure(t *testing.T) {
	request := pipelineRequestFixture(t)
	fence, repository := authorizedPipelineFixture(t, request)
	sideEffects := &pipelineSideEffectStub{
		prepareErr:    errors.New("prepare failed before checkout creation"),
		quarantineErr: delivery.ErrWorktreeNotFound,
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Execute(t.Context(), request, fence); err == nil ||
		strings.Contains(err.Error(), "quarantine failed delivery") {
		t.Fatalf("proven pre-worker absence error = %v", err)
	}
	if repository.run.State != string(runstate.StatePreparing) || repository.releases != 1 {
		t.Fatalf("proven pre-worker absence did not retire leases: run=%#v releases=%d", repository.run, repository.releases)
	}
}

func TestPipelineKeepsRunNonResumableWhenAttemptFinalizationFails(t *testing.T) {
	request := pipelineRequestFixture(t)
	fence, repository := authorizedPipelineFixture(t, request)
	repository.finishErr = errors.New("attempt transaction unavailable")
	sideEffects := &pipelineSideEffectStub{
		workerResult: successfulWorkerResult(request),
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := pipeline.Execute(t.Context(), request, fence)
	if err == nil || !strings.Contains(err.Error(), "attempt transaction unavailable") {
		t.Fatalf("attempt finalization error = %v", err)
	}
	if repository.attempt.Status != "running" ||
		repository.run.State != string(runstate.StateRunning) ||
		outcome.Run.State != string(runstate.StateRunning) {
		t.Fatalf(
			"failed attempt finalization exposed resumable state: run=%#v attempt=%#v outcome=%#v",
			repository.run, repository.attempt, outcome,
		)
	}
	if sideEffects.quarantines != 1 || repository.releases != 1 {
		t.Fatalf(
			"failed attempt finalization did not retire delivery resources: quarantines=%d releases=%d",
			sideEffects.quarantines, repository.releases,
		)
	}
}

func TestPipelineNormalizesSucceededResultReturnedWithWorkerError(t *testing.T) {
	request := pipelineRequestFixture(t)
	fence, repository := authorizedPipelineFixture(t, request)
	sideEffects := &pipelineSideEffectStub{
		workerErr:    errors.New("final worker lifecycle event was not durable"),
		workerResult: successfulWorkerResult(request),
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := pipeline.Execute(t.Context(), request, fence)
	if err == nil || !strings.Contains(err.Error(), "final worker lifecycle event") {
		t.Fatalf("worker lifecycle error = %v", err)
	}
	if outcome.Worker.Status != "failed_retryable" ||
		outcome.Worker.TerminationReason != "telemetry_failure" ||
		repository.attempt.Status != "failed_retryable" ||
		repository.run.State != string(runstate.StateFailedRetryable) ||
		sideEffects.quarantines != 1 || repository.releases != 1 {
		t.Fatalf(
			"succeeded+error was not normalized: outcome=%#v run=%#v attempt=%#v",
			outcome, repository.run, repository.attempt,
		)
	}
}

func TestPipelinePersistsLeaseGuardCancellationAsRetryableFailure(t *testing.T) {
	request := pipelineRequestFixture(t)
	fence, repository := authorizedPipelineFixture(t, request)
	repository.leaseSetRenewErrAfter = 2
	repository.leaseSetRenewErr = errors.New("delivery lease heartbeat rejected")
	sideEffects := &pipelineSideEffectStub{
		workerHook: func(ctx context.Context, _ contracts.WorkerTask) (contracts.WorkerResult, error) {
			<-ctx.Done()
			return cancelledWorkerResult(request), nil
		},
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	pipeline.heartbeatInterval = func(time.Duration) time.Duration { return time.Millisecond }

	outcome, err := pipeline.Execute(t.Context(), request, fence)
	if err == nil || !strings.Contains(err.Error(), "delivery lease heartbeat rejected") {
		t.Fatalf("lease heartbeat failure error = %v", err)
	}
	if outcome.Worker.Status != "failed_retryable" ||
		outcome.Worker.TerminationReason != "telemetry_failure" ||
		repository.attempt.Status != "failed_retryable" ||
		repository.run.State != string(runstate.StateFailedRetryable) ||
		sideEffects.quarantines != 1 || repository.releases != 1 {
		t.Fatalf(
			"guard cancellation was not durably retryable: outcome=%#v run=%#v attempt=%#v",
			outcome, repository.run, repository.attempt,
		)
	}
}

func TestPipelineRejectsWorkerCancellationWithoutGovernedRunState(t *testing.T) {
	request := pipelineRequestFixture(t)
	fence, repository := authorizedPipelineFixture(t, request)
	sideEffects := &pipelineSideEffectStub{workerResult: cancelledWorkerResult(request)}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := pipeline.Execute(t.Context(), request, fence)
	if err == nil || !strings.Contains(err.Error(), "lacks durable governed Run cancellation") {
		t.Fatalf("ungoverned cancellation error = %v", err)
	}
	if outcome.Worker.Status != "failed_retryable" ||
		repository.attempt.Status != "failed_retryable" ||
		repository.run.State != string(runstate.StateFailedRetryable) ||
		sideEffects.quarantines != 1 || repository.releases != 1 {
		t.Fatalf(
			"ungoverned cancellation was not retryable: outcome=%#v run=%#v attempt=%#v",
			outcome, repository.run, repository.attempt,
		)
	}
}

func TestPipelineFinalizesDurablyGovernedWorkerCancellation(t *testing.T) {
	request := pipelineRequestFixture(t)
	fence, repository := authorizedPipelineFixture(t, request)
	sideEffects := &pipelineSideEffectStub{
		workerHook: func(context.Context, contracts.WorkerTask) (contracts.WorkerResult, error) {
			repository.run.State = string(runstate.StateCancelling)
			repository.run.Version++
			return cancelledWorkerResult(request), context.Canceled
		},
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := pipeline.Execute(t.Context(), request, fence)
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("governed cancellation error = %v", err)
	}
	if outcome.Worker.Status != "cancelled" ||
		repository.attempt.Status != "cancelled" ||
		repository.run.State != string(runstate.StateCancelled) ||
		sideEffects.quarantines != 1 || repository.releases != 1 {
		t.Fatalf(
			"governed cancellation was not finalized: outcome=%#v run=%#v attempt=%#v",
			outcome, repository.run, repository.attempt,
		)
	}
}

func TestPipelineKeepsCommitLeaseLossRetryable(t *testing.T) {
	request := pipelineRequestFixture(t)
	fence, repository := authorizedPipelineFixture(t, request)
	repository.leaseSetRenewErrAfter = 3
	repository.leaseSetRenewErr = errors.New("commit heartbeat lease rejected")
	sideEffects := &pipelineSideEffectStub{
		workerResult: successfulWorkerResult(request),
		commitHook: func(ctx context.Context) (delivery.CommitResult, error) {
			<-ctx.Done()
			return delivery.CommitResult{}, context.Cause(ctx)
		},
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	pipeline.heartbeatInterval = func(time.Duration) time.Duration { return time.Millisecond }

	outcome, err := pipeline.Execute(t.Context(), request, fence)
	if err == nil || !strings.Contains(err.Error(), "commit heartbeat lease rejected") {
		t.Fatalf("commit heartbeat failure error = %v", err)
	}
	if outcome.Attempt.Status != "succeeded" ||
		repository.run.State != string(runstate.StateFailedRetryable) ||
		sideEffects.quarantines != 1 || repository.releases != 1 {
		t.Fatalf(
			"commit lease loss was not retryable: outcome=%#v run=%#v",
			outcome, repository.run,
		)
	}
}

func TestPipelineKeepsCallerCancelledCommitRetryable(t *testing.T) {
	request := pipelineRequestFixture(t)
	fence, repository := authorizedPipelineFixture(t, request)
	ctx, cancel := context.WithCancel(t.Context())
	sideEffects := &pipelineSideEffectStub{
		workerResult: successfulWorkerResult(request),
		commitHook: func(context.Context) (delivery.CommitResult, error) {
			cancel()
			return delivery.CommitResult{}, context.Canceled
		},
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := pipeline.Execute(ctx, request, fence)
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("caller-cancelled commit error = %v", err)
	}
	if outcome.Attempt.Status != "succeeded" ||
		repository.run.State != string(runstate.StateFailedRetryable) ||
		sideEffects.quarantines != 1 || repository.releases != 1 {
		t.Fatalf(
			"caller-cancelled commit was not retryable: outcome=%#v run=%#v",
			outcome, repository.run,
		)
	}
}

func TestPipelineRejectedValidationCleansBeforeTerminalState(t *testing.T) {
	request := pipelineRequestFixture(t)
	fence, repository := authorizedPipelineFixture(t, request)
	sideEffects := &pipelineSideEffectStub{
		workerResult: successfulWorkerResult(request),
		commit: delivery.CommitResult{
			BaseCommit: request.BaseCommit, ResultCommit: strings.Repeat("c", 40),
		},
		bundle: delivery.ValidationBundle{
			Report: contracts.ValidationReport{Status: "failed"},
		},
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := pipeline.Execute(t.Context(), request, fence)
	if err == nil || !strings.Contains(err.Error(), "validation status") {
		t.Fatalf("rejected validation error = %v", err)
	}
	if outcome.Run.State != string(runstate.StateFailedTerminal) ||
		repository.releases != 1 || sideEffects.quarantines != 1 || outcome.Quarantine == nil {
		t.Fatalf("rejected validation cleanup = %#v", outcome)
	}

	request = pipelineRequestFixture(t)
	fence, repository = authorizedPipelineFixture(t, request)
	repository.releaseErr = errors.New("transient lease release failure")
	sideEffects = &pipelineSideEffectStub{
		workerResult: successfulWorkerResult(request),
		commit: delivery.CommitResult{
			BaseCommit: request.BaseCommit, ResultCommit: strings.Repeat("c", 40),
		},
		bundle: delivery.ValidationBundle{
			Report: contracts.ValidationReport{Status: "failed"},
		},
	}
	pipeline, err = NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Execute(t.Context(), request, fence); err == nil || !strings.Contains(err.Error(), "lease release failure") {
		t.Fatalf("cleanup failure error = %v", err)
	}
	if repository.run.State != string(runstate.StateValidating) || sideEffects.quarantines != 1 {
		t.Fatalf("cleanup failure prematurely closed Run: run=%#v quarantines=%d", repository.run, sideEffects.quarantines)
	}
}

func TestPipelineCleansWorkerAttemptBeforeClosingRun(t *testing.T) {
	for _, test := range []struct {
		name       string
		result     func(contracts.DeliveryRequest) contracts.WorkerResult
		target     runstate.State
		prepareRun func(*pipelineRepositoryStub)
	}{
		{
			name: "blocked", target: runstate.StateAwaitingDecision,
			result: func(request contracts.DeliveryRequest) contracts.WorkerResult {
				return nonSuccessWorkerResult(request, "blocked")
			},
		},
		{
			name: "failed retryable", target: runstate.StateFailedRetryable,
			result: func(request contracts.DeliveryRequest) contracts.WorkerResult {
				return nonSuccessWorkerResult(request, "failed_retryable")
			},
		},
		{
			name: "failed terminal", target: runstate.StateFailedTerminal,
			result: func(request contracts.DeliveryRequest) contracts.WorkerResult {
				return nonSuccessWorkerResult(request, "failed_terminal")
			},
		},
		{
			name: "cancelled", target: runstate.StateCancelled,
			result: func(request contracts.DeliveryRequest) contracts.WorkerResult {
				return cancelledWorkerResult(request)
			},
			prepareRun: func(repository *pipelineRepositoryStub) {
				repository.run.State = string(runstate.StateCancelling)
				repository.run.Version++
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := pipelineRequestFixture(t)
			fence, repository := authorizedPipelineFixture(t, request)
			sideEffects := &pipelineSideEffectStub{}
			closedAfterCleanup := false
			repository.transitionHook = func(target runstate.State) error {
				if target != test.target {
					return nil
				}
				if sideEffects.quarantines != 1 || repository.releases != 1 {
					return fmt.Errorf(
						"Run reached %s before cleanup: quarantines=%d releases=%d",
						target, sideEffects.quarantines, repository.releases,
					)
				}
				closedAfterCleanup = true
				return nil
			}
			sideEffects.workerHook = func(
				context.Context,
				contracts.WorkerTask,
			) (contracts.WorkerResult, error) {
				if test.prepareRun != nil {
					test.prepareRun(repository)
				}
				return test.result(request), nil
			}
			pipeline, err := NewPipeline(
				repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
			)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := pipeline.Execute(t.Context(), request, fence)
			if err == nil || !strings.Contains(err.Error(), "worker attempt ended") {
				t.Fatalf("non-success worker error = %v", err)
			}
			if !closedAfterCleanup || outcome.Run.State != string(test.target) {
				t.Fatalf(
					"worker closure order not proven: outcome=%#v quarantines=%d releases=%d",
					outcome, sideEffects.quarantines, repository.releases,
				)
			}
		})
	}
}

func TestPipelinePublicationConflictCleansBeforeTerminalState(t *testing.T) {
	request := pipelineRequestFixture(t)
	fence, repository := authorizedPipelineFixture(t, request)
	repository.publicationFound = true
	repository.publication = persistence.DeliveryPublication{State: "conflict"}
	sideEffects := &pipelineSideEffectStub{
		workerResult: successfulWorkerResult(request),
		commit: delivery.CommitResult{
			BaseCommit: request.BaseCommit, ResultCommit: strings.Repeat("c", 40),
		},
		bundle: delivery.ValidationBundle{
			Report: contracts.ValidationReport{Status: "passed"},
		},
		publicationErr: delivery.ErrPublicationConflict,
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := pipeline.Execute(t.Context(), request, fence)
	if !errors.Is(err, delivery.ErrPublicationConflict) {
		t.Fatalf("publication conflict error = %v", err)
	}
	if outcome.Run.State != string(runstate.StateFailedTerminal) ||
		repository.releases != 1 || sideEffects.quarantines != 1 || outcome.Quarantine == nil {
		t.Fatalf(
			"publication conflict was not settled: outcome=%#v releases=%d quarantines=%d",
			outcome, repository.releases, sideEffects.quarantines,
		)
	}
}

func TestPipelinePublicationUsesBoundedContextAfterCallerCancellation(t *testing.T) {
	request := pipelineRequestFixture(t)
	fence, repository := authorizedPipelineFixture(t, request)
	ctx, cancel := context.WithCancel(t.Context())
	sideEffects := &pipelineSideEffectStub{
		workerResult: successfulWorkerResult(request),
		commit: delivery.CommitResult{
			BaseCommit: request.BaseCommit, ResultCommit: strings.Repeat("c", 40),
		},
		bundle: delivery.ValidationBundle{
			Report: contracts.ValidationReport{Status: "passed"},
		},
		publicationResult: delivery.PublicationResult{
			Receipt:       contracts.DeliveryReceipt{Status: "published"},
			LeaseReleased: true,
		},
		validationHook: cancel,
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := pipeline.Execute(ctx, request, fence)
	if err != nil {
		t.Fatalf("publish after caller cancellation: %v", err)
	}
	if outcome.Run.State != string(runstate.StateCompleted) ||
		!sideEffects.publicationDeadline || sideEffects.publicationContextErr != nil {
		t.Fatalf("publication context was not detached and bounded: outcome=%#v effects=%#v", outcome, sideEffects)
	}
}

func TestPipelineRefreshesSchedulerAfterDetachedPublicationRenewal(t *testing.T) {
	request := pipelineRequestFixture(t)
	fence, repository := authorizedPipelineFixture(t, request)
	repository.schedulerRenewHook = func(_ context.Context, ordinal int) error {
		if ordinal == 4 {
			return errors.New("scheduler fence expired during publication renewal")
		}
		return nil
	}
	sideEffects := &pipelineSideEffectStub{
		workerResult: successfulWorkerResult(request),
		commit: delivery.CommitResult{
			BaseCommit: request.BaseCommit, ResultCommit: strings.Repeat("c", 40),
		},
		bundle: delivery.ValidationBundle{
			Report: contracts.ValidationReport{Status: "passed"},
		},
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Execute(t.Context(), request, fence); err == nil ||
		!strings.Contains(err.Error(), "renew scheduler fence for publication") {
		t.Fatalf("stale live publication scheduler error = %v", err)
	}
	if repository.leaseSetRenewals != 3 || repository.schedulerRenewals != 4 {
		t.Fatalf(
			"live publication renewals = delivery %d scheduler %d, want 3 and 4",
			repository.leaseSetRenewals, repository.schedulerRenewals,
		)
	}
	if sideEffects.publicationCalls != 0 {
		t.Fatalf("stale live scheduler reached publication %d times", sideEffects.publicationCalls)
	}
}

func TestPipelineRefreshesBothAuthoritiesBeforeWorktreeMutation(t *testing.T) {
	request := pipelineRequestFixture(t)
	fence, repository := authorizedPipelineFixture(t, request)
	sideEffects := &pipelineSideEffectStub{
		workerResult: successfulWorkerResult(request),
		commit: delivery.CommitResult{
			BaseCommit: request.BaseCommit, ResultCommit: strings.Repeat("c", 40),
		},
		bundle: delivery.ValidationBundle{
			Report: contracts.ValidationReport{Status: "passed"},
		},
		publicationResult: delivery.PublicationResult{
			Receipt: contracts.DeliveryReceipt{Status: "published"}, LeaseReleased: true,
		},
	}
	sideEffects.prepareHook = func() {
		if repository.schedulerRenewals < 2 || repository.leaseSetRenewals < 1 {
			t.Fatalf(
				"Prepare saw stale authority refresh counts: scheduler=%d delivery=%d",
				repository.schedulerRenewals, repository.leaseSetRenewals,
			)
		}
	}
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	observer := observability.NewObserver(provider, nil)
	mcpContext, mcpOperation := observer.Start(
		t.Context(), observability.BoundaryMCP, observability.OperationSubmitSprint,
	)
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
		observer,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, executionErr := pipeline.Execute(mcpContext, request, fence)
	mcpOperation.End(executionErr)
	if executionErr != nil {
		t.Fatalf("execute with synchronous authority refresh: %v", executionErr)
	}
	assertConnectedPipelineTrace(t, exporter.GetSpans())
}

func TestPipelineExporterFailureDoesNotChangeCanonicalOutcome(t *testing.T) {
	request := pipelineRequestFixture(t)
	fence, repository := authorizedPipelineFixture(t, request)
	sideEffects := &pipelineSideEffectStub{
		workerResult: successfulWorkerResult(request),
		commit: delivery.CommitResult{
			BaseCommit: request.BaseCommit, ResultCommit: strings.Repeat("c", 40),
		},
		bundle: delivery.ValidationBundle{
			Report: contracts.ValidationReport{Status: "passed"},
		},
		publicationResult: delivery.PublicationResult{
			Receipt: contracts.DeliveryReceipt{Status: "published"}, LeaseReleased: true,
		},
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(failingSpanExporter{}))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
		observability.NewObserver(provider, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := pipeline.Execute(t.Context(), request, fence)
	if err != nil {
		t.Fatalf("telemetry failure changed canonical execution: %v", err)
	}
	if outcome.Run.State != string(runstate.StateCompleted) ||
		outcome.Publication.Receipt.Status != "published" {
		t.Fatalf("canonical outcome changed by telemetry failure: %#v", outcome)
	}
}

type failingSpanExporter struct{}

func (failingSpanExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return errors.New("synthetic telemetry outage")
}

func (failingSpanExporter) Shutdown(context.Context) error { return nil }

func assertConnectedPipelineTrace(t *testing.T, spans tracetest.SpanStubs) {
	t.Helper()
	byName := make(map[string]tracetest.SpanStub, len(spans))
	for _, span := range spans {
		byName[span.Name] = span
	}
	root, ok := byName["forja.scheduler.execute_delivery"]
	if !ok {
		t.Fatalf("scheduler root missing from spans: %#v", spans)
	}
	mcpRoot, ok := byName["forja.mcp.submit_sprint"]
	if !ok {
		t.Fatalf("synthetic MCP root missing from spans: %#v", spans)
	}
	if root.Parent.SpanID() != mcpRoot.SpanContext.SpanID() {
		t.Fatal("scheduler root is disconnected from the synthetic MCP boundary")
	}
	for _, name := range []string{
		"forja.worker.dispatch_worker",
		"forja.validation.validate_change",
		"forja.delivery.publish_change",
	} {
		span, ok := byName[name]
		if !ok {
			t.Fatalf("pipeline child %q missing from spans: %#v", name, spans)
		}
		if span.Parent.SpanID() != root.SpanContext.SpanID() {
			t.Fatalf("pipeline child %q is disconnected from scheduler root", name)
		}
	}
}

func TestObservedWorkerErrorPrefersCanonicalTerminalReason(t *testing.T) {
	t.Parallel()
	result := contracts.WorkerResult{
		Status: "failed_retryable", TerminationReason: "telemetry_failure",
	}
	got := observability.Classify(observedWorkerError(result, errors.New("raw failure")))
	if got != observability.FailureUnavailable {
		t.Fatalf("terminal worker reason classified as %q, want %q", got, observability.FailureUnavailable)
	}
}

func TestObservedPipelineErrorUsesDurableTerminalOutcome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		outcome Outcome
		err     error
		want    observability.FailureClass
	}{
		{
			name: "validation rejection",
			outcome: Outcome{Validation: delivery.ValidationBundle{
				Report: contracts.ValidationReport{Status: "failed"},
			}},
			err:  errors.New("validation rejected"),
			want: observability.FailureValidation,
		},
		{
			name: "blocked worker",
			outcome: Outcome{Worker: contracts.WorkerResult{
				Status: "blocked", TerminationReason: "worker_blocked",
			}},
			err:  errors.New("worker stopped"),
			want: observability.FailureWorker,
		},
		{
			name: "worker deadline",
			outcome: Outcome{Worker: contracts.WorkerResult{
				Status: "failed_retryable", TerminationReason: "wall_timeout",
			}},
			err:  errors.New("worker stopped"),
			want: observability.FailureDeadline,
		},
		{
			name:    "recovered cancellation",
			outcome: Outcome{Attempt: persistence.Attempt{Status: "cancelled"}},
			err:     errors.New("recovered terminal attempt"),
			want:    observability.FailureCancelled,
		},
		{
			name:    "recovered retryable worker failure",
			outcome: Outcome{Attempt: persistence.Attempt{Status: "failed_retryable"}},
			err:     errors.New("recovered terminal attempt"),
			want:    observability.FailureWorker,
		},
		{
			name: "stable class wins",
			err:  observability.NewFailure(observability.FailureConflict),
			want: observability.FailureConflict,
		},
		{
			name: "unknown orchestration failure remains internal",
			err:  errors.New("unknown"),
			want: observability.FailureInternal,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := observability.Classify(observedPipelineError(test.outcome, test.err)); got != test.want {
				t.Fatalf("pipeline outcome classified as %q, want %q", got, test.want)
			}
		})
	}
}

func TestPipelineRejectsStaleSchedulerFenceBeforeMutation(t *testing.T) {
	request := pipelineRequestFixture(t)
	fence := persistence.LeaseProof{
		LeaseKey: persistence.LeaseKey{
			TenantID: control.LocalTenantID, RepositoryID: control.LocalRepositoryID,
			ResourceType: "scheduler", ResourceID: "scheduler-test",
		},
		OwnerID: "scheduler-owner", FencingToken: 7,
	}
	digest, err := deliveryRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	repository := &pipelineRepositoryStub{
		authority: control.Authority{
			TenantID: control.LocalTenantID, RepositoryID: control.LocalRepositoryID,
		},
		run: contracts.Run{
			RunID: request.RunID, Objective: request.Objective,
			State: string(runstate.StateQueued), Version: 3,
		},
		attempt: persistence.Attempt{
			AttemptID: request.AttemptID, RunID: request.RunID,
			Ordinal: request.AttemptOrdinal, Status: "queued", Version: 1,
			LeaseResourceType: fence.ResourceType, LeaseResourceID: fence.ResourceID,
			WorkerID: fence.OwnerID, FencingToken: fence.FencingToken,
		},
		authorization: persistence.DeliveryAuthorization{
			Request: request, RequestSHA256: digest,
			ApprovedBy: "independent-human", ApprovedAt: time.Now().UTC(),
		},
		verifyErr: errors.New("simulated expired scheduler fence"),
	}
	sideEffects := &pipelineSideEffectStub{}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pipeline.Execute(t.Context(), request, fence)
	if err == nil || !strings.Contains(err.Error(), "expired scheduler fence") {
		t.Fatalf("stale scheduler error = %v", err)
	}
	if repository.transitions != 0 || repository.leaseAcquisitions != 0 || sideEffects.calls != 0 {
		t.Fatal("stale scheduler fence reached durable or filesystem mutation")
	}
}

func TestPipelineRecoveryCompletesPreparedPublicationWithoutRerunningWorker(t *testing.T) {
	request := pipelineRequestFixture(t)
	recoveryFence := persistence.LeaseProof{
		LeaseKey: persistence.LeaseKey{
			TenantID: control.LocalTenantID, RepositoryID: control.LocalRepositoryID,
			ResourceType: "scheduler", ResourceID: "scheduler-test",
		},
		OwnerID: "recovery-scheduler", FencingToken: 8,
	}
	digest, err := deliveryRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	receipt := pipelineReceiptFixture(request)
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	repository := &pipelineRepositoryStub{
		authority: control.Authority{
			TenantID: control.LocalTenantID, RepositoryID: control.LocalRepositoryID,
		},
		run: contracts.Run{
			RunID: request.RunID, Objective: request.Objective,
			State: string(runstate.StateValidating), Version: 6,
		},
		attempt: persistence.Attempt{
			AttemptID: request.AttemptID, RunID: request.RunID,
			Ordinal: request.AttemptOrdinal, Status: "succeeded", Version: 3,
			LeaseResourceType: recoveryFence.ResourceType,
			LeaseResourceID:   recoveryFence.ResourceID,
		},
		authorization: persistence.DeliveryAuthorization{
			Request: request, RequestSHA256: digest,
			ApprovedBy: "independent-human", ApprovedAt: time.Now().UTC(),
		},
		publicationFound: true,
		publication: persistence.DeliveryPublication{
			State:  "prepared",
			Intent: persistence.DeliveryPublicationIntent{ReceiptJSON: receiptJSON},
		},
		leaseFound: true,
		leaseState: "released",
		leaseSet: persistence.LeaseSet{
			LeaseSetID: request.AttemptID, OwnerID: "delivery-pipeline-test",
		},
	}
	sideEffects := &pipelineSideEffectStub{
		loadErr: errors.New("persisted evidence was removed after journal prepare"),
		publicationResult: delivery.PublicationResult{
			Receipt: receipt, Replayed: true,
		},
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := pipeline.Recover(t.Context(), request, recoveryFence)
	if err != nil {
		t.Fatalf("recover prepared publication: %v", err)
	}
	if outcome.Run.State != string(runstate.StateCompleted) ||
		outcome.Publication.Receipt.Status != "published" ||
		sideEffects.workerCalls != 0 || sideEffects.recoveryCalls != 1 ||
		sideEffects.commitCalls != 0 || sideEffects.loadCalls != 0 ||
		!sideEffects.publicationDeadline {
		t.Fatalf("unexpected recovery outcome: %#v sideEffects=%#v", outcome, sideEffects)
	}
}

func TestPipelineRecoveryRejectsUnrelatedSchedulerResource(t *testing.T) {
	request := pipelineRequestFixture(t)
	recoveryFence, repository := authorizedPipelineFixture(t, request)
	repository.run.State = string(runstate.StateValidating)
	repository.attempt.Status = "succeeded"
	recoveryFence.ResourceID = "another-scheduler"
	sideEffects := &pipelineSideEffectStub{}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Recover(t.Context(), request, recoveryFence); err == nil ||
		!strings.Contains(err.Error(), "attempt scheduler resource") {
		t.Fatalf("unrelated recovery fence error = %v", err)
	}
	if sideEffects.calls != 0 || repository.releases != 0 {
		t.Fatal("unrelated scheduler reached delivery side effects")
	}
}

func TestPipelineRecoveryClosesTerminalNonSuccessAttempts(t *testing.T) {
	tests := []struct {
		status string
		want   runstate.State
	}{
		{status: "blocked", want: runstate.StateAwaitingDecision},
		{status: "failed_terminal", want: runstate.StateFailedTerminal},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			request := pipelineRequestFixture(t)
			recoveryFence, repository := authorizedPipelineFixture(t, request)
			repository.run.State = string(runstate.StateRunning)
			repository.attempt.Status = test.status
			repository.leaseFound = true
			repository.leaseState = "active"
			repository.leaseSet = persistence.LeaseSet{
				LeaseSetID: request.AttemptID, OwnerID: "pipeline-test",
			}
			sideEffects := &pipelineSideEffectStub{}
			pipeline, err := NewPipeline(
				repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
			)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := pipeline.Recover(t.Context(), request, recoveryFence)
			if err == nil || !strings.Contains(err.Error(), "recovered terminal worker attempt") {
				t.Fatalf("terminal recovery error = %v", err)
			}
			if outcome.Run.State != string(test.want) || sideEffects.quarantines != 1 ||
				repository.releases != 1 {
				t.Fatalf(
					"terminal recovery = run %q quarantines %d releases %d",
					outcome.Run.State, sideEffects.quarantines, repository.releases,
				)
			}
		})
	}
}

func TestPipelineRecoveryKeepsUngovernedCancelledAttemptRetryable(t *testing.T) {
	request := pipelineRequestFixture(t)
	recoveryFence, repository := authorizedPipelineFixture(t, request)
	repository.run.State = string(runstate.StateRunning)
	repository.attempt.Status = "cancelled"
	repository.leaseFound = true
	repository.leaseState = "active"
	repository.leaseSet = persistence.LeaseSet{
		LeaseSetID: request.AttemptID, OwnerID: "pipeline-test",
	}
	sideEffects := &pipelineSideEffectStub{}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := pipeline.Recover(t.Context(), request, recoveryFence)
	if err == nil || !strings.Contains(err.Error(), "is retryable") {
		t.Fatalf("ungoverned cancellation recovery error = %v", err)
	}
	if outcome.Run.State != string(runstate.StateFailedRetryable) ||
		sideEffects.quarantines != 1 || repository.releases != 1 {
		t.Fatalf(
			"ungoverned cancellation recovery = outcome %#v quarantines %d releases %d",
			outcome, sideEffects.quarantines, repository.releases,
		)
	}
}

func TestPipelineRecoveryCleansSucceededAttemptAfterRunFailure(t *testing.T) {
	for _, state := range []runstate.State{
		runstate.StateFailedRetryable,
		runstate.StateFailedTerminal,
	} {
		t.Run(string(state), func(t *testing.T) {
			request := pipelineRequestFixture(t)
			recoveryFence, repository := authorizedPipelineFixture(t, request)
			repository.run.State = string(state)
			repository.attempt.Status = "succeeded"
			repository.leaseFound = true
			repository.leaseState = "active"
			repository.leaseSet = persistence.LeaseSet{
				LeaseSetID: request.AttemptID, OwnerID: "pipeline-test",
			}
			sideEffects := &pipelineSideEffectStub{}
			pipeline, err := NewPipeline(
				repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
			)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := pipeline.Recover(t.Context(), request, recoveryFence)
			if err == nil || !strings.Contains(err.Error(), "cleaned succeeded attempt") {
				t.Fatalf("terminal succeeded-attempt recovery error = %v", err)
			}
			if outcome.Run.State != string(state) || sideEffects.quarantines != 1 ||
				repository.releases != 1 || outcome.Quarantine == nil {
				t.Fatalf(
					"terminal succeeded cleanup = outcome %#v quarantines %d releases %d",
					outcome, sideEffects.quarantines, repository.releases,
				)
			}
		})
	}
}

func TestPipelineRecoveryFinalizesCancellingRun(t *testing.T) {
	for _, status := range []string{
		"succeeded", "failed_retryable", "blocked", "failed_terminal", "cancelled",
	} {
		t.Run(status, func(t *testing.T) {
			request := pipelineRequestFixture(t)
			recoveryFence, repository := authorizedPipelineFixture(t, request)
			repository.run.State = string(runstate.StateCancelling)
			repository.attempt.Status = status
			started := time.Now().UTC()
			repository.attempt.StartedAt = &started
			repository.leaseFound = true
			repository.leaseState = "active"
			repository.leaseSet = persistence.LeaseSet{
				LeaseSetID: request.AttemptID, OwnerID: "pipeline-test",
			}
			sideEffects := &pipelineSideEffectStub{}
			pipeline, err := NewPipeline(
				repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
			)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := pipeline.Recover(t.Context(), request, recoveryFence)
			if err == nil || !strings.Contains(err.Error(), "recovered cancellation") {
				t.Fatalf("cancellation recovery error = %v", err)
			}
			if outcome.Run.State != string(runstate.StateCancelled) ||
				sideEffects.quarantines != 1 || repository.releases != 1 {
				t.Fatalf(
					"cancellation recovery = outcome %#v quarantines %d releases %d",
					outcome, sideEffects.quarantines, repository.releases,
				)
			}

			repository.leaseState = "released"
			outcome, err = pipeline.Recover(t.Context(), request, recoveryFence)
			if err == nil || !strings.Contains(err.Error(), "recovered cancellation") {
				t.Fatalf("cancelled replay recovery error = %v", err)
			}
			if outcome.Run.State != string(runstate.StateCancelled) || repository.releases != 1 {
				t.Fatalf(
					"cancelled replay changed terminal state: outcome=%#v releases=%d",
					outcome, repository.releases,
				)
			}
		})
	}
}

func TestPipelineRecoveryRequiresQuarantineProofForStartedCancellation(t *testing.T) {
	for _, test := range []struct {
		name        string
		started     bool
		wantRelease int
		wantState   runstate.State
	}{
		{name: "started", started: true, wantState: runstate.StateCancelling},
		{name: "never started", wantRelease: 1, wantState: runstate.StateCancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := pipelineRequestFixture(t)
			recoveryFence, repository := authorizedPipelineFixture(t, request)
			repository.run.State = string(runstate.StateCancelling)
			repository.attempt.Status = "failed_retryable"
			if test.started {
				started := time.Now().UTC()
				repository.attempt.StartedAt = &started
			}
			repository.leaseFound = true
			repository.leaseState = "active"
			repository.leaseSet = persistence.LeaseSet{
				LeaseSetID: request.AttemptID, OwnerID: "pipeline-test",
			}
			sideEffects := &pipelineSideEffectStub{quarantineErr: delivery.ErrWorktreeNotFound}
			pipeline, err := NewPipeline(
				repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
			)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := pipeline.Recover(t.Context(), request, recoveryFence)
			if err == nil {
				t.Fatal("cancellation recovery unexpectedly omitted its diagnostic")
			}
			if outcome.Run.State != string(test.wantState) || repository.releases != test.wantRelease {
				t.Fatalf(
					"missing cancellation worktree = outcome %#v releases %d",
					outcome, repository.releases,
				)
			}
		})
	}
}

func TestWorkerTaskIncludesEveryAuthorizedArtifactScope(t *testing.T) {
	request := pipelineRequestFixture(t)
	request.ArtifactScopes = []string{"artifacts", "evidence"}
	worktree := delivery.Worktree{Path: t.TempDir()}
	task := workerTaskFrom(request, worktree)
	want := []string{"artifacts", "evidence", "internal/generated"}
	if !slices.Equal(task.WriteScopes, want) {
		t.Fatalf("worker writable scopes = %v, want %v", task.WriteScopes, want)
	}
	if !slices.Equal(request.WriteScopes, []string{"internal/generated"}) {
		t.Fatalf("worker task construction mutated request scopes: %v", request.WriteScopes)
	}
}

func TestPipelineRecoveryAcceptsMissingPrePrepareWorktreeOnly(t *testing.T) {
	for _, test := range []struct {
		name        string
		state       runstate.State
		wantRelease int
		wantState   runstate.State
	}{
		{name: "preparing", state: runstate.StatePreparing, wantRelease: 1, wantState: runstate.StateFailedRetryable},
		{name: "running", state: runstate.StateRunning, wantRelease: 0, wantState: runstate.StateRunning},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := pipelineRequestFixture(t)
			recoveryFence, repository := authorizedPipelineFixture(t, request)
			repository.run.State = string(test.state)
			repository.attempt.Status = "failed_retryable"
			repository.leaseFound = true
			repository.leaseState = "active"
			repository.leaseSet = persistence.LeaseSet{
				LeaseSetID: request.AttemptID, OwnerID: "pipeline-test",
			}
			sideEffects := &pipelineSideEffectStub{quarantineErr: delivery.ErrWorktreeNotFound}
			pipeline, err := NewPipeline(
				repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
			)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := pipeline.Recover(t.Context(), request, recoveryFence)
			if err == nil {
				t.Fatal("missing worktree recovery unexpectedly succeeded without diagnostic")
			}
			if outcome.Run.State != string(test.wantState) || repository.releases != test.wantRelease {
				t.Fatalf(
					"missing worktree recovery = outcome %#v releases %d",
					outcome, repository.releases,
				)
			}
		})
	}
}

func TestPipelineRecoveryRevalidatesCompletedPublicationAndReleasesLease(t *testing.T) {
	request := pipelineRequestFixture(t)
	recoveryFence, repository := authorizedPipelineFixture(t, request)
	repository.run.State = string(runstate.StateCompleted)
	repository.attempt.Status = "succeeded"
	repository.leaseFound = true
	repository.leaseState = "active"
	repository.leaseSet = persistence.LeaseSet{
		LeaseSetID: request.AttemptID, OwnerID: "pipeline-test",
	}
	receipt := pipelineReceiptFixture(request)
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	repository.publicationFound = true
	repository.publication = persistence.DeliveryPublication{
		State:  "published",
		Intent: persistence.DeliveryPublicationIntent{ReceiptJSON: receiptJSON},
	}
	sideEffects := &pipelineSideEffectStub{
		loadErr: errors.New("persisted evidence was removed after publication"),
		publicationResult: delivery.PublicationResult{
			Receipt: receipt, Replayed: true, LeaseReleased: true,
		},
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := pipeline.Recover(t.Context(), request, recoveryFence)
	if err != nil {
		t.Fatalf("replay completed publication: %v", err)
	}
	if sideEffects.recoveryCalls != 1 || sideEffects.loadCalls != 0 ||
		!outcome.Publication.LeaseReleased ||
		outcome.Commit.ResultCommit != receipt.ResultCommit {
		t.Fatalf("completed replay bypassed publisher: outcome=%#v calls=%d", outcome, sideEffects.recoveryCalls)
	}

	sideEffects.publicationErr = delivery.ErrPublicationConflict
	if _, err := pipeline.Recover(t.Context(), request, recoveryFence); !errors.Is(err, delivery.ErrPublicationConflict) {
		t.Fatalf("moved publication ref was not rejected: %v", err)
	}
}

func TestPipelineRecoveryUsesJournalCommitWithoutLiveDeliveryAuthority(t *testing.T) {
	request := pipelineRequestFixture(t)
	recoveryFence, repository := authorizedPipelineFixture(t, request)
	repository.run.State = string(runstate.StateValidating)
	repository.attempt.Status = "succeeded"
	repository.leaseFound = true
	repository.leaseState = "released"
	repository.leaseSet = persistence.LeaseSet{
		LeaseSetID: request.AttemptID, OwnerID: "pipeline-test",
	}
	receipt := pipelineReceiptFixture(request)
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	repository.publicationFound = true
	repository.publication = persistence.DeliveryPublication{
		State:  "prepared",
		Intent: persistence.DeliveryPublicationIntent{ReceiptJSON: receiptJSON},
	}
	sideEffects := &pipelineSideEffectStub{
		bundle: delivery.ValidationBundle{
			Report: contracts.ValidationReport{Status: "passed"},
		},
		publicationResult: delivery.PublicationResult{
			Receipt: receipt, Replayed: true, LeaseReleased: true,
		},
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := pipeline.Recover(t.Context(), request, recoveryFence)
	if err != nil {
		t.Fatalf("recover journaled commit without live delivery authority: %v", err)
	}
	if sideEffects.commitCalls != 0 || sideEffects.recoveryCalls != 1 ||
		outcome.Commit.ResultCommit != receipt.ResultCommit ||
		outcome.Run.State != string(runstate.StateCompleted) {
		t.Fatalf(
			"journal recovery mutated Git or lost commit identity: outcome=%#v effects=%#v",
			outcome, sideEffects,
		)
	}
}

func TestPipelineRecoveryBoundsDetachedPublicationLeaseRenewal(t *testing.T) {
	request := pipelineRequestFixture(t)
	recoveryFence, repository := authorizedPipelineFixture(t, request)
	repository.run.State = string(runstate.StateValidating)
	repository.attempt.Status = "succeeded"
	repository.leaseFound = true
	repository.leaseState = "active"
	repository.leaseSet = persistence.LeaseSet{
		LeaseSetID: request.AttemptID, OwnerID: "pipeline-test",
	}
	boundedRenewalObserved := false
	repository.leaseSetRenewHook = func(ctx context.Context, ordinal int) error {
		if ordinal != 2 {
			return nil
		}
		deadline, ok := ctx.Deadline()
		if !ok {
			return errors.New("detached recovery renewal has no deadline")
		}
		if remaining := time.Until(deadline); remaining <= 0 || remaining > cleanupTimeout {
			return fmt.Errorf("detached recovery renewal deadline is %s", remaining)
		}
		boundedRenewalObserved = true
		return nil
	}
	receipt := pipelineReceiptFixture(request)
	sideEffects := &pipelineSideEffectStub{
		commit: delivery.CommitResult{
			BaseCommit: request.BaseCommit, ResultCommit: receipt.ResultCommit,
		},
		bundle: delivery.ValidationBundle{
			Report: contracts.ValidationReport{Status: "passed"},
		},
		publicationResult: delivery.PublicationResult{
			Receipt: receipt, LeaseReleased: true,
		},
	}
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
		observability.NewObserver(provider, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Recover(t.Context(), request, recoveryFence); err != nil {
		t.Fatalf("bounded recovery publication renewal: %v", err)
	}
	if !boundedRenewalObserved {
		t.Fatal("detached recovery publication renewal did not expose a bounded context")
	}
	assertConnectedRecoveryTrace(t, exporter.GetSpans())
}

func assertConnectedRecoveryTrace(t *testing.T, spans tracetest.SpanStubs) {
	t.Helper()
	byName := make(map[string]tracetest.SpanStub, len(spans))
	for _, span := range spans {
		byName[span.Name] = span
	}
	root, ok := byName["forja.scheduler.recover_delivery"]
	if !ok {
		t.Fatalf("recovery root missing from spans: %#v", spans)
	}
	for _, name := range []string{
		"forja.validation.validate_change",
		"forja.delivery.publish_change",
	} {
		span, ok := byName[name]
		if !ok {
			t.Fatalf("recovery child %q missing from spans: %#v", name, spans)
		}
		if span.Parent.SpanID() != root.SpanContext.SpanID() {
			t.Fatalf("recovery child %q is disconnected from scheduler root", name)
		}
	}
}

func TestPipelineRecoveryRefreshesSchedulerAfterDetachedPublicationRenewal(t *testing.T) {
	request := pipelineRequestFixture(t)
	recoveryFence, repository := authorizedPipelineFixture(t, request)
	repository.run.State = string(runstate.StateValidating)
	repository.attempt.Status = "succeeded"
	repository.leaseFound = true
	repository.leaseState = "active"
	repository.leaseSet = persistence.LeaseSet{
		LeaseSetID: request.AttemptID, OwnerID: "pipeline-test",
	}
	repository.schedulerRenewHook = func(_ context.Context, ordinal int) error {
		if ordinal == 3 {
			return errors.New("scheduler fence expired during detached delivery renewal")
		}
		return nil
	}
	receipt := pipelineReceiptFixture(request)
	sideEffects := &pipelineSideEffectStub{
		commit: delivery.CommitResult{
			BaseCommit: request.BaseCommit, ResultCommit: receipt.ResultCommit,
		},
		bundle: delivery.ValidationBundle{
			Report: contracts.ValidationReport{Status: "passed"},
		},
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Recover(t.Context(), request, recoveryFence); err == nil ||
		!strings.Contains(err.Error(), "renew recovery scheduler fence before publication") {
		t.Fatalf("stale publication scheduler error = %v", err)
	}
	if repository.leaseSetRenewals != 2 || repository.schedulerRenewals != 3 {
		t.Fatalf(
			"publication renewals = delivery %d scheduler %d, want 2 and 3",
			repository.leaseSetRenewals,
			repository.schedulerRenewals,
		)
	}
	if sideEffects.commitCalls != 1 || sideEffects.publicationCalls != 0 {
		t.Fatalf(
			"stale publication scheduler reached wrong stages: commits=%d publications=%d",
			sideEffects.commitCalls,
			sideEffects.publicationCalls,
		)
	}
}

func TestPipelineRecoveryRefreshesSchedulerAfterDeliveryLeaseRenewal(t *testing.T) {
	request := pipelineRequestFixture(t)
	recoveryFence, repository := authorizedPipelineFixture(t, request)
	repository.run.State = string(runstate.StateValidating)
	repository.attempt.Status = "succeeded"
	repository.leaseFound = true
	repository.leaseState = "active"
	repository.leaseSet = persistence.LeaseSet{
		LeaseSetID: request.AttemptID, OwnerID: "pipeline-test",
	}
	repository.schedulerRenewHook = func(_ context.Context, ordinal int) error {
		if ordinal == 2 {
			return errors.New("scheduler fence was replaced while delivery renewal blocked")
		}
		return nil
	}
	sideEffects := &pipelineSideEffectStub{
		commit: delivery.CommitResult{
			BaseCommit: request.BaseCommit, ResultCommit: strings.Repeat("c", 40),
		},
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Recover(t.Context(), request, recoveryFence); err == nil ||
		!strings.Contains(err.Error(), "renew recovery scheduler fence before Git mutation") {
		t.Fatalf("stale recovery scheduler error = %v", err)
	}
	if repository.leaseSetRenewals != 1 || repository.schedulerRenewals != 2 {
		t.Fatalf(
			"recovery renewals = delivery %d scheduler %d, want 1 and 2",
			repository.leaseSetRenewals,
			repository.schedulerRenewals,
		)
	}
	if sideEffects.commitCalls != 0 {
		t.Fatalf("stale recovery mutated Git %d times", sideEffects.commitCalls)
	}
}

func TestPipelineRecoveryCleansRejectedValidationWithoutJournal(t *testing.T) {
	request := pipelineRequestFixture(t)
	recoveryFence, repository := authorizedPipelineFixture(t, request)
	repository.run.State = string(runstate.StateValidating)
	repository.attempt.Status = "succeeded"
	repository.leaseFound = true
	repository.leaseState = "active"
	repository.leaseSet = persistence.LeaseSet{
		LeaseSetID: request.AttemptID, OwnerID: "pipeline-test",
	}
	sideEffects := &pipelineSideEffectStub{
		commit: delivery.CommitResult{
			BaseCommit: request.BaseCommit, ResultCommit: strings.Repeat("c", 40),
		},
		bundle: delivery.ValidationBundle{
			Report: contracts.ValidationReport{Status: "failed"},
		},
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := pipeline.Recover(t.Context(), request, recoveryFence)
	if err == nil || !strings.Contains(err.Error(), "validation status") {
		t.Fatalf("rejected validation recovery error = %v", err)
	}
	if outcome.Run.State != string(runstate.StateFailedTerminal) ||
		sideEffects.quarantines != 1 || repository.releases != 1 ||
		outcome.Quarantine == nil {
		t.Fatalf(
			"rejected recovery was not cleaned: outcome=%#v quarantines=%d releases=%d",
			outcome, sideEffects.quarantines, repository.releases,
		)
	}
}

func TestPipelineRecoveryCleansRejectedValidationAfterLeaseRelease(t *testing.T) {
	request := pipelineRequestFixture(t)
	recoveryFence, repository := authorizedPipelineFixture(t, request)
	repository.run.State = string(runstate.StateValidating)
	repository.attempt.Status = "succeeded"
	repository.leaseFound = true
	repository.leaseState = "released"
	repository.leaseSet = persistence.LeaseSet{
		LeaseSetID: request.AttemptID, OwnerID: "pipeline-test",
	}
	sideEffects := &pipelineSideEffectStub{
		bundle: delivery.ValidationBundle{
			Report: contracts.ValidationReport{Status: "failed"},
		},
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := pipeline.Recover(t.Context(), request, recoveryFence)
	if err == nil || !strings.Contains(err.Error(), "validation status") {
		t.Fatalf("released rejection recovery error = %v", err)
	}
	if outcome.Run.State != string(runstate.StateFailedTerminal) ||
		repository.releases != 0 || sideEffects.quarantines != 1 || outcome.Quarantine == nil {
		t.Fatalf(
			"released rejection was misclassified: outcome=%#v releases=%d quarantines=%d",
			outcome, repository.releases, sideEffects.quarantines,
		)
	}
}

func TestPipelineRecoveryKeepsRejectedValidationRecoverableWhenCleanupFails(t *testing.T) {
	request := pipelineRequestFixture(t)
	recoveryFence, repository := authorizedPipelineFixture(t, request)
	repository.run.State = string(runstate.StateValidating)
	repository.attempt.Status = "succeeded"
	repository.leaseFound = true
	repository.leaseState = "active"
	repository.leaseSet = persistence.LeaseSet{
		LeaseSetID: request.AttemptID, OwnerID: "pipeline-test",
	}
	repository.releaseErr = errors.New("transient recovery lease release failure")
	sideEffects := &pipelineSideEffectStub{
		commit: delivery.CommitResult{
			BaseCommit: request.BaseCommit, ResultCommit: strings.Repeat("c", 40),
		},
		bundle: delivery.ValidationBundle{
			Report: contracts.ValidationReport{Status: "failed"},
		},
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Recover(t.Context(), request, recoveryFence); err == nil || !strings.Contains(err.Error(), "recovery lease release failure") {
		t.Fatalf("recovery cleanup error = %v", err)
	}
	if repository.run.State != string(runstate.StateValidating) ||
		sideEffects.quarantines != 1 {
		t.Fatalf("failed cleanup closed recovery: run=%#v quarantines=%d", repository.run, sideEffects.quarantines)
	}
}

func TestPipelineRecoverySettlesJournaledPublicationConflict(t *testing.T) {
	request := pipelineRequestFixture(t)
	recoveryFence, repository := authorizedPipelineFixture(t, request)
	repository.run.State = string(runstate.StateValidating)
	repository.attempt.Status = "succeeded"
	repository.leaseFound = true
	repository.leaseState = "active"
	repository.leaseSet = persistence.LeaseSet{
		LeaseSetID: request.AttemptID, OwnerID: "pipeline-test",
	}
	repository.publicationFound = true
	repository.publication = persistence.DeliveryPublication{State: "conflict"}
	sideEffects := &pipelineSideEffectStub{
		commit: delivery.CommitResult{
			BaseCommit: request.BaseCommit, ResultCommit: strings.Repeat("c", 40),
		},
		bundle: delivery.ValidationBundle{
			Report: contracts.ValidationReport{Status: "passed"},
		},
		publicationErr: delivery.ErrPublicationConflict,
	}
	pipeline, err := NewPipeline(
		repository, sideEffects, sideEffects, sideEffects, sideEffects, "pipeline-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := pipeline.Recover(t.Context(), request, recoveryFence)
	if !errors.Is(err, delivery.ErrPublicationConflict) {
		t.Fatalf("recover publication conflict error = %v", err)
	}
	if outcome.Run.State != string(runstate.StateFailedTerminal) ||
		repository.releases != 1 || sideEffects.quarantines != 1 || outcome.Quarantine == nil {
		t.Fatalf(
			"recovered conflict was not settled: outcome=%#v releases=%d quarantines=%d",
			outcome, repository.releases, sideEffects.quarantines,
		)
	}
	if sideEffects.commitCalls != 0 {
		t.Fatalf("conflict recovery rebuilt a quarantined commit %d times", sideEffects.commitCalls)
	}
}

func pipelineRequestFixture(t *testing.T) contracts.DeliveryRequest {
	t.Helper()
	repository := t.TempDir()
	worktreeRoot := t.TempDir()
	previous := strings.Repeat("b", 40)
	deliveryID := "delivery_11111111-1111-4111-8111-111111111111"
	return contracts.DeliveryRequest{
		DeliveryID:                deliveryID,
		TenantID:                  "tenant_" + control.LocalTenantID,
		RepositoryID:              "repo_" + control.LocalRepositoryID,
		TaskID:                    "task_11111111-1111-4111-8111-111111111111",
		AttemptID:                 "attempt_11111111-1111-4111-8111-111111111111",
		RunID:                     "run_11111111-1111-4111-8111-111111111111",
		SchemaVersion:             contracts.DeliverySchemaVersion,
		RepositoryPath:            repository,
		WorktreeRoot:              worktreeRoot,
		BaseCommit:                strings.Repeat("a", 40),
		PublicationRef:            "refs/forja/deliveries/" + deliveryID,
		PublicationPreviousCommit: &previous,
		AuthorID:                  "worker-author",
		ValidatorID:               "independent-validator",
		Role:                      "implementer",
		Objective:                 "Implement the exact approved task.",
		ReadScopes:                []string{"."},
		WriteScopes:               []string{"internal/generated"},
		ArtifactScopes:            []string{"evidence"},
		EvidenceScope:             "evidence",
		AttemptOrdinal:            1,
		WorkerBudgets: contracts.WorkerBudgets{
			WallClockMS: 1_000, InactivityMS: 500, MaxOutputBytes: 4_096,
			CancellationGraceMS: 100, MaxRetries: 1,
		},
		MechanicalValidatorIDs: []string{"unit-tests"},
		LeaseTTLMS:             contracts.MinimumPublicationLeaseTTLMS,
	}
}

func authorizedPipelineFixture(
	t *testing.T,
	request contracts.DeliveryRequest,
) (persistence.LeaseProof, *pipelineRepositoryStub) {
	t.Helper()
	fence := persistence.LeaseProof{
		LeaseKey: persistence.LeaseKey{
			TenantID: control.LocalTenantID, RepositoryID: control.LocalRepositoryID,
			ResourceType: "scheduler", ResourceID: "scheduler-test",
		},
		OwnerID: "scheduler-owner", FencingToken: 7,
	}
	digest, err := deliveryRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	return fence, &pipelineRepositoryStub{
		authority: control.Authority{
			TenantID: control.LocalTenantID, RepositoryID: control.LocalRepositoryID,
		},
		run: contracts.Run{
			RunID: request.RunID, Objective: request.Objective,
			State: string(runstate.StateQueued), Version: 3,
		},
		attempt: persistence.Attempt{
			AttemptID: request.AttemptID, RunID: request.RunID,
			Ordinal: request.AttemptOrdinal, Status: "queued", Version: 1,
			LeaseResourceType: fence.ResourceType, LeaseResourceID: fence.ResourceID,
			WorkerID: fence.OwnerID, FencingToken: fence.FencingToken,
		},
		authorization: persistence.DeliveryAuthorization{
			Request: request, RequestSHA256: digest,
			ApprovedBy: "independent-human", ApprovedAt: time.Now().UTC(),
		},
	}
}

func pipelineReceiptFixture(request contracts.DeliveryRequest) contracts.DeliveryReceipt {
	now := time.Now().UTC()
	return contracts.DeliveryReceipt{
		DeliveryID: request.DeliveryID, TenantID: request.TenantID,
		RepositoryID: request.RepositoryID, SchemaVersion: contracts.DeliverySchemaVersion,
		Status: "published", BaseCommit: request.BaseCommit,
		ResultCommit: strings.Repeat("c", 40), ResultTree: strings.Repeat("d", 40),
		PatchSHA256: strings.Repeat("e", 64), ChangedPaths: []string{"internal/generated/value.json"},
		PublicationRef:            request.PublicationRef,
		PublicationPreviousCommit: request.PublicationPreviousCommit,
		AuthorID:                  request.AuthorID, ValidatorID: request.ValidatorID,
		LeaseFences: []contracts.DeliveryLeaseFence{
			{ResourceType: "worktree", ResourceID: request.DeliveryID, OwnerID: "pipeline-test", FencingToken: 1},
			{ResourceType: "file", ResourceID: "internal/generated", OwnerID: "pipeline-test", FencingToken: 2},
			{ResourceType: "artifact", ResourceID: "evidence", OwnerID: "pipeline-test", FencingToken: 3},
		},
		ValidationReportRef: "evidence/report.json#sha256=" + strings.Repeat("a", 64),
		EvidenceManifestRef: "evidence/manifest.json#sha256=" + strings.Repeat("b", 64),
		CreatedAt:           now, PublishedAt: now,
	}
}

func successfulWorkerResult(request contracts.DeliveryRequest) contracts.WorkerResult {
	now := time.Now().UTC()
	exitCode := 0
	emptyHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	return contracts.WorkerResult{
		TaskID: request.TaskID, AttemptID: request.AttemptID, RunID: request.RunID,
		SchemaVersion: workerTaskSchemaVersion, Adapter: "test-adapter",
		Status: "succeeded", Retryable: false, TerminationReason: "completed",
		StartedAt: now, FinishedAt: now, DurationMS: 0, ExitCode: &exitCode,
		Stdout: "", Stderr: "", StdoutSHA256: emptyHash, StderrSHA256: emptyHash,
		Usage: contracts.WorkerUsage{}, EvidenceRefs: []string{},
		Report: &contracts.WorkerReport{
			Status: "completed", Summary: "Completed before telemetry failed.",
			ChangedPaths: []string{}, EvidenceRefs: []string{}, Risks: []string{},
		},
	}
}

func cancelledWorkerResult(request contracts.DeliveryRequest) contracts.WorkerResult {
	result := successfulWorkerResult(request)
	result.Status = "cancelled"
	result.Retryable = false
	result.TerminationReason = "cancelled"
	result.ExitCode = nil
	result.Report = nil
	return result
}

func nonSuccessWorkerResult(
	request contracts.DeliveryRequest,
	status string,
) contracts.WorkerResult {
	result := successfulWorkerResult(request)
	result.Status = status
	result.Report = nil
	result.ExitCode = nil
	switch status {
	case "blocked":
		result.TerminationReason = "worker_blocked"
		result.Report = &contracts.WorkerReport{
			Status: "blocked", Summary: "Independent decision is required.",
			ChangedPaths: []string{}, EvidenceRefs: []string{}, Risks: []string{},
		}
	case "failed_retryable":
		result.Retryable = true
		result.TerminationReason = "process_failure"
	case "failed_terminal":
		result.TerminationReason = "process_failure"
	default:
		panic("unsupported test worker status " + status)
	}
	return result
}

type pipelineRepositoryStub struct {
	authority             control.Authority
	run                   contracts.Run
	attempt               persistence.Attempt
	authorization         persistence.DeliveryAuthorization
	transitions           int
	leaseAcquisitions     int
	releases              int
	publication           persistence.DeliveryPublication
	publicationFound      bool
	leaseSet              persistence.LeaseSet
	leaseState            string
	leaseFound            bool
	verifyErr             error
	releaseErr            error
	schedulerRenewals     int
	schedulerRenewHook    func(context.Context, int) error
	leaseSetRenewals      int
	leaseSetRenewErrAfter int
	leaseSetRenewErr      error
	leaseSetRenewHook     func(context.Context, int) error
	acquireErr            error
	startErr              error
	finishErr             error
	releaseHook           func() error
	transitionHook        func(runstate.State) error
}

func (s *pipelineRepositoryStub) Authority() control.Authority { return s.authority }

func (s *pipelineRepositoryStub) GetRun(context.Context, identity.RunID) (contracts.Run, error) {
	return s.run, nil
}

func (s *pipelineRepositoryStub) TransitionRun(
	_ context.Context,
	_ identity.RunID,
	_ int,
	target runstate.State,
	_ runstate.CommandMetadata,
) (contracts.Run, error) {
	if s.transitionHook != nil {
		if err := s.transitionHook(target); err != nil {
			return contracts.Run{}, err
		}
	}
	s.transitions++
	s.run.State = string(target)
	s.run.Version++
	return s.run, nil
}

func (s *pipelineRepositoryStub) GetAttempt(context.Context, string) (persistence.Attempt, error) {
	return s.attempt, nil
}

func (s *pipelineRepositoryStub) GetDeliveryPublication(
	context.Context,
	string,
	string,
) (persistence.DeliveryPublication, bool, error) {
	return s.publication, s.publicationFound, nil
}

func (s *pipelineRepositoryStub) GetLeaseSet(
	context.Context,
	string,
) (persistence.LeaseSet, string, bool, error) {
	return s.leaseSet, s.leaseState, s.leaseFound, nil
}

func (s *pipelineRepositoryStub) ReconcileAbandonedAttempts(
	context.Context,
	runstate.CommandMetadata,
	persistence.LeaseProof,
) ([]persistence.Attempt, error) {
	return nil, nil
}

func (s *pipelineRepositoryStub) AuthorizeDelivery(
	context.Context,
	contracts.DeliveryRequest,
	runstate.CommandMetadata,
) (persistence.DeliveryAuthorization, error) {
	return s.authorization, nil
}

func (s *pipelineRepositoryStub) GetDeliveryAuthorization(
	context.Context,
	string,
	string,
) (persistence.DeliveryAuthorization, bool, error) {
	return s.authorization, s.authorization.RequestSHA256 != "", nil
}

func (s *pipelineRepositoryStub) VerifyLease(
	context.Context,
	persistence.LeaseProof,
	time.Duration,
) (persistence.Lease, error) {
	return persistence.Lease{}, s.verifyErr
}

func (s *pipelineRepositoryStub) AcquireLease(
	context.Context,
	persistence.LeaseKey,
	string,
	time.Duration,
) (persistence.Lease, error) {
	return persistence.Lease{}, nil
}

func (s *pipelineRepositoryStub) RenewLease(
	ctx context.Context,
	_ persistence.LeaseKey,
	_ string,
	_ int64,
	_ time.Duration,
) (persistence.Lease, error) {
	s.schedulerRenewals++
	if s.schedulerRenewHook != nil {
		if err := s.schedulerRenewHook(ctx, s.schedulerRenewals); err != nil {
			return persistence.Lease{}, err
		}
	}
	return persistence.Lease{}, nil
}

func (s *pipelineRepositoryStub) ReleaseLease(
	context.Context,
	persistence.LeaseKey,
	string,
	int64,
) error {
	return nil
}

func (s *pipelineRepositoryStub) StartAttempt(
	context.Context,
	string,
	int,
	runstate.CommandMetadata,
	persistence.LeaseProof,
) (persistence.Attempt, error) {
	if s.startErr != nil {
		return persistence.Attempt{}, s.startErr
	}
	s.attempt.Status = "running"
	s.attempt.Version++
	return s.attempt, nil
}

func (s *pipelineRepositoryStub) FinishAttempt(
	_ context.Context,
	_ string,
	_ int,
	result contracts.WorkerResult,
	_ runstate.CommandMetadata,
	_ persistence.LeaseProof,
) (persistence.Attempt, error) {
	if s.finishErr != nil {
		return persistence.Attempt{}, s.finishErr
	}
	s.attempt.Status = result.Status
	s.attempt.Version++
	return s.attempt, nil
}

func (s *pipelineRepositoryStub) AcquireLeaseSet(
	_ context.Context,
	leaseSetID string,
	keys []persistence.LeaseKey,
	ownerID string,
	ttl time.Duration,
) (persistence.LeaseSet, error) {
	s.leaseAcquisitions++
	if s.acquireErr != nil {
		return persistence.LeaseSet{}, s.acquireErr
	}
	leasing := make([]persistence.Lease, len(keys))
	for index, key := range keys {
		leasing[index] = persistence.Lease{
			LeaseKey: key, OwnerID: ownerID, FencingToken: int64(index + 1),
			AcquiredAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(ttl),
		}
	}
	return persistence.LeaseSet{
		LeaseSetID: leaseSetID, OwnerID: ownerID, Leases: leasing,
		AcquiredAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(ttl),
	}, nil
}

func (s *pipelineRepositoryStub) RenewLeaseSet(
	ctx context.Context,
	leaseSet persistence.LeaseSet,
	_ time.Duration,
) (persistence.LeaseSet, error) {
	s.leaseSetRenewals++
	if s.leaseSetRenewHook != nil {
		if err := s.leaseSetRenewHook(ctx, s.leaseSetRenewals); err != nil {
			return persistence.LeaseSet{}, err
		}
	}
	if s.leaseSetRenewErrAfter > 0 && s.leaseSetRenewals >= s.leaseSetRenewErrAfter {
		return persistence.LeaseSet{}, s.leaseSetRenewErr
	}
	return leaseSet, nil
}

func (s *pipelineRepositoryStub) ReleaseLeaseSet(context.Context, persistence.LeaseSet) error {
	s.releases++
	if s.releaseHook != nil {
		if err := s.releaseHook(); err != nil {
			return err
		}
	}
	return s.releaseErr
}

type pipelineSideEffectStub struct {
	calls                 int
	commitCalls           int
	quarantines           int
	workerCalls           int
	recoveryCalls         int
	publicationCalls      int
	workerErr             error
	workerResult          contracts.WorkerResult
	commit                delivery.CommitResult
	bundle                delivery.ValidationBundle
	loadCalls             int
	loadErr               error
	publicationResult     delivery.PublicationResult
	publicationErr        error
	quarantineErr         error
	publicationDeadline   bool
	publicationContextErr error
	prepareErr            error
	validationHook        func()
	prepareHook           func()
	workerHook            func(context.Context, contracts.WorkerTask) (contracts.WorkerResult, error)
	commitHook            func(context.Context) (delivery.CommitResult, error)
}

func (s *pipelineSideEffectStub) Prepare(
	_ context.Context,
	request contracts.DeliveryRequest,
) (delivery.Worktree, error) {
	s.calls++
	if s.prepareHook != nil {
		s.prepareHook()
	}
	if s.prepareErr != nil {
		return delivery.Worktree{}, s.prepareErr
	}
	return delivery.Worktree{
		DeliveryID: request.DeliveryID, AttemptID: request.AttemptID,
		RepositoryPath: request.RepositoryPath,
		Path:           filepath.Join(request.WorktreeRoot, request.DeliveryID, request.AttemptID),
		BaseCommit:     request.BaseCommit,
	}, nil
}

func (s *pipelineSideEffectStub) CreateResultCommit(
	ctx context.Context,
	_ contracts.DeliveryRequest,
) (delivery.CommitResult, error) {
	s.calls++
	s.commitCalls++
	if s.commitHook != nil {
		return s.commitHook(ctx)
	}
	return s.commit, nil
}

func (s *pipelineSideEffectStub) Quarantine(
	context.Context,
	contracts.DeliveryRequest,
) (delivery.QuarantineResult, error) {
	s.calls++
	s.quarantines++
	return delivery.QuarantineResult{}, s.quarantineErr
}

func (s *pipelineSideEffectStub) Execute(
	ctx context.Context,
	task contracts.WorkerTask,
) (contracts.WorkerResult, error) {
	s.calls++
	s.workerCalls++
	if s.workerHook != nil {
		return s.workerHook(ctx, task)
	}
	return s.workerResult, s.workerErr
}

func (s *pipelineSideEffectStub) Validate(
	context.Context,
	contracts.DeliveryRequest,
	delivery.CommitResult,
) (delivery.ValidationBundle, error) {
	s.calls++
	if s.validationHook != nil {
		s.validationHook()
	}
	return s.bundle, nil
}

func (s *pipelineSideEffectStub) Load(
	context.Context,
	contracts.DeliveryRequest,
) (delivery.ValidationBundle, error) {
	s.calls++
	s.loadCalls++
	return s.bundle, s.loadErr
}

func (s *pipelineSideEffectStub) Publish(
	ctx context.Context,
	_ contracts.DeliveryRequest,
	_ delivery.CommitResult,
	_ delivery.ValidationBundle,
	_ persistence.LeaseSet,
) (delivery.PublicationResult, error) {
	s.calls++
	s.publicationCalls++
	_, s.publicationDeadline = ctx.Deadline()
	s.publicationContextErr = ctx.Err()
	return s.publicationResult, s.publicationErr
}

func (s *pipelineSideEffectStub) Recover(
	ctx context.Context,
	_ contracts.DeliveryRequest,
	_ delivery.CommitResult,
	_ delivery.ValidationBundle,
	_ persistence.LeaseSet,
) (delivery.PublicationResult, error) {
	s.calls++
	s.recoveryCalls++
	s.publicationCalls++
	_, s.publicationDeadline = ctx.Deadline()
	s.publicationContextErr = ctx.Err()
	return s.publicationResult, s.publicationErr
}
