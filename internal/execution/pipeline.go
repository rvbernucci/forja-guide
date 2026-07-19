// Package execution composes governed approval, bounded workers, validation,
// and fenced publication without moving authority into an adapter.
package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/delivery"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/observability"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

const (
	workerTaskSchemaVersion = "1.0"
	cleanupTimeout          = 45 * time.Second
)

// Repository is the durable state and fencing boundary required by one
// governed execution. The scheduler creates the queued attempt before calling
// the pipeline; this boundary starts and finishes that exact attempt.
type Repository interface {
	Authority() control.Authority
	GetRun(context.Context, identity.RunID) (contracts.Run, error)
	TransitionRun(
		context.Context,
		identity.RunID,
		int,
		runstate.State,
		runstate.CommandMetadata,
	) (contracts.Run, error)
	GetAttempt(context.Context, string) (persistence.Attempt, error)
	GetDeliveryPublication(
		context.Context,
		string,
		string,
	) (persistence.DeliveryPublication, bool, error)
	GetLeaseSet(
		context.Context,
		string,
	) (persistence.LeaseSet, string, bool, error)
	StartAttempt(
		context.Context,
		string,
		int,
		runstate.CommandMetadata,
		persistence.LeaseProof,
	) (persistence.Attempt, error)
	FinishAttempt(
		context.Context,
		string,
		int,
		contracts.WorkerResult,
		runstate.CommandMetadata,
		persistence.LeaseProof,
	) (persistence.Attempt, error)
	ReconcileAbandonedAttempts(
		context.Context,
		runstate.CommandMetadata,
		persistence.LeaseProof,
	) ([]persistence.Attempt, error)
	persistence.DeliveryAuthorizationRepository
	persistence.LeaseRepository
	persistence.LeaseSetRepository
}

// Worktrees is the trusted Git mutation boundary used by the pipeline.
type Worktrees interface {
	Prepare(context.Context, contracts.DeliveryRequest) (delivery.Worktree, error)
	CreateResultCommit(context.Context, contracts.DeliveryRequest) (delivery.CommitResult, error)
	Quarantine(context.Context, contracts.DeliveryRequest) (delivery.QuarantineResult, error)
}

// Worker executes exactly one schema-bound process attempt.
type Worker interface {
	Execute(context.Context, contracts.WorkerTask) (contracts.WorkerResult, error)
}

// Validator independently checks a supervisor-created result commit.
type Validator interface {
	Validate(
		context.Context,
		contracts.DeliveryRequest,
		delivery.CommitResult,
	) (delivery.ValidationBundle, error)
	Load(
		context.Context,
		contracts.DeliveryRequest,
	) (delivery.ValidationBundle, error)
}

// Publisher owns journal-before-CAS-before-receipt publication.
type Publisher interface {
	Publish(
		context.Context,
		contracts.DeliveryRequest,
		delivery.CommitResult,
		delivery.ValidationBundle,
		persistence.LeaseSet,
	) (delivery.PublicationResult, error)
	Recover(
		context.Context,
		contracts.DeliveryRequest,
		delivery.CommitResult,
		delivery.ValidationBundle,
		persistence.LeaseSet,
	) (delivery.PublicationResult, error)
}

// Outcome exposes only canonical stage results. Empty later-stage values mean
// the pipeline failed closed before reaching that boundary.
type Outcome struct {
	Run         contracts.Run
	Attempt     persistence.Attempt
	Worktree    delivery.Worktree
	Worker      contracts.WorkerResult
	Commit      delivery.CommitResult
	Validation  delivery.ValidationBundle
	Publication delivery.PublicationResult
	Quarantine  *delivery.QuarantineResult
}

// Pipeline composes existing authority boundaries into one governed delivery.
type Pipeline struct {
	repository        Repository
	worktrees         Worktrees
	worker            Worker
	validator         Validator
	publisher         Publisher
	registry          *contracts.Registry
	ownerID           string
	heartbeatInterval func(time.Duration) time.Duration
	observer          *observability.Observer
}

