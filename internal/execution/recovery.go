package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/delivery"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

// Recover resumes only stages with durable, independently re-verifiable
// inputs. An active attempt is never assumed dead: a replacement scheduler
// must first prove expiry through fenced attempt reconciliation.
func (p *Pipeline) Recover(
	ctx context.Context,
	request contracts.DeliveryRequest,
	recoveryFence persistence.LeaseProof,
) (outcome Outcome, resultErr error) {
	if ctx == nil {
		return Outcome{}, fmt.Errorf("recovery context is required")
	}
	if err := validateDeliveryRequestDocument(p.registry, request); err != nil {
		return Outcome{}, fmt.Errorf("validate recovery delivery request: %w", err)
	}
	runID, err := p.authorize(request, recoveryFence)
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
	leaseTTL := time.Duration(request.LeaseTTLMS) * time.Millisecond
	if _, err := p.repository.VerifyLease(ctx, recoveryFence, 0); err != nil {
		return Outcome{}, fmt.Errorf("verify live recovery scheduler fence: %w", err)
	}
	if _, err := p.repository.RenewLease(
		ctx,
		recoveryFence.LeaseKey,
		recoveryFence.OwnerID,
		recoveryFence.FencingToken,
		leaseTTL,
	); err != nil {
		return Outcome{}, fmt.Errorf("renew recovery scheduler fence: %w", err)
	}
	run, err := p.repository.GetRun(ctx, runID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load interrupted run: %w", err)
	}
	attempt, err := p.repository.GetAttempt(ctx, request.AttemptID)
	if err != nil {
		return Outcome{}, fmt.Errorf("load interrupted attempt: %w", err)
	}
	outcome.Run, outcome.Attempt = run, attempt
	if run.RunID != request.RunID || run.Objective != request.Objective ||
		attempt.AttemptID != request.AttemptID || attempt.RunID != request.RunID ||
		attempt.Ordinal != request.AttemptOrdinal {
		return outcome, fmt.Errorf("interrupted state differs from immutable delivery approval")
	}
	// A replacement scheduler may carry a newer owner/token, but it must own the
	// same durable scheduler resource recorded by the attempt. Repository-wide
	// authority alone must never adopt an unrelated scheduler's delivery.
	if attempt.LeaseResourceType != recoveryFence.ResourceType ||
		attempt.LeaseResourceID != recoveryFence.ResourceID {
		return outcome, fmt.Errorf("recovery fence does not own the attempt scheduler resource")
	}

	if (attempt.Status == "queued" || attempt.Status == "running") &&
		!attemptOwnedByFence(attempt, recoveryFence) {
		if _, err := p.repository.ReconcileAbandonedAttempts(
			ctx,
			p.metadata(request, "attempt-recovery-reconcile"),
			recoveryFence,
		); err != nil {
			return outcome, fmt.Errorf("reconcile abandoned attempt: %w", err)
		}
		attempt, err = p.repository.GetAttempt(ctx, request.AttemptID)
		if err != nil {
			return outcome, fmt.Errorf("reload reconciled attempt: %w", err)
		}
		outcome.Attempt = attempt
	}
	if (run.State == string(runstate.StateCancelling) ||
		run.State == string(runstate.StateCancelled)) &&
		attempt.Status != "queued" && attempt.Status != "running" {
		return p.recoverCancellation(ctx, request, runID, run, attempt, outcome)
	}

	switch attempt.Status {
	case "queued", "running":
		return outcome, fmt.Errorf(
			"attempt remains protected by its original live scheduler fence; process quiescence is not proven",
		)
	case "failed_retryable":
		return p.failInterrupted(ctx, request, runID, run, attempt, outcome)
	case "cancelled":
		if run.State == string(runstate.StateCancelled) {
			return p.recoverNonSuccess(ctx, request, runID, run, attempt, outcome)
		}
		return p.failInterrupted(ctx, request, runID, run, attempt, outcome)
	case "blocked", "failed_terminal":
		return p.recoverNonSuccess(ctx, request, runID, run, attempt, outcome)
	case "succeeded":
		return p.resumeSucceeded(ctx, request, recoveryFence, runID, run, attempt, outcome)
	default:
		if run.State == string(runstate.StateCompleted) {
			return p.replayCompleted(ctx, request, run, attempt, outcome)
		}
		return outcome, fmt.Errorf(
			"attempt status %q and Run state %q have no automatic recovery path",
			attempt.Status, run.State,
		)
	}
}

