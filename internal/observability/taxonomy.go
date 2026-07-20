// Package observability provides bounded traces, metrics, and log correlation.
package observability

import (
	"context"
	"errors"

	"github.com/rvbernucci/forja-guide/internal/fault"
)

// Boundary is a stable, low-cardinality runtime boundary.
type Boundary string

const (
	BoundaryMCP         Boundary = "mcp"
	BoundaryHTTP        Boundary = "http"
	BoundaryScheduler   Boundary = "scheduler"
	BoundaryWorker      Boundary = "worker"
	BoundaryValidation  Boundary = "validation"
	BoundaryPersistence Boundary = "persistence"
	BoundaryDelivery    Boundary = "delivery"
	BoundaryIndexing    Boundary = "indexing"
	BoundaryRetrieval   Boundary = "retrieval"
	BoundaryTelemetry   Boundary = "telemetry"
	BoundaryOther       Boundary = "other"
)

// Operation is a stable, low-cardinality operation name.
type Operation string

const (
	OperationPlanSprint       Operation = "plan_sprint"
	OperationSubmitSprint     Operation = "submit_sprint"
	OperationGetSprint        Operation = "get_sprint"
	OperationGetRun           Operation = "get_run"
	OperationApproveDecision  Operation = "approve_decision"
	OperationRejectDecision   Operation = "reject_decision"
	OperationCancelRun        Operation = "cancel_run"
	OperationResumeRun        Operation = "resume_run"
	OperationCreateRun        Operation = "create_run"
	OperationTransitionRun    Operation = "transition_run"
	OperationHealth           Operation = "health"
	OperationReadiness        Operation = "readiness"
	OperationVersion          Operation = "version"
	OperationMetrics          Operation = "metrics"
	OperationExecuteDelivery  Operation = "execute_delivery"
	OperationRecoverDelivery  Operation = "recover_delivery"
	OperationDispatchWorker   Operation = "dispatch_worker"
	OperationRunWorker        Operation = "run_worker"
	OperationValidateChange   Operation = "validate_change"
	OperationPublishChange    Operation = "publish_change"
	OperationPublishIndex     Operation = "publish_index"
	OperationQuery            Operation = "query"
	OperationProjectRetrieval Operation = "project_retrieval"
	OperationOther            Operation = "other"
)

var validBoundaries = map[Boundary]struct{}{
	BoundaryMCP: {}, BoundaryHTTP: {}, BoundaryScheduler: {}, BoundaryWorker: {},
	BoundaryValidation: {}, BoundaryPersistence: {}, BoundaryDelivery: {},
	BoundaryIndexing: {}, BoundaryRetrieval: {}, BoundaryTelemetry: {}, BoundaryOther: {},
}

var validOperations = map[Operation]struct{}{
	OperationPlanSprint: {}, OperationSubmitSprint: {}, OperationGetSprint: {},
	OperationGetRun: {}, OperationApproveDecision: {}, OperationRejectDecision: {},
	OperationCancelRun: {}, OperationResumeRun: {}, OperationCreateRun: {},
	OperationTransitionRun: {}, OperationHealth: {}, OperationReadiness: {},
	OperationVersion: {}, OperationMetrics: {}, OperationExecuteDelivery: {},
	OperationRecoverDelivery: {}, OperationDispatchWorker: {}, OperationRunWorker: {},
	OperationValidateChange: {},
	OperationPublishChange:  {}, OperationQuery: {}, OperationOther: {},
	OperationPublishIndex: {}, OperationProjectRetrieval: {},
}

// FailureClass is the stable failure taxonomy shared by traces and metrics.
type FailureClass string

const (
	FailureNone            FailureClass = "none"
	FailureCancelled       FailureClass = "cancelled"
	FailureDeadline        FailureClass = "deadline"
	FailureInvalidArgument FailureClass = "invalid_argument"
	FailureUnauthenticated FailureClass = "unauthenticated"
	FailurePermission      FailureClass = "permission_denied"
	FailureNotFound        FailureClass = "not_found"
	FailureConflict        FailureClass = "conflict"
	FailureUnavailable     FailureClass = "unavailable"
	FailureResourceLimit   FailureClass = "resource_limit"
	FailureWorker          FailureClass = "worker_failed"
	FailureValidation      FailureClass = "validation_failed"
	FailureInternal        FailureClass = "internal"
)

type classifiedError struct{ class FailureClass }

func (failure classifiedError) Error() string { return string(failure.class) }

func (failure classifiedError) failureClass() FailureClass { return failure.class }

// NewFailure creates a content-free error for an already classified outcome.
func NewFailure(class FailureClass) error {
	if class == FailureNone {
		return nil
	}
	return classifiedError{class: class}
}

func normalizeBoundary(boundary Boundary) Boundary {
	if _, ok := validBoundaries[boundary]; ok {
		return boundary
	}
	return BoundaryOther
}

func normalizeOperation(operation Operation) Operation {
	if _, ok := validOperations[operation]; ok {
		return operation
	}
	return OperationOther
}

// Classify maps an error chain to one bounded operational failure class.
func Classify(err error) FailureClass {
	if err == nil {
		return FailureNone
	}
	if errors.Is(err, context.Canceled) {
		return FailureCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return FailureDeadline
	}
	var classified interface{ failureClass() FailureClass }
	if errors.As(err, &classified) {
		return classified.failureClass()
	}
	switch fault.CodeOf(err) {
	case fault.CodeInvalidArgument:
		return FailureInvalidArgument
	case fault.CodeUnauthenticated:
		return FailureUnauthenticated
	case fault.CodePermissionDenied:
		return FailurePermission
	case fault.CodeNotFound:
		return FailureNotFound
	case fault.CodeConflict:
		return FailureConflict
	case fault.CodeUnavailable:
		return FailureUnavailable
	default:
		return FailureInternal
	}
}

func outcome(err error) string {
	switch Classify(err) {
	case FailureNone:
		return "succeeded"
	case FailureCancelled:
		return "cancelled"
	default:
		return "failed"
	}
}