// NewPipeline constructs one repository-bound orchestration pipeline.
func NewPipeline(
	repository Repository,
	worktrees Worktrees,
	worker Worker,
	validator Validator,
	publisher Publisher,
	ownerID string,
	observers ...*observability.Observer,
) (*Pipeline, error) {
	if repository == nil || worktrees == nil || worker == nil || validator == nil || publisher == nil {
		return nil, fmt.Errorf("execution pipeline requires repository, worktree, worker, validator, and publisher services")
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" || len(ownerID) > 160 {
		return nil, fmt.Errorf("execution pipeline owner ID must contain between 1 and 160 bytes")
	}
	if len(observers) > 1 {
		return nil, fmt.Errorf("execution pipeline accepts at most one observer")
	}
	observer := observability.NewObserver(nil, nil)
	if len(observers) == 1 && observers[0] != nil {
		observer = observers[0]
	}
	registry, err := contracts.NewRegistry()
	if err != nil {
		return nil, fmt.Errorf("compile execution contracts: %w", err)
	}
	return &Pipeline{
		repository:        repository,
		worktrees:         worktrees,
		worker:            worker,
		validator:         validator,
		publisher:         publisher,
		registry:          registry,
		ownerID:           ownerID,
		heartbeatInterval: deliveryHeartbeatInterval,
		observer:          observer,
	}, nil
}