func (p *Pipeline) resumeSucceeded(
	ctx context.Context,
	request contracts.DeliveryRequest,
	recoveryFence persistence.LeaseProof,
	runID identity.RunID,
	run contracts.Run,
	attempt persistence.Attempt,
	outcome Outcome,
) (Outcome, error) {
	if run.State == string(runstate.StateCompleted) {
		return p.replayCompleted(ctx, request, run, attempt, outcome)
	}
	if run.State == string(runstate.StateFailedRetryable) ||
		run.State == string(runstate.StateFailedTerminal) {
		return p.cleanupSucceededTerminal(ctx, request, run, attempt, outcome)
	}
	if run.State != string(runstate.StateRunning) &&
		run.State != string(runstate.StateValidating) {
		return outcome, fmt.Errorf("succeeded attempt cannot resume Run state %q", run.State)
	}
	leaseSet, leaseState, leaseFound, err := p.repository.GetLeaseSet(ctx, request.AttemptID)
	if err != nil {
		return outcome, fmt.Errorf("load recovery lease set: %w", err)
	}
	publicationRecord, publicationFound, err := p.repository.GetDeliveryPublication(
		ctx, request.DeliveryID, request.AttemptID,
	)
	if err != nil {
		return outcome, fmt.Errorf("load recovery publication journal: %w", err)
	}
	if !leaseFound {
		return outcome, fmt.Errorf("recovery lease-set proof is missing")
	}
	// Conflict cleanup may have moved the worktree before the process could
	// close the Run. Settle that durable journal before trying to reconstruct a
	// commit from a source path that is intentionally no longer present.
	if publicationFound && publicationRecord.State == "conflict" {
		settled, cleanupErr := p.terminatePublicationConflict(
			ctx, request, runID, run, leaseSet, leaseState == "active", outcome,
		)
		return settled, errors.Join(delivery.ErrPublicationConflict, cleanupErr)
	}

	leaseTTL := time.Duration(request.LeaseTTLMS) * time.Millisecond
	var guard *leaseGuard
	if leaseState == "active" {
		leaseSet, err = p.repository.RenewLeaseSet(
			ctx, leaseSet, leaseTTL,
		)
		if err == nil {
			// Renewing the delivery set may block long enough for the scheduler
			// fence checked at recovery entry to expire. Refresh it synchronously
			// before the guard's first-tick gap and before any Git mutation.
			if _, schedulerErr := p.repository.RenewLease(
				ctx,
				recoveryFence.LeaseKey,
				recoveryFence.OwnerID,
				recoveryFence.FencingToken,
				leaseTTL,
			); schedulerErr != nil {
				return outcome, fmt.Errorf(
					"renew recovery scheduler fence before Git mutation: %w",
					schedulerErr,
				)
			}
			guard = startLeaseGuard(
				ctx, p.repository, recoveryFence, leaseSet,
				leaseTTL,
				deliveryHeartbeatInterval(leaseTTL),
			)
		}
	}
	if guard == nil && !publicationFound {
		bundle, loadErr := p.validator.Load(ctx, request)
		if loadErr == nil && bundle.Report.Status != "passed" {
			outcome.Validation = bundle
			rejected, cleanupErr := p.rejectRecoveredValidation(
				ctx, request, runID, run, leaseSet, leaseState, outcome,
			)
			return rejected, errors.Join(
				fmt.Errorf("recovered validation status is %q", bundle.Report.Status),
				cleanupErr,
			)
		}
		if loadErr != nil && !errors.Is(loadErr, delivery.ErrValidationEvidenceNotFound) {
			return outcome, fmt.Errorf("load interrupted validation evidence: %w", loadErr)
		}
		return p.failInterrupted(ctx, request, runID, run, attempt, outcome)
	}
	if guard != nil {
		defer func() {
			if guard != nil {
				_, _ = guard.Stop()
			}
		}()
		ctx = guard.Context()
	}

	var commit delivery.CommitResult
	if publicationFound {
		commit, err = p.commitFromPublication(publicationRecord)
		if err != nil {
			return outcome, fmt.Errorf("reconstruct journaled result commit: %w", err)
		}
	} else {
		commit, err = p.worktrees.CreateResultCommit(ctx, request)
		if err != nil {
			return outcome, fmt.Errorf("reconstruct interrupted result commit: %w", err)
		}
	}
	outcome.Commit = commit
	if run.State == string(runstate.StateRunning) {
		run, err = p.transition(
			ctx, request, runID, run, runstate.StateValidating, "recovery-run-validating",
		)
		if err != nil {
			return outcome, err
		}
		outcome.Run = run
	}

	var bundle delivery.ValidationBundle
	if !publicationFound {
		var loadErr error
		bundle, loadErr = p.validator.Load(ctx, request)
		if loadErr != nil {
			if !errors.Is(loadErr, delivery.ErrValidationEvidenceNotFound) {
				return outcome, fmt.Errorf("load interrupted validation evidence: %w", loadErr)
			}
			bundle, err = p.validator.Validate(ctx, request, commit)
			if err != nil {
				return outcome, fmt.Errorf("rerun interrupted validation: %w", err)
			}
		}
		outcome.Validation = bundle
	}
	if !publicationFound && bundle.Report.Status != "passed" {
		var heartbeatErr error
		if guard != nil {
			leaseSet, heartbeatErr = guard.Stop()
			guard = nil
		}
		rejected, cleanupErr := p.rejectRecoveredValidation(
			ctx, request, runID, run, leaseSet, leaseState, outcome,
		)
		return rejected, errors.Join(
			fmt.Errorf("recovered validation status is %q", bundle.Report.Status),
			heartbeatErr,
			cleanupErr,
		)
	}

	if guard != nil {
		leaseSet, err = guard.Stop()
		guard = nil
		if err != nil {
			return outcome, fmt.Errorf("recovery lease heartbeat failed: %w", err)
		}
		renewCtx, cancelRenew := context.WithTimeout(
			context.WithoutCancel(ctx), cleanupTimeout,
		)
		leaseSet, err = p.repository.RenewLeaseSet(
			renewCtx, leaseSet,
			leaseTTL,
		)
		if err != nil {
			cancelRenew()
			return outcome, fmt.Errorf("renew recovery publication lease: %w", err)
		}
		if _, err := p.repository.RenewLease(
			renewCtx,
			recoveryFence.LeaseKey,
			recoveryFence.OwnerID,
			recoveryFence.FencingToken,
			leaseTTL,
		); err != nil {
			cancelRenew()
			return outcome, fmt.Errorf(
				"renew recovery scheduler fence before publication: %w", err,
			)
		}
		cancelRenew()
	}

	var publication delivery.PublicationResult
	var publicationErr error
	publicationCtx, cancelPublication := context.WithTimeout(
		context.WithoutCancel(ctx), cleanupTimeout,
	)
	defer cancelPublication()
	if publicationFound {
		publication, publicationErr = p.publisher.Recover(
			publicationCtx, request, commit, bundle, leaseSet,
		)
	} else {
		publication, publicationErr = p.publisher.Publish(
			publicationCtx, request, commit, bundle, leaseSet,
		)
	}
	outcome.Publication = publication
	if publication.Receipt.Status != "published" {
		if publicationErr == nil {
			publicationErr = fmt.Errorf("publication recovery returned no durable receipt")
		}
		journalCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		record, found, journalErr := p.repository.GetDeliveryPublication(
			journalCtx, request.DeliveryID, request.AttemptID,
		)
		if journalErr == nil && found && record.State == "abandoned" {
			failedOutcome, failErr := p.failInterrupted(
				journalCtx, request, runID, run, attempt, outcome,
			)
			return failedOutcome, errors.Join(publicationErr, failErr)
		}
		if journalErr == nil && found && record.State == "conflict" {
			settled, cleanupErr := p.terminatePublicationConflict(
				journalCtx, request, runID, run, leaseSet,
				leaseState == "active", outcome,
			)
			return settled, errors.Join(publicationErr, cleanupErr)
		}
		return outcome, errors.Join(publicationErr, journalErr)
	}
	completionCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	run, transitionErr := p.transition(
		completionCtx, request, runID, run, runstate.StateCompleted, "recovery-run-completed",
	)
	if transitionErr == nil {
		outcome.Run = run
	}
	return outcome, errors.Join(publicationErr, transitionErr)
}

