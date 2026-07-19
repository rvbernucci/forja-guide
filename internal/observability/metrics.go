package observability

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics owns Forja's bounded-cardinality Prometheus instruments.
type Metrics struct {
	operations        *prometheus.CounterVec
	duration          *prometheus.HistogramVec
	inFlight          *prometheus.GaugeVec
	telemetryFailures *prometheus.CounterVec
}

// NewMetrics registers a fresh metric set with an explicit registry.
func NewMetrics(registerer prometheus.Registerer) (*Metrics, error) {
	metrics := &Metrics{
		operations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "forja",
			Name:      "operations_total",
			Help:      "Completed Forja operations by bounded runtime boundary and outcome.",
		}, []string{"boundary", "operation", "outcome", "failure_class"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "forja",
			Name:      "operation_duration_seconds",
			Help:      "Forja operation duration by bounded runtime boundary and outcome.",
			Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 15, 30, 60, 300},
		}, []string{"boundary", "operation", "outcome"}),
		inFlight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "forja",
			Name:      "operations_in_flight",
			Help:      "Currently active Forja operations by bounded runtime boundary.",
		}, []string{"boundary", "operation"}),
		telemetryFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "forja",
			Name:      "telemetry_failures_total",
			Help:      "Failures isolated inside the non-authoritative telemetry plane.",
		}, []string{"signal"}),
	}
	for _, collector := range []prometheus.Collector{
		metrics.operations,
		metrics.duration,
		metrics.inFlight,
		metrics.telemetryFailures,
	} {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return metrics, nil
}

func (m *Metrics) started(boundary Boundary, operation Operation) {
	if m == nil {
		return
	}
	m.inFlight.WithLabelValues(string(boundary), string(operation)).Inc()
}

func (m *Metrics) finished(
	boundary Boundary,
	operation Operation,
	started time.Time,
	err error,
) {
	if m == nil {
		return
	}
	result := outcome(err)
	m.inFlight.WithLabelValues(string(boundary), string(operation)).Dec()
	m.operations.WithLabelValues(
		string(boundary), string(operation), result, string(Classify(err)),
	).Inc()
	m.duration.WithLabelValues(
		string(boundary), string(operation), result,
	).Observe(time.Since(started).Seconds())
}

func (m *Metrics) telemetryFailed(signal string) {
	if m == nil {
		return
	}
	switch signal {
	case "traces", "metrics", "logs", "exporter":
	default:
		signal = "other"
	}
	m.telemetryFailures.WithLabelValues(signal).Inc()
}

// Handler returns an OpenMetrics-compatible scrape handler.
func Handler(gatherer prometheus.Gatherer) http.Handler {
	return promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}