// Execute converts one approved, queued run and its exact durable attempt into
// a validated publication receipt. The scheduler fence remains caller-owned;
// the pipeline owns only the request-derived delivery lease set.
func (p *Pipeline) Execute(
	ctx context.Context,
	request contracts.DeliveryRequest,
	schedulerFence persistence.LeaseProof,
) (outcome Outcome, resultErr error) {
	if ctx == nil {
		return Outcome{}, fmt.Errorf("execution context is required")
	}
	ctx, operation := p.observer.Start(
		ctx,
		observability.BoundaryScheduler,
		observability.OperationExecuteDelivery,
	)
	observability.AddCorrelation(ctx, request.RunID, &request.AttemptID)
	defer func() { operation.End(observedPipelineError(outcome, resultErr)) }()
	if err := validateDeliveryRequestDocument(p.registry, request); err != nil {
		return Outcome{}, fmt.Errorf("validate approved delivery request: %w", err)
	}
	runID, err := p.authorize(request, schedulerFence)
	if err != nil {
		return Outcome{}, err
	}
	authorization, found, err := p.repository.GetDeliveryAuthorization(
		ctx, request.DeliveryID, request.AttemptID,
	)
	if err != nil {
		return Outcome{}, fmt.Errorf("load immutable delivery authorization: %w", err)
	}
	if !found {
		return Outcome{}, fmt.Errorf("immutable human delivery authorization is required")
	}
	if err := requireExactAuthorization(request, authorization); err != nil {
		return Outcome{}, err
	}
	run, err := p.repository.GetRun(ctx, runID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load approved run: %w", err)
	}
	attempt, err := p.repository.GetAttempt(ctx, request.AttemptID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load durable attempt: %w", err)
	}
	if err := validateApprovedState(request, run, attempt, schedulerFence); err != nil {
		return Outcome{}, err
	}
	outcome.Run = run
	outcome.Attempt = attempt
	leaseTTL := time.Duration(request.LeaseTTLMS) * time.Millisecond
	if _, err := p.repository.VerifyLease(ctx, schedulerFence, 0); err != nil {
		return outcome, fmt.Errorf("verify live scheduler fence before delivery side effects: %w", err)
	}
	if _, err := p.repository.RenewLease(
		ctx,
		schedulerFence.LeaseKey,
		schedulerFence.OwnerID,
		schedulerFence.FencingToken,
		leaseTTL,
	); err != nil {
		return outcome, fmt.Errorf("renew live scheduler fence before delivery side effects: %w", err)
	}

	run, err = p.transition(ctx, request, runID, run, runstate.StatePreparing, "run-preparing")
	if err != nil {
		return outcome, err
	}
	outcome.Run = run

	leaseSet, err := p.acquireDeliveryLeases(ctx, request)
	if err != nil {
		// The approved Attempt is still queued and may remain protected by its
		// original scheduler fence. Keep the Run non-resumable until fenced
		// recovery proves quiescence and reconciles that Attempt.
		return outcome, fmt.Errorf("acquire delivery lease set: %w", err)
	}
	leaseOwned := true
	prepared := false
	attemptStarted := false
	attemptTerminal := false
	publicationStarted := false
	cleanupManaged := false
	var guard *leaseGuard
	cleanup := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		var cleanupErr error
		if guard != nil {
			current, guardErr := guard.Stop()
			guard = nil
			leaseSet = current
			cleanupErr = errors.Join(cleanupErr, guardErr)
		}
		if prepared {
			quarantine, quarantineErr := p.worktrees.Quarantine(cleanupCtx, request)
			if errors.Is(quarantineErr, delivery.ErrWorktreeNotFound) && !attemptStarted {
				// Before worker exposure, Prepare may fail without ever creating an
				// attempt path or may remove a fresh clean checkout itself.
				prepared = false
			} else if quarantineErr != nil {
				cleanupErr = errors.Join(
					cleanupErr,
					fmt.Errorf("quarantine failed delivery: %w", quarantineErr),
				)
			} else {
				prepared = false
				outcome.Quarantine = &quarantine
			}
		}
		// Never release write authority while an attempt path still lacks a
		// durable quarantine proof. Expiry and fenced recovery are safer than
		// exposing contaminated bytes to an overlapping retry.
		if leaseOwned && !prepared {
			if releaseErr := p.repository.ReleaseLeaseSet(cleanupCtx, leaseSet); releaseErr != nil {
				cleanupErr = errors.Join(
					cleanupErr,
					fmt.Errorf("release failed delivery leases: %w", releaseErr),
				)
			} else {
				leaseOwned = false
			}
		}
		if cleanupErr == nil {
			cleanupManaged = true
		}
		return cleanupErr
	}
	failAfterCleanup := func(target runstate.State, stage string) error {
		if cleanupErr := cleanup(); cleanupErr != nil {
			return cleanupErr
		}
		if !attemptTerminal {
			return nil
		}
		return p.failRun(request, runID, run, target, stage)
	}
	defer func() {
		if resultErr == nil || publicationStarted || cleanupManaged {
			return
		}
		resultErr = errors.Join(resultErr, cleanup())
	}()
	// Acquisition can block behind overlapping delivery scopes. Refresh the
	// newly acquired set and then the scheduler fence synchronously so no Git
	// mutation starts in the heartbeat's first-tick gap with stale authority.
	leaseSet, err = p.repository.RenewLeaseSet(ctx, leaseSet, leaseTTL)
	if err != nil {
		return outcome, errors.Join(
			fmt.Errorf("renew delivery leases before worktree mutation: %w", err),
			failAfterCleanup(runstate.StateFailedRetryable, "pre-prepare-delivery-lease-failed"),
		)
	}
	if _, err := p.repository.RenewLease(
		ctx,
		schedulerFence.LeaseKey,
		schedulerFence.OwnerID,
		schedulerFence.FencingToken,
		leaseTTL,
	); err != nil {
		return outcome, errors.Join(
			fmt.Errorf("renew scheduler fence before worktree mutation: %w", err),
			failAfterCleanup(runstate.StateFailedRetryable, "pre-prepare-scheduler-lease-failed"),
		)
	}
	guard = startLeaseGuard(
		ctx,
		p.repository,
		schedulerFence,
		leaseSet,
		leaseTTL,
		p.heartbeatInterval(leaseTTL),
	)
	executionCtx := guard.Context()

	// Once Prepare is invoked, the attempt namespace may contain pre-existing,
	// partially created, or freshly created bytes even when it returns an error.
	// Cleanup must prove quarantine or pre-worker absence before releasing leases.
	prepared = true
	worktree, err := p.worktrees.Prepare(executionCtx, request)
	if err != nil {
		return outcome, errors.Join(
			fmt.Errorf("prepare isolated worktree: %w", err),
			failAfterCleanup(runstate.StateFailedRetryable, "worktree-prepare-failed"),
		)
	}
	outcome.Worktree = worktree

	attempt, err = p.repository.StartAttempt(
		executionCtx,
		attempt.AttemptID,
		attempt.Version,
		p.metadata(request, "attempt-start"),
		schedulerFence,
	)
	if err != nil {
		return outcome, errors.Join(
			fmt.Errorf("start durable attempt: %w", err),
			failAfterCleanup(runstate.StateFailedRetryable, "attempt-start-failed"),
		)
	}
	attemptStarted = true
	outcome.Attempt = attempt
	run, err = p.transition(executionCtx, request, runID, run, runstate.StateRunning, "run-running")
	if err != nil {
		failure := canonicalInfrastructureFailure(request, "start_failure")
		outcome.Worker = failure
		finishCtx, cancelFinish := context.WithTimeout(
			context.WithoutCancel(ctx), cleanupTimeout,
		)
		finished, finishErr := p.finishAttempt(
			finishCtx, request, attempt, failure, schedulerFence, "attempt-start-transition-failure",
		)
		cancelFinish()
		var failRunErr error
		if finishErr == nil {
			attemptTerminal = true
			outcome.Attempt = finished
			failRunErr = failAfterCleanup(runstate.StateFailedRetryable, "run-start-failed")
		}
		return outcome, errors.Join(
			fmt.Errorf("mark approved run running: %w", err),
			finishErr,
			failRunErr,
		)
	}
	outcome.Run = run

	workerTask := workerTaskFrom(request, worktree)
	workerContext, workerOperation := p.observer.Start(
		executionCtx,
		observability.BoundaryWorker,
		observability.OperationDispatchWorker,
	)
	workerResult, workerErr := p.worker.Execute(workerContext, workerTask)
	workerOperation.End(observedWorkerError(workerResult, workerErr))
	// Stop the guard before persisting the attempt so its final renewal outcome
	// and the durable worker status cannot disagree after a restart.
	leaseSet, guardErr := guard.Stop()
	guard = nil
	// An error means the supervisor boundary did not complete reliably. Never
	// persist a contradictory succeeded/blocked/terminal result alongside it.
	if guardErr != nil {
		workerResult = canonicalInfrastructureFailure(request, "telemetry_failure")
		workerErr = errors.Join(
			workerErr,
			fmt.Errorf("delivery authority was lost during worker execution: %w", guardErr),
		)
	} else if workerErr != nil && workerResult.Status != "failed_retryable" &&
		workerResult.Status != "cancelled" {
		workerResult = canonicalInfrastructureFailure(request, "telemetry_failure")
	}
	if workerResult.Status == "cancelled" {
		stateCtx, cancelState := context.WithTimeout(
			context.WithoutCancel(ctx), cleanupTimeout,
		)
		currentRun, stateErr := p.repository.GetRun(stateCtx, runID)
		cancelState()
		if stateErr != nil ||
			(currentRun.State != string(runstate.StateCancelling) &&
				currentRun.State != string(runstate.StateCancelled)) {
			cancellationErr := stateErr
			if cancellationErr == nil {
				cancellationErr = fmt.Errorf(
					"Run state %q is not cancelling or cancelled", currentRun.State,
				)
			}
			workerResult = canonicalInfrastructureFailure(request, "telemetry_failure")
			workerErr = errors.Join(
				workerErr,
				fmt.Errorf(
					"worker cancellation lacks durable governed Run cancellation: %w",
					cancellationErr,
				),
			)
		} else {
			run = currentRun
			outcome.Run = run
		}
	}
	outcome.Worker = workerResult
	if err := p.validateWorkerResult(request, workerResult); err != nil {
		failure := canonicalInfrastructureFailure(request, "invalid_report")
		outcome.Worker = failure
		finished, finishErr := p.finishAttempt(
			ctx, request, attempt, failure, schedulerFence, "attempt-invalid-result",
		)
		var failRunErr error
		if finishErr == nil {
			attemptTerminal = true
			outcome.Attempt = finished
			failRunErr = failAfterCleanup(runstate.StateFailedRetryable, "worker-result-invalid")
		}
		return outcome, errors.Join(
			fmt.Errorf("validate worker result: %w", err),
			workerErr,
			finishErr,
			failRunErr,
		)
	}
	attempt, err = p.finishAttempt(
		ctx, request, attempt, workerResult, schedulerFence, "attempt-finish",
	)
	if err != nil {
		return outcome, errors.Join(
			fmt.Errorf("finish durable attempt: %w", err),
			workerErr,
		)
	}
	attemptTerminal = true
	outcome.Attempt = attempt
	if guardErr != nil {
		return outcome, errors.Join(
			fmt.Errorf("delivery authority was lost during worker execution: %w", guardErr),
			workerErr,
			failAfterCleanup(runstate.StateFailedRetryable, "worker-lease-lost"),
		)
	}
	if workerErr != nil || workerResult.Status != "succeeded" {
		cleanupErr := cleanup()
		if cleanupErr != nil {
			return outcome, errors.Join(
				workerErr,
				fmt.Errorf("worker attempt ended with status %q", workerResult.Status),
				cleanupErr,
			)
		}
		return outcome, errors.Join(
			workerErr,
			p.completeNonSuccess(ctx, request, runID, run, workerResult.Status, &outcome),
			fmt.Errorf("worker attempt ended with status %q", workerResult.Status),
		)
	}
	// The worker can run for almost the entire lease TTL. Refresh both delivery
	// and scheduler authority synchronously before resuming Git mutation, then
	// restart the guard for commit, validation, and publication preparation.
	leaseSet, err = p.repository.RenewLeaseSet(ctx, leaseSet, leaseTTL)
	if err != nil {
		return outcome, errors.Join(
			fmt.Errorf("renew delivery leases after worker execution: %w", err),
			failAfterCleanup(runstate.StateFailedRetryable, "post-worker-delivery-lease-failed"),
		)
	}
	if _, err := p.repository.RenewLease(
		ctx,
		schedulerFence.LeaseKey,
		schedulerFence.OwnerID,
		schedulerFence.FencingToken,
		leaseTTL,
	); err != nil {
		return outcome, errors.Join(
			fmt.Errorf("renew scheduler fence after worker execution: %w", err),
			failAfterCleanup(runstate.StateFailedRetryable, "post-worker-scheduler-lease-failed"),
		)
	}
	guard = startLeaseGuard(
		ctx,
		p.repository,
		schedulerFence,
		leaseSet,
		leaseTTL,
		p.heartbeatInterval(leaseTTL),
	)
	executionCtx = guard.Context()

	commit, err := p.worktrees.CreateResultCommit(executionCtx, request)
	if err != nil {
		executionCause := context.Cause(executionCtx)
		var heartbeatErr error
		leaseSet, heartbeatErr = guard.Stop()
		guard = nil
		target := runstate.StateFailedTerminal
		stage := "result-commit-failed"
		if heartbeatErr != nil || executionCause != nil ||
			errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			target = runstate.StateFailedRetryable
			stage = "result-commit-interrupted"
		}
		return outcome, errors.Join(
			fmt.Errorf("create supervisor result commit: %w", err),
			heartbeatErr,
			failAfterCleanup(target, stage),
		)
	}
	outcome.Commit = commit
	run, err = p.transition(executionCtx, request, runID, run, runstate.StateValidating, "run-validating")
	if err != nil {
		return outcome, fmt.Errorf("mark approved run validating: %w", err)
	}
	outcome.Run = run

	bundle, err := p.validateObserved(executionCtx, request, commit)
	if err != nil {
		return outcome, errors.Join(
			fmt.Errorf("validate result commit: %w", err),
			failAfterCleanup(runstate.StateFailedRetryable, "validation-error"),
		)
	}
	outcome.Validation = bundle
	if bundle.Report.Status != "passed" {
		if cleanupErr := cleanup(); cleanupErr != nil {
			return outcome, errors.Join(
				fmt.Errorf("independent validation status is %q", bundle.Report.Status),
				cleanupErr,
			)
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		failed, transitionErr := p.transition(
			cleanupCtx,
			request,
			runID,
			run,
			runstate.StateFailedTerminal,
			"validation-rejected",
		)
		cancelCleanup()
		if transitionErr == nil {
			outcome.Run = failed
		}
		return outcome, errors.Join(
			fmt.Errorf("independent validation status is %q", bundle.Report.Status),
			transitionErr,
		)
	}

	leaseSet, err = guard.Stop()
	guard = nil
	if err != nil {
		return outcome, errors.Join(
			fmt.Errorf("delivery lease heartbeat failed during validation: %w", err),
			failAfterCleanup(runstate.StateFailedRetryable, "post-validation-lease-failed"),
		)
	}
	publicationCtx, cancelPublication := context.WithTimeout(
		context.WithoutCancel(ctx), cleanupTimeout,
	)
	leaseSet, err = p.repository.RenewLeaseSet(publicationCtx, leaseSet, leaseTTL)
	if err != nil {
		cancelPublication()
		return outcome, errors.Join(
			fmt.Errorf("renew delivery leases for publication: %w", err),
			failAfterCleanup(runstate.StateFailedRetryable, "publication-lease-failed"),
		)
	}
	if _, err := p.repository.RenewLease(
		publicationCtx,
		schedulerFence.LeaseKey,
		schedulerFence.OwnerID,
		schedulerFence.FencingToken,
		leaseTTL,
	); err != nil {
		cancelPublication()
		return outcome, errors.Join(
			fmt.Errorf("renew scheduler fence for publication: %w", err),
			failAfterCleanup(runstate.StateFailedRetryable, "publication-scheduler-fence-failed"),
		)
	}

	publication, publicationErr := p.publishObserved(
		publicationCtx, request, commit, bundle, leaseSet,
	)
	cancelPublication()
	outcome.Publication = publication
	if publication.Receipt.Status != "published" {
		if publicationErr == nil {
			publicationErr = fmt.Errorf("publisher returned no durable receipt")
		}
		journalCtx, cancelJournal := context.WithTimeout(
			context.WithoutCancel(ctx), cleanupTimeout,
		)
		record, intentPersisted, journalErr := p.repository.GetDeliveryPublication(
			journalCtx, request.DeliveryID, request.AttemptID,
		)
		cancelJournal()
		// An unreadable journal must be treated as potentially prepared. Only a
		// positive absence proof permits the ordinary quarantine/release path.
		publicationStarted = intentPersisted || journalErr != nil
		if journalErr == nil && intentPersisted && record.State == "conflict" {
			cleanupManaged = true
			settled, cleanupErr := p.terminatePublicationConflict(
				ctx, request, runID, run, leaseSet, true, outcome,
			)
			return settled, errors.Join(
				fmt.Errorf("publish validated result: %w", publicationErr),
				cleanupErr,
			)
		}
		var runErr error
		if !publicationStarted {
			runErr = failAfterCleanup(
				runstate.StateFailedRetryable, "publication-pre-journal-failed",
			)
		}
		return outcome, errors.Join(
			fmt.Errorf("publish validated result: %w", publicationErr),
			journalErr,
			runErr,
		)
	}
	publicationStarted = true
	leaseOwned = !publication.LeaseReleased
	completionCtx, cancelCompletion := context.WithTimeout(
		context.WithoutCancel(ctx), cleanupTimeout,
	)
	run, transitionErr := p.transition(
		completionCtx,
		request,
		runID,
		run,
		runstate.StateCompleted,
		"run-completed",
	)
	cancelCompletion()
	if transitionErr == nil {
		outcome.Run = run
	}
	return outcome, errors.Join(publicationErr, transitionErr)
}