func (p *Pipeline) cleanupSucceededTerminal(
	ctx context.Context,
	request contracts.DeliveryRequest,
	run contracts.Run,
	attempt persistence.Attempt,
	outcome Outcome,
) (Outcome, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	record, publicationFound, err := p.repository.GetDeliveryPublication(
		cleanupCtx, request.DeliveryID, request.AttemptID,
	)
	if err != nil {
		return outcome, fmt.Errorf("load terminal succeeded-attempt publication: %w", err)
	}
	if publicationFound && (record.State == "prepared" || record.State == "published") {
		return outcome, fmt.Errorf(
			"terminal Run contradicts active publication state %q", record.State,
		)
	}
	leaseSet, state, found, err := p.repository.GetLeaseSet(cleanupCtx, request.AttemptID)
	if err != nil {
		return outcome, fmt.Errorf("load terminal succeeded-attempt lease set: %w", err)
	}
	if !found {
		return outcome, fmt.Errorf("terminal succeeded attempt has no historical lease-set proof")
	}
	quarantine, err := p.worktrees.Quarantine(cleanupCtx, request)
	if err != nil {
		return outcome, fmt.Errorf("quarantine terminal succeeded attempt: %w", err)
	}
	outcome.Quarantine = &quarantine
	if state == "active" {
		if err := p.repository.ReleaseLeaseSet(cleanupCtx, leaseSet); err != nil {
			return outcome, fmt.Errorf("release terminal succeeded-attempt leases: %w", err)
		}
	}
	outcome.Run, outcome.Attempt = run, attempt
	return outcome, fmt.Errorf(
		"cleaned succeeded attempt %s after Run reached %q",
		attempt.AttemptID, run.State,
	)
}

