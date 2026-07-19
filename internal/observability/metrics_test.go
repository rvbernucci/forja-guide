package observability

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

type blockingCollector struct {
	description *prometheus.Desc
	started     chan struct{}
	release     chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
	calls       atomic.Int32
}

func newBlockingCollector() *blockingCollector {
	return &blockingCollector{
		description: prometheus.NewDesc("forja_test_blocking", "test", nil, nil),
		started:     make(chan struct{}),
		release:     make(chan struct{}),
	}
}

func (collector *blockingCollector) Describe(channel chan<- *prometheus.Desc) {
	channel <- collector.description
}

func (collector *blockingCollector) Collect(channel chan<- prometheus.Metric) {
	collector.calls.Add(1)
	collector.startOnce.Do(func() { close(collector.started) })
	<-collector.release
	channel <- prometheus.MustNewConstMetric(collector.description, prometheus.GaugeValue, 1)
}

func (collector *blockingCollector) unblock() {
	collector.releaseOnce.Do(func() { close(collector.release) })
}

func TestMetricsHandlerRejectsConcurrentGatherWithoutStartingAnotherCollector(t *testing.T) {
	t.Parallel()
	registry := prometheus.NewPedanticRegistry()
	collector := newBlockingCollector()
	t.Cleanup(collector.unblock)
	if err := registry.Register(collector); err != nil {
		t.Fatal(err)
	}
	handler := Handler(registry)
	firstStatus := make(chan int, 1)
	go func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(
			recorder,
			httptest.NewRequest(http.MethodGet, "/metrics", nil),
		)
		firstStatus <- recorder.Code
	}()
	<-collector.started

	second := httptest.NewRecorder()
	handler.ServeHTTP(
		second,
		httptest.NewRequest(http.MethodGet, "/metrics", nil),
	)
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("concurrent scrape status = %d, want 503", second.Code)
	}
	if calls := collector.calls.Load(); calls != 1 {
		t.Fatalf("collector calls = %d, want one in-flight gather", calls)
	}

	collector.unblock()
	if status := <-firstStatus; status != http.StatusOK {
		t.Fatalf("first scrape status = %d, want 200", status)
	}
}