func observedWorkerError(result contracts.WorkerResult, err error) error {
	if result.Status == "" || result.Status == "succeeded" {
		return err
	}
	switch result.TerminationReason {
	case "cancelled":
		return context.Canceled
	case "wall_timeout", "inactivity_timeout":
		return context.DeadlineExceeded
	case "budget_rejected", "output_limit", "input_token_limit",
		"output_token_limit", "tool_call_limit":
		return observability.NewFailure(observability.FailureResourceLimit)
	case "start_failure", "telemetry_failure":
		return observability.NewFailure(observability.FailureUnavailable)
	default:
		return observability.NewFailure(observability.FailureWorker)
	}
}

// observedPipelineError preserves an already stable failure class and derives
// routine terminal outcomes from the durable stage result when the returned
// orchestration error is intentionally generic.
func observedPipelineError(outcome Outcome, err error) error {
	if err == nil || observability.Classify(err) != observability.FailureInternal {
		return err
	}
	if outcome.Validation.Report.Status != "" && outcome.Validation.Report.Status != "passed" {
		return observability.NewFailure(observability.FailureValidation)
	}
	if outcome.Worker.Status != "" && outcome.Worker.Status != "succeeded" {
		return observedWorkerError(outcome.Worker, err)
	}
	switch outcome.Attempt.Status {
	case "cancelled":
		return context.Canceled
	case "blocked", "failed_retryable", "failed_terminal":
		return observability.NewFailure(observability.FailureWorker)
	default:
		return err
	}
}