func (p *Pipeline) terminatePublicationConflict(
	ctx context.Context,
	request contracts.DeliveryRequest,
	runID identity.RunID,
	run contracts.Run,
	leaseSet persistence.LeaseSet,
	leaseActive bool,
	outcome Outcome,
) (Outcome, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	quarantine, err := p.worktrees.Quarantine(cleanupCtx, request)
	if err != nil {
		return outcome, fmt.Errorf("quarantine conflicted publication: %w", err)
	}
	outcome.Quarantine = &quarantine
	if leaseActive {
		if err := p.repository.ReleaseLeaseSet(cleanupCtx, leaseSet); err != nil {
			return outcome, fmt.Errorf("release conflicted publication leases: %w", err)
		}
	}
	failed, err := p.transition(
		cleanupCtx,
		request,
		runID,
		run,
		runstate.StateFailedTerminal,
		"publication-conflict",
	)
	if err != nil {
		return outcome, err
	}
	outcome.Run = failed
	return outcome, nil
}

func (p *Pipeline) failInterrupted(
	ctx context.Context,
	request contracts.DeliveryRequest,
	runID identity.RunID,
	run contracts.Run,
	attempt persistence.Attempt,
	outcome Outcome,
) (Outcome, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	leaseSet, state, found, loadErr := p.repository.GetLeaseSet(
		cleanupCtx, request.AttemptID,
	)
	if loadErr != nil {
		return outcome, loadErr
	}
	var cleanupErr error
	if found {
		quarantine, quarantineErr := p.worktrees.Quarantine(cleanupCtx, request)
		if errors.Is(quarantineErr, delivery.ErrWorktreeNotFound) &&
			run.State == string(runstate.StatePreparing) {
			quarantineErr = nil
		}
		if quarantineErr != nil {
			return outcome, errors.Join(
				fmt.Errorf("interrupted attempt %s cleanup is incomplete", attempt.AttemptID),
				quarantineErr,
			)
		} else {
			if quarantine.QuarantinePath != "" {
				outcome.Quarantine = &quarantine
			}
		}
		if state == "active" {
			cleanupErr = errors.Join(
				cleanupErr,
				p.repository.ReleaseLeaseSet(cleanupCtx, leaseSet),
			)
		}
	}
	if cleanupErr != nil {
		return outcome, errors.Join(
			fmt.Errorf("interrupted attempt %s cleanup is incomplete", attempt.AttemptID),
			cleanupErr,
		)
	}
	if run.State == string(runstate.StatePreparing) ||
		run.State == string(runstate.StateRunning) ||
		run.State == string(runstate.StateValidating) {
		failed, transitionErr := p.transition(
			cleanupCtx, request, runID, run,
			runstate.StateFailedRetryable, "interrupted-run-retryable",
		)
		if transitionErr != nil {
			return outcome, transitionErr
		}
		outcome.Run = failed
	}
	return outcome, fmt.Errorf("interrupted attempt %s is retryable", attempt.AttemptID)
}

