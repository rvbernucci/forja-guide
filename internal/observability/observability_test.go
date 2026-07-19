package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/rvbernucci/forja-guide/internal/fault"
)

func TestClassifyUsesStableFailureTaxonomy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err  error
		want FailureClass
	}{
		{nil, FailureNone},
		{context.Canceled, FailureCancelled},
		{context.DeadlineExceeded, FailureDeadline},
		{fault.New(fault.CodeInvalidArgument, "test", "bad"), FailureInvalidArgument},
		{fault.New(fault.CodeUnauthenticated, "test", "bad"), FailureUnauthenticated},
		{fault.New(fault.CodePermissionDenied, "test", "bad"), FailurePermission},
		{fault.New(fault.CodeNotFound, "test", "bad"), FailureNotFound},
		{fault.New(fault.CodeConflict, "test", "bad"), FailureConflict},
		{fault.New(fault.CodeUnavailable, "test", "bad"), FailureUnavailable},
		{errors.New("untyped"), FailureInternal},
	}
	for _, test := range tests {
		if got := Classify(test.err); got != test.want {
			t.Fatalf("Classify(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}

func TestObserverBoundsLabelsAndNeverRecordsRawErrors(t *testing.T) {
	t.Parallel()
	registry := prometheus.NewPedanticRegistry()
	metrics, err := NewMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	observer := NewObserver(provider, metrics)
	ctx, operation := observer.Start(
		context.Background(), Boundary("tenant-secret"), Operation("prompt-secret"),
	)
	secret := "Bearer do-not-record"
	AddCorrelation(ctx, secret, nil)
	operation.End(errors.New(secret))

	want := `
# HELP forja_operations_total Completed Forja operations by bounded runtime boundary and outcome.
# TYPE forja_operations_total counter
forja_operations_total{boundary="other",failure_class="internal",operation="other",outcome="failed"} 1
`
	if err := testutil.GatherAndCompare(registry, strings.NewReader(want), "forja_operations_total"); err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	if strings.Contains(spans[0].Name, secret) || strings.Contains(spans[0].Status.Description, secret) {
		t.Fatal("secret appeared in span identity or status")
	}
	for _, item := range spans[0].Attributes {
		if strings.Contains(item.Value.String(), secret) {
			t.Fatal("secret appeared in span attributes")
		}
	}
}

func TestHTTPHandlerExportsBoundedMetrics(t *testing.T) {
	t.Parallel()
	registry := prometheus.NewPedanticRegistry()
	metrics, err := NewMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	observer := NewObserver(nil, metrics)
	handler := observer.HTTPHandler(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusConflict)
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/runs/id/transitions", nil)
	request.Pattern = "POST /v1/runs/{run_id}/transitions"
	handler.ServeHTTP(httptest.NewRecorder(), request)

	want := `
# HELP forja_operations_total Completed Forja operations by bounded runtime boundary and outcome.
# TYPE forja_operations_total counter
forja_operations_total{boundary="http",failure_class="conflict",operation="transition_run",outcome="failed"} 1
`
	if err := testutil.GatherAndCompare(registry, strings.NewReader(want), "forja_operations_total"); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPHandlerAcceptsOnlyBoundedTraceParent(t *testing.T) {
	previousPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	t.Cleanup(func() { otel.SetTextMapPropagator(previousPropagator) })

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	handler := NewObserver(provider, nil).HTTPHandler(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) {},
	))
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set(
		"traceparent",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	)
	request.Header.Set("tracestate", "vendor=private-customer-data")
	request.Header.Set("baggage", "private-content=must-not-propagate")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	wantTraceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatal(err)
	}
	if got := spans[0].SpanContext().TraceID(); got != wantTraceID {
		t.Fatalf("trace ID = %s, want %s", got, wantTraceID)
	}
	if got := spans[0].SpanContext().TraceState().String(); got != "" {
		t.Fatalf("caller-controlled tracestate propagated: %q", got)
	}
}

func TestHTTPHandlerRecordsAndRepanicsHandlerPanic(t *testing.T) {
	t.Parallel()
	registry := prometheus.NewPedanticRegistry()
	metrics, err := NewMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewObserver(nil, metrics).HTTPHandler(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) { panic("private panic content") },
	))
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("handler panic was swallowed")
			}
		}()
		handler.ServeHTTP(
			httptest.NewRecorder(),
			httptest.NewRequest(http.MethodGet, "/healthz", nil),
		)
	}()
	want := `
# HELP forja_operations_total Completed Forja operations by bounded runtime boundary and outcome.
# TYPE forja_operations_total counter
forja_operations_total{boundary="http",failure_class="internal",operation="health",outcome="failed"} 1
`
	if err := testutil.GatherAndCompare(registry, strings.NewReader(want), "forja_operations_total"); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeConfigRejectsInvalidSampling(t *testing.T) {
	t.Parallel()
	for _, ratio := range []string{"1.1", "NaN", "+Inf"} {
		_, err := RuntimeConfigFromEnv("forjad", "test", "test", func(key string) (string, bool) {
			if key == envTraceSampleRate {
				return ratio, true
			}
			return "", false
		})
		if err == nil {
			t.Fatalf("invalid sample ratio %q was accepted", ratio)
		}
	}
}

