package observability

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fixedOperationalReader struct {
	snapshot OperationalSnapshot
	err      error
}

func (reader fixedOperationalReader) OperationalSnapshot(
	context.Context,
	OperationalThresholds,
) (OperationalSnapshot, error) {
	return reader.snapshot, reader.err
}

func TestOperationalCollectorExportsOnlyClosedConditions(t *testing.T) {
	t.Parallel()
	collector, err := NewOperationalCollector(fixedOperationalReader{
		snapshot: OperationalSnapshot{
			StuckRuns: 2, ExpiredLeases: 3, PendingOutbox: 5,
			InflightOutbox: 7, DeadOutbox: 11, ProjectionLag: 13,
			RetrievalGenerationsBuild: 43, RetrievalGenerationsLive: 47,
			RetrievalGenerationsDrain: 53, RetrievalGenerationsFail: 59,
			PendingApprovals: 17, WorkerCrashLoops: 19,
			ArtifactReconciliation: 23, ArtifactIntegrityFailures: 29,
			TombstonedArtifactObjects: 31, ProposedMemoryCandidates: 37,
			ActiveMemories: 41,
		},
	}, DefaultOperationalThresholds())
	if err != nil {
		t.Fatal(err)
	}
	registry := prometheus.NewPedanticRegistry()
	if err := registry.Register(collector); err != nil {
		t.Fatal(err)
	}
	want := `
# HELP forja_operational_collection_success Whether the most recent operational state collection succeeded.
# TYPE forja_operational_collection_success gauge
forja_operational_collection_success 1
# HELP forja_operational_condition_items Current content-free item count for each bounded operational condition.
# TYPE forja_operational_condition_items gauge
forja_operational_condition_items{condition="dead_outbox"} 11
forja_operational_condition_items{condition="active_memories"} 41
forja_operational_condition_items{condition="artifact_integrity_failures"} 29
forja_operational_condition_items{condition="artifact_reconciliation"} 23
forja_operational_condition_items{condition="expired_leases"} 3
forja_operational_condition_items{condition="inflight_outbox"} 7
forja_operational_condition_items{condition="pending_approvals"} 17
forja_operational_condition_items{condition="pending_outbox"} 5
forja_operational_condition_items{condition="projection_lag"} 13
forja_operational_condition_items{condition="proposed_memory_candidates"} 37
forja_operational_condition_items{condition="retrieval_generations_active"} 47
forja_operational_condition_items{condition="retrieval_generations_building"} 43
forja_operational_condition_items{condition="retrieval_generations_draining"} 53
forja_operational_condition_items{condition="retrieval_generations_failed"} 59
forja_operational_condition_items{condition="stuck_runs"} 2
forja_operational_condition_items{condition="tombstoned_artifact_objects"} 31
forja_operational_condition_items{condition="worker_crash_loops"} 19
`
	if err := testutil.GatherAndCompare(
		registry,
		strings.NewReader(want),
		"forja_operational_collection_success",
		"forja_operational_condition_items",
	); err != nil {
		t.Fatal(err)
	}
}

func TestOperationalCollectorFailsSoftWithoutPublishingStaleState(t *testing.T) {
	t.Parallel()
	collector, err := NewOperationalCollector(
		fixedOperationalReader{err: errors.New("database-secret")},
		DefaultOperationalThresholds(),
	)
	if err != nil {
		t.Fatal(err)
	}
	registry := prometheus.NewPedanticRegistry()
	if err := registry.Register(collector); err != nil {
		t.Fatal(err)
	}
	want := `
# HELP forja_operational_collection_success Whether the most recent operational state collection succeeded.
# TYPE forja_operational_collection_success gauge
forja_operational_collection_success 0
`
	if err := testutil.GatherAndCompare(
		registry,
		strings.NewReader(want),
		"forja_operational_collection_success",
	); err != nil {
		t.Fatal(err)
	}
	metricFamilies, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range metricFamilies {
		if strings.Contains(family.String(), "database-secret") {
			t.Fatal("raw collector failure leaked into metrics")
		}
	}
}

func TestOperationalThresholdsAreBounded(t *testing.T) {
	t.Parallel()
	thresholds := DefaultOperationalThresholds()
	thresholds.CrashLoopCount = 1
	if _, err := NewOperationalCollector(fixedOperationalReader{}, thresholds); err == nil {
		t.Fatal("unsafe crash-loop threshold was accepted")
	}
}