func (p *Pipeline) loadValidationObserved(
	ctx context.Context,
	request contracts.DeliveryRequest,
) (delivery.ValidationBundle, error) {
	ctx, operation := p.observer.Start(
		ctx, observability.BoundaryValidation, observability.OperationValidateChange,
	)
	bundle, err := p.validator.Load(ctx, request)
	operation.End(observedValidationError(bundle, err))
	return bundle, err
}

func (p *Pipeline) validateObserved(
	ctx context.Context,
	request contracts.DeliveryRequest,
	commit delivery.CommitResult,
) (delivery.ValidationBundle, error) {
	ctx, operation := p.observer.Start(
		ctx, observability.BoundaryValidation, observability.OperationValidateChange,
	)
	bundle, err := p.validator.Validate(ctx, request, commit)
	operation.End(observedValidationError(bundle, err))
	return bundle, err
}

func observedValidationError(bundle delivery.ValidationBundle, err error) error {
	if err != nil {
		if errors.Is(err, delivery.ErrValidationEvidenceNotFound) {
			return observability.NewFailure(observability.FailureNotFound)
		}
		return err
	}
	if bundle.Report.Status != "passed" {
		return observability.NewFailure(observability.FailureValidation)
	}
	return nil
}

func (p *Pipeline) publishObserved(
	ctx context.Context,
	request contracts.DeliveryRequest,
	commit delivery.CommitResult,
	bundle delivery.ValidationBundle,
	leaseSet persistence.LeaseSet,
) (delivery.PublicationResult, error) {
	ctx, operation := p.observer.Start(
		ctx, observability.BoundaryDelivery, observability.OperationPublishChange,
	)
	publication, err := p.publisher.Publish(ctx, request, commit, bundle, leaseSet)
	operation.End(observedPublicationError(publication, err))
	return publication, err
}