func (p *Pipeline) recoverCancellation(
	ctx context.Context,
	request contracts.DeliveryRequest,
	runID identity.RunID,
	run contracts.Run,
	attempt persistence.Attempt,
	outcome Outcome,
) (Outcome, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	leaseSet, state, found, err := p.repository.GetLeaseSet(cleanupCtx, request.AttemptID)
	if err != nil {
		return outcome, fmt.Errorf("load cancelling attempt lease set: %w", err)
	}
	if found {
		quarantine, quarantineErr := p.worktrees.Quarantine(cleanupCtx, request)
		if errors.Is(quarantineErr, delivery.ErrWorktreeNotFound) &&
			attempt.StartedAt == nil && attempt.Status == "failed_retryable" {
			// Reconciliation may terminalize a queued attempt that never reached
			// StartAttempt. Only that durable timeline proves pre-worker absence.
			quarantineErr = nil
		}
		if quarantineErr != nil {
			return outcome, fmt.Errorf("quarantine cancelling attempt: %w", quarantineErr)
		}
		if quarantineErr == nil && quarantine.QuarantinePath != "" {
			outcome.Quarantine = &quarantine
		}
		if state == "active" {
			if err := p.repository.ReleaseLeaseSet(cleanupCtx, leaseSet); err != nil {
				return outcome, fmt.Errorf("release cancelling attempt leases: %w", err)
			}
		}
	}
	if err := p.completeNonSuccess(
		cleanupCtx, request, runID, run, "cancelled", &outcome,
	); err != nil {
		return outcome, err
	}
	return outcome, fmt.Errorf(
		"recovered cancellation for attempt %s with durable status %q",
		attempt.AttemptID, attempt.Status,
	)
}

func (p *Pipeline) recoverNonSuccess(
	ctx context.Context,
	request contracts.DeliveryRequest,
	runID identity.RunID,
	run contracts.Run,
	attempt persistence.Attempt,
	outcome Outcome,
) (Outcome, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	leaseSet, state, found, err := p.repository.GetLeaseSet(cleanupCtx, request.AttemptID)
	if err != nil {
		return outcome, fmt.Errorf("load terminal attempt lease set: %w", err)
	}
	if found {
		quarantine, quarantineErr := p.worktrees.Quarantine(cleanupCtx, request)
		if quarantineErr != nil {
			return outcome, fmt.Errorf("quarantine terminal attempt: %w", quarantineErr)
		}
		outcome.Quarantine = &quarantine
		if state == "active" {
			if err := p.repository.ReleaseLeaseSet(cleanupCtx, leaseSet); err != nil {
				return outcome, fmt.Errorf("release terminal attempt leases: %w", err)
			}
		}
	}
	if err := p.completeNonSuccess(
		cleanupCtx, request, runID, run, attempt.Status, &outcome,
	); err != nil {
		return outcome, err
	}
	return outcome, fmt.Errorf(
		"recovered terminal worker attempt %s with status %q",
		attempt.AttemptID, attempt.Status,
	)
}

