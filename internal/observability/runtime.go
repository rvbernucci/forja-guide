package observability

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	envOTelEnabled     = "FORJA_OTEL_ENABLED"
	envTraceSampleRate = "FORJA_TRACE_SAMPLE_RATIO"
	envTraceParent     = "FORJA_TRACEPARENT"
)

// RuntimeConfig configures one process-wide telemetry runtime.
type RuntimeConfig struct {
	ServiceName string
	Version     string
	Environment string
	OTelEnabled bool
	SampleRatio float64
}

// ExtractTraceContextFromEnv continues a W3C trace across the one-shot worker
// process boundary without accepting arbitrary baggage or task content.
func ExtractTraceContextFromEnv(
	ctx context.Context,
	lookup func(string) (string, bool),
) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if lookup == nil {
		return ctx
	}
	value, ok := lookup(envTraceParent)
	value = strings.TrimSpace(value)
	if !ok || value == "" || len(value) > 128 {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(
		ctx,
		propagation.MapCarrier{"traceparent": value},
	)
}

// RuntimeConfigFromEnv resolves bounded Forja telemetry controls.
func RuntimeConfigFromEnv(
	serviceName string,
	version string,
	environment string,
	lookup func(string) (string, bool),
) (RuntimeConfig, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	config := RuntimeConfig{
		ServiceName: strings.TrimSpace(serviceName),
		Version:     strings.TrimSpace(version),
		Environment: strings.TrimSpace(environment),
		SampleRatio: 0.1,
	}
	if value, ok := lookup(envOTelEnabled); ok && strings.TrimSpace(value) != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return RuntimeConfig{}, fmt.Errorf("parse %s: %w", envOTelEnabled, err)
		}
		config.OTelEnabled = enabled
	}
	if value, ok := lookup(envTraceSampleRate); ok && strings.TrimSpace(value) != "" {
		ratio, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return RuntimeConfig{}, fmt.Errorf("parse %s: %w", envTraceSampleRate, err)
		}
		config.SampleRatio = ratio
	}
	if err := validateRuntimeConfig(config); err != nil {
		return RuntimeConfig{}, err
	}
	return config, nil
}

func validateRuntimeConfig(config RuntimeConfig) error {
	if strings.TrimSpace(config.ServiceName) == "" ||
		strings.TrimSpace(config.Environment) == "" ||
		math.IsNaN(config.SampleRatio) || math.IsInf(config.SampleRatio, 0) ||
		config.SampleRatio < 0 || config.SampleRatio > 1 {
		return fmt.Errorf("telemetry requires service, environment, and a sample ratio between zero and one")
	}
	return nil
}

// Runtime owns the process tracer provider and Prometheus registry.
type Runtime struct {
	Observer *Observer
	Registry *prometheus.Registry
	handler  http.Handler
	provider *sdktrace.TracerProvider
}

// NewRuntime creates a fail-soft telemetry plane. Exporter setup errors are
// logged and downgraded to local no-export tracing.
func NewRuntime(
	ctx context.Context,
	config RuntimeConfig,
	logger *slog.Logger,
) (*Runtime, error) {
	if err := validateRuntimeConfig(config); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	registry := prometheus.NewPedanticRegistry()
	metrics, err := NewMetrics(registry)
	if err != nil {
		return nil, fmt.Errorf("register Forja metrics: %w", err)
	}
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {
		metrics.telemetryFailed("traces")
		logger.Warn("OpenTelemetry asynchronous failure", "failure_class", FailureUnavailable)
	}))
	for _, collector := range []prometheus.Collector{
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	} {
		if err := registry.Register(collector); err != nil {
			return nil, fmt.Errorf("register runtime metrics: %w", err)
		}
	}

	options := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(config.ServiceName),
			semconv.ServiceVersion(config.Version),
			semconv.DeploymentEnvironmentNameKey.String(config.Environment),
		)),
	}
	if config.OTelEnabled {
		exporter, exporterErr := otlptracehttp.New(ctx)
		if exporterErr != nil {
			metrics.telemetryFailed("exporter")
			logger.Warn("OpenTelemetry exporter unavailable", "failure_class", FailureUnavailable)
			options = append(options, sdktrace.WithSampler(sdktrace.NeverSample()))
		} else {
			options = append(
				options,
				sdktrace.WithBatcher(exporter),
				sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(config.SampleRatio))),
			)
		}
	} else {
		options = append(options, sdktrace.WithSampler(sdktrace.NeverSample()))
	}
	provider := sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return &Runtime{
		Observer: NewObserver(provider, metrics),
		Registry: registry,
		handler:  Handler(registry),
		provider: provider,
	}, nil
}

// MetricsHandler returns the process-local Prometheus scrape endpoint.
func (runtime *Runtime) MetricsHandler() http.Handler {
	if runtime == nil || runtime.handler == nil {
		return http.NotFoundHandler()
	}
	return runtime.handler
}

// RegisterCollector extends the process registry with a bounded collector.
func (runtime *Runtime) RegisterCollector(collector prometheus.Collector) error {
	if runtime == nil || runtime.Registry == nil || collector == nil {
		return fmt.Errorf("telemetry runtime and collector are required")
	}
	if err := runtime.Registry.Register(collector); err != nil {
		return fmt.Errorf("register telemetry collector: %w", err)
	}
	return nil
}

// TracerProvider exposes the configured provider to instrumentation adapters.
func (runtime *Runtime) TracerProvider() trace.TracerProvider {
	if runtime == nil || runtime.provider == nil {
		return trace.NewNoopTracerProvider()
	}
	return runtime.provider
}

// Shutdown flushes telemetry without owning canonical service shutdown.
func (runtime *Runtime) Shutdown(ctx context.Context) error {
	if runtime == nil || runtime.provider == nil {
		return nil
	}
	return runtime.provider.Shutdown(ctx)
}