func (p *Pipeline) recoverPublicationObserved(
	ctx context.Context,
	request contracts.DeliveryRequest,
	commit delivery.CommitResult,
	bundle delivery.ValidationBundle,
	leaseSet persistence.LeaseSet,
) (delivery.PublicationResult, error) {
	ctx, operation := p.observer.Start(
		ctx, observability.BoundaryDelivery, observability.OperationPublishChange,
	)
	publication, err := p.publisher.Recover(ctx, request, commit, bundle, leaseSet)
	operation.End(observedPublicationError(publication, err))
	return publication, err
}

func observedPublicationError(publication delivery.PublicationResult, err error) error {
	if err != nil {
		return err
	}
	if publication.Receipt.Status != "published" {
		return observability.NewFailure(observability.FailureUnavailable)
	}
	return nil
}

func (p *Pipeline) authorize(
	request contracts.DeliveryRequest,
	schedulerFence persistence.LeaseProof,
) (identity.RunID, error) {
	storageTenant, storageRepository, err := contracts.RepositoryStorageIdentity(
		request.TenantID,
		request.RepositoryID,
	)
	if err != nil {
		return "", fmt.Errorf("map delivery authority: %w", err)
	}
	authority := p.repository.Authority()
	if authority.TenantID != storageTenant || authority.RepositoryID != storageRepository {
		return "", fmt.Errorf("delivery authority does not match the bound repository")
	}
	if schedulerFence.TenantID != storageTenant ||
		schedulerFence.RepositoryID != storageRepository ||
		schedulerFence.ResourceType != "scheduler" ||
		schedulerFence.ResourceID == "" ||
		schedulerFence.OwnerID == "" ||
		schedulerFence.FencingToken < 1 {
		return "", fmt.Errorf("a matching live scheduler fence is required")
	}
	runID, err := identity.ParseRunID(request.RunID)
	if err != nil {
		return "", fmt.Errorf("parse approved run ID: %w", err)
	}
	return runID, nil
}