func (p *Pipeline) replayCompleted(
	ctx context.Context,
	request contracts.DeliveryRequest,
	run contracts.Run,
	attempt persistence.Attempt,
	outcome Outcome,
) (Outcome, error) {
	record, found, err := p.repository.GetDeliveryPublication(
		ctx, request.DeliveryID, request.AttemptID,
	)
	if err != nil {
		return outcome, err
	}
	if !found || record.State != "published" {
		return outcome, fmt.Errorf("completed Run has no durable published receipt")
	}
	commit, err := p.commitFromPublication(record)
	if err != nil {
		return outcome, fmt.Errorf("decode completed delivery receipt: %w", err)
	}
	leaseSet, _, leaseFound, err := p.repository.GetLeaseSet(ctx, request.AttemptID)
	if err != nil {
		return outcome, fmt.Errorf("load completed delivery lease set: %w", err)
	}
	if !leaseFound {
		return outcome, fmt.Errorf("completed Run has no historical delivery lease proof")
	}
	replayCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	publication, publicationErr := p.publisher.Recover(
		replayCtx, request, commit, delivery.ValidationBundle{}, leaseSet,
	)
	if publication.Receipt.Status != "published" {
		if publicationErr == nil {
			publicationErr = fmt.Errorf("completed publication replay returned no durable receipt")
		}
		return outcome, publicationErr
	}
	if !publication.LeaseReleased && publicationErr == nil {
		publicationErr = fmt.Errorf("completed publication replay did not release its lease set")
	}
	outcome.Run, outcome.Attempt = run, attempt
	outcome.Commit = commit
	outcome.Publication = publication
	return outcome, publicationErr
}

func (p *Pipeline) commitFromPublication(
	record persistence.DeliveryPublication,
) (delivery.CommitResult, error) {
	if err := p.registry.ValidateJSON(
		"delivery-receipt.schema.json", record.Intent.ReceiptJSON,
	); err != nil {
		return delivery.CommitResult{}, fmt.Errorf("validate delivery receipt: %w", err)
	}
	var receipt contracts.DeliveryReceipt
	if err := json.Unmarshal(record.Intent.ReceiptJSON, &receipt); err != nil {
		return delivery.CommitResult{}, fmt.Errorf("decode delivery receipt: %w", err)
	}
	return delivery.CommitResult{
		BaseCommit: receipt.BaseCommit, ResultCommit: receipt.ResultCommit,
		ResultTree: receipt.ResultTree, PatchSHA256: receipt.PatchSHA256,
		ChangedPaths: append([]string(nil), receipt.ChangedPaths...),
	}, nil
}

func (p *Pipeline) rejectRecoveredValidation(
	ctx context.Context,
	request contracts.DeliveryRequest,
	runID identity.RunID,
	run contracts.Run,
	leaseSet persistence.LeaseSet,
	leaseState string,
	outcome Outcome,
) (Outcome, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	quarantine, err := p.worktrees.Quarantine(cleanupCtx, request)
	if err != nil {
		return outcome, fmt.Errorf("quarantine rejected recovery: %w", err)
	}
	outcome.Quarantine = &quarantine
	if leaseState == "active" {
		if err := p.repository.ReleaseLeaseSet(cleanupCtx, leaseSet); err != nil {
			return outcome, fmt.Errorf("release rejected recovery leases: %w", err)
		}
	}
	failed, err := p.transition(
		cleanupCtx,
		request,
		runID,
		run,
		runstate.StateFailedTerminal,
		"recovery-validation-rejected",
	)
	if err != nil {
		return outcome, err
	}
	outcome.Run = failed
	return outcome, nil
}

func attemptOwnedByFence(
	attempt persistence.Attempt,
	fence persistence.LeaseProof,
) bool {
	return attempt.LeaseResourceType == fence.ResourceType &&
		attempt.LeaseResourceID == fence.ResourceID &&
		attempt.WorkerID == fence.OwnerID &&
		attempt.FencingToken == fence.FencingToken
}
