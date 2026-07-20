package observability

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const maxMetricsRequestsInFlight = 1

// Metrics owns Forja's bounded-cardinality Prometheus instruments.
type Metrics struct {
	operations           *prometheus.CounterVec
	duration             *prometheus.HistogramVec
	inFlight             *prometheus.GaugeVec
	telemetryFailures    *prometheus.CounterVec
	indexEntities        *prometheus.CounterVec
	indexInvalidations   *prometheus.CounterVec
	retrievalCandidates  *prometheus.CounterVec
	retrievalResolutions *prometheus.CounterVec
	retrievalProjections *prometheus.CounterVec
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
		indexEntities: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "forja", Name: "index_entities_total",
			Help: "Canonical index entities published by bounded entity kind.",
		}, []string{"entity_kind"}),
		indexInvalidations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "forja", Name: "index_invalidations_total",
			Help: "Deterministic index invalidations by bounded reason.",
		}, []string{"reason"}),
		retrievalCandidates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "forja", Name: "retrieval_candidates_total",
			Help: "Governed retrieval candidates by bounded ranking stage.",
		}, []string{"stage"}),
		retrievalResolutions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "forja", Name: "retrieval_resolutions_total",
			Help: "Governed retrieval resolution outcomes.",
		}, []string{"outcome"}),
		retrievalProjections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "forja", Name: "retrieval_projection_deliveries_total",
			Help: "Governed retrieval projection delivery outcomes.",
		}, []string{"outcome"}),
	}
	for _, collector := range []prometheus.Collector{
		metrics.operations,
		metrics.duration,
		metrics.inFlight,
		metrics.telemetryFailures,
		metrics.indexEntities,
		metrics.indexInvalidations,
		metrics.retrievalCandidates,
		metrics.retrievalResolutions,
		metrics.retrievalProjections,
	} {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return metrics, nil
}

func (m *Metrics) retrieved(stats RetrievalStats) {
	if m == nil {
		return
	}
	for stage, count := range map[string]int{"dense": stats.DenseCandidates, "sparse": stats.SparseCandidates, "fused": stats.FusedCandidates} {
		if count > 0 {
			m.retrievalCandidates.WithLabelValues(stage).Add(float64(count))
		}
	}
	for outcome, count := range map[string]int{"accepted": stats.Accepted, "rejected": stats.Rejected} {
		if count > 0 {
			m.retrievalResolutions.WithLabelValues(outcome).Add(float64(count))
		}
	}
	if stats.Degraded {
		m.retrievalResolutions.WithLabelValues("degraded").Inc()
	}
	for outcome, count := range map[string]int{"claimed": stats.ProjectionClaimed, "published": stats.ProjectionPublished, "skipped": stats.ProjectionSkipped, "retried": stats.ProjectionRetried, "dead": stats.ProjectionDead} {
		if count > 0 {
			m.retrievalProjections.WithLabelValues(outcome).Add(float64(count))
		}
	}
}

func (m *Metrics) indexed(stats IndexStats) {
	if m == nil {
		return
	}
	for kind, count := range map[string]int{"file": stats.Files, "symbol": stats.Symbols, "relation": stats.Relations, "diagnostic": stats.Diagnostics, "reused": stats.Reused} {
		if count > 0 {
			m.indexEntities.WithLabelValues(kind).Add(float64(count))
		}
	}
	for reason, count := range stats.Invalidations {
		switch reason {
		case "source_changed", "dependency_changed", "adapter_changed", "configuration_changed", "deleted":
		default:
			reason = "other"
		}
		if count > 0 {
			m.indexInvalidations.WithLabelValues(reason).Add(float64(count))
		}
	}
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
		EnableOpenMetrics:   true,
		MaxRequestsInFlight: maxMetricsRequestsInFlight,
	})
}