func validateApprovedState(
	request contracts.DeliveryRequest,
	run contracts.Run,
	attempt persistence.Attempt,
	schedulerFence persistence.LeaseProof,
) error {
	if run.RunID != request.RunID || run.Objective != request.Objective ||
		run.State != string(runstate.StateQueued) {
		return fmt.Errorf("delivery request is not bound to an approved queued run")
	}
	if attempt.AttemptID != request.AttemptID ||
		attempt.RunID != request.RunID ||
		attempt.Ordinal != request.AttemptOrdinal ||
		attempt.Status != "queued" ||
		attempt.LeaseResourceType != schedulerFence.ResourceType ||
		attempt.LeaseResourceID != schedulerFence.ResourceID ||
		attempt.WorkerID != schedulerFence.OwnerID ||
		attempt.FencingToken != schedulerFence.FencingToken {
		return fmt.Errorf("delivery request does not match the exact queued scheduler attempt")
	}
	return nil
}

func workerTaskFrom(
	request contracts.DeliveryRequest,
	worktree delivery.Worktree,
) contracts.WorkerTask {
	writableScopes := append(slices.Clone(request.WriteScopes), request.ArtifactScopes...)
	slices.Sort(writableScopes)
	return contracts.WorkerTask{
		TaskID:            request.TaskID,
		AttemptID:         request.AttemptID,
		RunID:             request.RunID,
		SchemaVersion:     workerTaskSchemaVersion,
		Role:              request.Role,
		Objective:         request.Objective,
		RepositoryPath:    request.RepositoryPath,
		WorktreePath:      worktree.Path,
		ReadScopes:        slices.Clone(request.ReadScopes),
		WriteScopes:       writableScopes,
		ContextPackRef:    cloneOptionalString(request.ContextPackRef),
		ResultSchemaRef:   contracts.WorkerReportSchemaRef,
		EvidenceOutputDir: filepath.Join(worktree.Path, filepath.FromSlash(request.EvidenceScope)),
		AttemptOrdinal:    request.AttemptOrdinal,
		Model:             cloneOptionalString(request.Model),
		Budgets:           request.WorkerBudgets,
	}
}

func (p *Pipeline) validateWorkerResult(
	request contracts.DeliveryRequest,
	result contracts.WorkerResult,
) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode worker result: %w", err)
	}
	if err := p.registry.ValidateJSON("worker-result.schema.json", encoded); err != nil {
		return err
	}
	if result.TaskID != request.TaskID || result.AttemptID != request.AttemptID ||
		result.RunID != request.RunID {
		return fmt.Errorf("worker result identity differs from the approved request")
	}
	return nil
}