func TestNewRuntimeRejectsInvalidDirectConfig(t *testing.T) {
	t.Parallel()
	_, err := NewRuntime(context.Background(), RuntimeConfig{
		ServiceName: "forjad",
		Environment: "test",
		SampleRatio: 2,
	}, nil)
	if err == nil {
		t.Fatal("invalid direct runtime config was accepted")
	}
}

func TestRuntimePropagatesTraceContextWithoutBaggage(t *testing.T) {
	previousPropagator := otel.GetTextMapPropagator()
	previousProvider := otel.GetTracerProvider()
	t.Cleanup(func() {
		otel.SetTextMapPropagator(previousPropagator)
		otel.SetTracerProvider(previousProvider)
	})
	runtime, err := NewRuntime(context.Background(), RuntimeConfig{
		ServiceName: "forjad-test", Environment: "test", SampleRatio: 0.1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Shutdown(context.Background()) })
	ctx := otel.GetTextMapPropagator().Extract(context.Background(), propagation.MapCarrier{
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"baggage":     "private-content=must-not-propagate",
	})
	if !trace.SpanContextFromContext(ctx).IsValid() {
		t.Fatal("traceparent was not propagated")
	}
	if baggage.FromContext(ctx).Len() != 0 {
		t.Fatal("caller-controlled baggage entered the runtime context")
	}
}

func TestExtractTraceContextFromEnvAcceptsOnlyValidBoundedTraceParent(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
	ctx := ExtractTraceContextFromEnv(context.Background(), func(key string) (string, bool) {
		if key == envTraceParent {
			return "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", true
		}
		return "", false
	})
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() || !spanContext.IsRemote() || !spanContext.IsSampled() {
		t.Fatalf("traceparent was not extracted: %#v", spanContext)
	}
}

func TestOperationHandleEndsExactlyOnce(t *testing.T) {
	t.Parallel()
	registry := prometheus.NewPedanticRegistry()
	metrics, err := NewMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	_, operation := NewObserver(nil, metrics).Start(
		context.Background(), BoundaryWorker, OperationRunWorker,
	)
	operation.End(nil)
	operation.End(errors.New("second result must be ignored"))
	want := `
# HELP forja_operations_total Completed Forja operations by bounded runtime boundary and outcome.
# TYPE forja_operations_total counter
forja_operations_total{boundary="worker",failure_class="none",operation="run_worker",outcome="succeeded"} 1
`
	if err := testutil.GatherAndCompare(registry, strings.NewReader(want), "forja_operations_total"); err != nil {
		t.Fatal(err)
	}
}

func TestPGXTracerDoesNotRecordSQLOrArguments(t *testing.T) {
	t.Parallel()
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tracer := NewPGXTracer(NewObserver(provider, nil))
	secret := "Bearer database-secret"
	ctx := tracer.TraceQueryStart(
		context.Background(),
		nil,
		pgx.TraceQueryStartData{SQL: "SELECT '" + secret + "'", Args: []any{secret}},
	)
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	for _, item := range spans[0].Attributes {
		if strings.Contains(item.Value.String(), secret) {
			t.Fatal("database content appeared in trace attributes")
		}
	}
}
