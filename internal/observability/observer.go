package observability

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Observer joins traces and Prometheus metrics without becoming authority.
type Observer struct {
	tracer  trace.Tracer
	metrics *Metrics
	now     func() time.Time
}

// NewObserver creates an observer over explicit providers.
func NewObserver(provider trace.TracerProvider, metrics *Metrics) *Observer {
	if provider == nil {
		provider = noop.NewTracerProvider()
	}
	return &Observer{
		tracer:  provider.Tracer("github.com/rvbernucci/forja-guide"),
		metrics: metrics,
		now:     time.Now,
	}
}

// OperationHandle closes one traced and measured operation exactly once.
type OperationHandle struct {
	span      trace.Span
	metrics   *Metrics
	boundary  Boundary
	operation Operation
	started   time.Time
	once      sync.Once
}

// Start begins one bounded operation and returns a derived context.
func (o *Observer) Start(
	ctx context.Context,
	boundary Boundary,
	operation Operation,
) (context.Context, *OperationHandle) {
	if ctx == nil {
		ctx = context.Background()
	}
	boundary = normalizeBoundary(boundary)
	operation = normalizeOperation(operation)
	if o == nil {
		o = NewObserver(nil, nil)
	}
	started := o.now()
	o.metrics.started(boundary, operation)
	ctx, span := o.tracer.Start(
		ctx,
		fmt.Sprintf("forja.%s.%s", boundary, operation),
		trace.WithAttributes(
			attribute.String("forja.boundary", string(boundary)),
			attribute.String("forja.operation", string(operation)),
		),
	)
	return ctx, &OperationHandle{
		span: span, metrics: o.metrics, boundary: boundary,
		operation: operation, started: started,
	}
}

// End records only the stable failure class, never a raw error message.
func (h *OperationHandle) End(err error) {
	if h == nil {
		return
	}
	h.once.Do(func() {
		defer func() {
			if recover() != nil {
				h.metrics.telemetryFailed("other")
			}
		}()
		class := Classify(err)
		result := outcome(err)
		h.span.SetAttributes(
			attribute.String("forja.outcome", result),
			attribute.String("forja.failure_class", string(class)),
		)
		if err != nil {
			h.span.SetStatus(codes.Error, string(class))
		} else {
			h.span.SetStatus(codes.Ok, "")
		}
		h.span.End()
		h.metrics.finished(h.boundary, h.operation, h.started, err)
	})
}

// AddCorrelation adds irreversible correlation hashes to the current span.
func AddCorrelation(ctx context.Context, correlationID string, causationID *string) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.SetAttributes(attribute.String("forja.correlation_hash", hashIdentifier(correlationID)))
	if causationID != nil {
		span.SetAttributes(attribute.String("forja.causation_hash", hashIdentifier(*causationID)))
	}
}

func hashIdentifier(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest[:12])
}

// HTTPHandler extracts W3C context and records a bounded route operation.
func (o *Observer) HTTPHandler(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(
			request.Context(), propagation.HeaderCarrier(request.Header),
		)
		ctx, handle := o.Start(ctx, BoundaryHTTP, httpOperation(request))
		capture := &statusCapture{ResponseWriter: writer, status: http.StatusOK}
		defer func() {
			if recovered := recover(); recovered != nil {
				handle.End(NewFailure(FailureInternal))
				panic(recovered)
			}
			handle.End(httpStatusError(capture.status))
		}()
		next.ServeHTTP(capture, request.WithContext(ctx))
	})
}

type statusCapture struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (capture *statusCapture) WriteHeader(status int) {
	if capture.wroteHeader {
		return
	}
	capture.wroteHeader = true
	capture.status = status
	capture.ResponseWriter.WriteHeader(status)
}

func (capture *statusCapture) Write(data []byte) (int, error) {
	if !capture.wroteHeader {
		capture.WriteHeader(http.StatusOK)
	}
	return capture.ResponseWriter.Write(data)
}

// Unwrap lets http.ResponseController preserve optional writer capabilities.
func (capture *statusCapture) Unwrap() http.ResponseWriter {
	return capture.ResponseWriter
}

func httpOperation(request *http.Request) Operation {
	switch request.Method + " " + request.URL.Path {
	case "GET /healthz":
		return OperationHealth
	case "GET /readyz":
		return OperationReadiness
	case "GET /version":
		return OperationVersion
	case "GET /metrics":
		return OperationMetrics
	case "POST /v1/runs":
		return OperationCreateRun
	}
	path := strings.TrimPrefix(request.URL.Path, "/v1/runs/")
	if request.Method == http.MethodGet && path != request.URL.Path &&
		path != "" && !strings.Contains(path, "/") {
		return OperationGetRun
	}
	if request.Method == http.MethodPost && strings.HasSuffix(path, "/transitions") &&
		strings.Count(path, "/") == 1 {
		return OperationTransitionRun
	}
	return OperationOther
}

func httpStatusError(status int) error {
	if status < http.StatusBadRequest {
		return nil
	}
	switch status {
	case http.StatusUnauthorized:
		return statusFault{class: FailureUnauthenticated}
	case http.StatusForbidden:
		return statusFault{class: FailurePermission}
	case http.StatusNotFound:
		return statusFault{class: FailureNotFound}
	case http.StatusConflict:
		return statusFault{class: FailureConflict}
	case http.StatusServiceUnavailable:
		return statusFault{class: FailureUnavailable}
	default:
		if status < http.StatusInternalServerError {
			return statusFault{class: FailureInvalidArgument}
		}
		return statusFault{class: FailureInternal}
	}
}

type statusFault struct{ class FailureClass }

func (failure statusFault) Error() string { return string(failure.class) }

func (failure statusFault) failureClass() FailureClass { return failure.class }