func (p *Pipeline) acquireDeliveryLeases(
	ctx context.Context,
	request contracts.DeliveryRequest,
) (persistence.LeaseSet, error) {
	storageTenant, storageRepository, err := contracts.RepositoryStorageIdentity(
		request.TenantID,
		request.RepositoryID,
	)
	if err != nil {
		return persistence.LeaseSet{}, err
	}
	encodedKeys := contracts.ExpectedDeliveryFenceKeys(request)
	keys := make([]persistence.LeaseKey, 0, len(encodedKeys))
	for _, encoded := range encodedKeys {
		resourceType, resourceID, ok := strings.Cut(encoded, "\x00")
		if !ok || resourceType == "" || resourceID == "" {
			return persistence.LeaseSet{}, fmt.Errorf("invalid derived delivery fence %q", encoded)
		}
		keys = append(keys, persistence.LeaseKey{
			TenantID: storageTenant, RepositoryID: storageRepository,
			ResourceType: resourceType, ResourceID: resourceID,
		})
	}
	return p.repository.AcquireLeaseSet(
		ctx,
		request.AttemptID,
		keys,
		p.ownerID,
		time.Duration(request.LeaseTTLMS)*time.Millisecond,
	)
}

func (p *Pipeline) transition(
	ctx context.Context,
	request contracts.DeliveryRequest,
	runID identity.RunID,
	run contracts.Run,
	target runstate.State,
	stage string,
) (contracts.Run, error) {
	updated, err := p.repository.TransitionRun(
		ctx,
		runID,
		run.Version,
		target,
		p.metadata(request, stage),
	)
	if err != nil {
		return contracts.Run{}, fmt.Errorf("transition run %s to %s: %w", run.RunID, target, err)
	}
	return updated, nil
}

func (p *Pipeline) finishAttempt(
	ctx context.Context,
	request contracts.DeliveryRequest,
	attempt persistence.Attempt,
	result contracts.WorkerResult,
	schedulerFence persistence.LeaseProof,
	stage string,
) (persistence.Attempt, error) {
	persistenceCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), cleanupTimeout,
	)
	defer cancel()
	return p.repository.FinishAttempt(
		persistenceCtx,
		attempt.AttemptID,
		attempt.Version,
		result,
		p.metadata(request, stage),
		schedulerFence,
	)
}

func (p *Pipeline) failRun(
	request contracts.DeliveryRequest,
	runID identity.RunID,
	run contracts.Run,
	target runstate.State,
	stage string,
) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_, err := p.transition(cleanupCtx, request, runID, run, target, stage)
	return err
}

func (p *Pipeline) completeNonSuccess(
	ctx context.Context,
	request contracts.DeliveryRequest,
	runID identity.RunID,
	run contracts.Run,
	status string,
	outcome *Outcome,
) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	switch status {
	case "blocked":
		validating := run
		if run.State == string(runstate.StateRunning) {
			var err error
			validating, err = p.transition(
				ctx, request, runID, run, runstate.StateValidating, "blocked-validating",
			)
			if err != nil {
				return err
			}
		}
		if validating.State == string(runstate.StateAwaitingDecision) {
			outcome.Run = validating
			return nil
		}
		if validating.State != string(runstate.StateValidating) {
			return fmt.Errorf("blocked attempt cannot close Run state %q", validating.State)
		}
		awaiting, err := p.transition(
			ctx, request, runID, validating, runstate.StateAwaitingDecision, "worker-blocked",
		)
		if err == nil {
			outcome.Run = awaiting
		}
		return err
	case "cancelled":
		if run.State == string(runstate.StateCancelled) {
			outcome.Run = run
			return nil
		}
		if run.State != string(runstate.StateCancelling) {
			return fmt.Errorf(
				"cancelled attempt cannot close Run state %q without a governed cancellation",
				run.State,
			)
		}
		cancelled, err := p.transition(
			ctx, request, runID, run, runstate.StateCancelled, "worker-cancelled",
		)
		if err == nil {
			outcome.Run = cancelled
		}
		return err
	case "failed_retryable":
		if run.State == string(runstate.StateFailedRetryable) {
			outcome.Run = run
			return nil
		}
		failed, err := p.transition(
			ctx, request, runID, run, runstate.StateFailedRetryable, "worker-retryable",
		)
		if err == nil {
			outcome.Run = failed
		}
		return err
	default:
		if run.State == string(runstate.StateFailedTerminal) {
			outcome.Run = run
			return nil
		}
		failed, err := p.transition(
			ctx, request, runID, run, runstate.StateFailedTerminal, "worker-terminal",
		)
		if err == nil {
			outcome.Run = failed
		}
		return err
	}
}

func (p *Pipeline) metadata(
	request contracts.DeliveryRequest,
	stage string,
) runstate.CommandMetadata {
	return runstate.CommandMetadata{
		IdempotencyKey: "delivery:" + request.DeliveryID + ":" + request.AttemptID + ":" + stage,
		ActorType:      "system",
		ActorID:        p.ownerID,
		CorrelationID:  request.DeliveryID,
	}
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
