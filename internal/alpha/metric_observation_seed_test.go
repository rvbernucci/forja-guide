package alpha

import (
	"strings"
	"testing"
	"time"
)

func TestMetricObservationsFromCompanyFactsSelectsMappedNumericFacts(t *testing.T) {
	snapshot := mustCompanyFactsSnapshot(t)
	observations, err := MetricObservationsFromCompanyFacts(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 4 {
		t.Fatalf("observations = %d, want 4", len(observations))
	}
	for _, observation := range observations {
		if observation.ObservationID == "" || observation.MetricID == "" || observation.SourceFactID == "" {
			t.Fatalf("observation missing IDs: %#v", observation)
		}
		if observation.IssuerID != "alpha_issuer_nvda" {
			t.Fatalf("issuer = %s", observation.IssuerID)
		}
		if observation.ValueNumeric == "" || observation.Unit != "USD" || observation.Currency != "USD" {
			t.Fatalf("observation value/unit invalid: %#v", observation)
		}
		if observation.PeriodStart == "" || observation.PeriodEnd == "" {
			t.Fatalf("observation period invalid: %#v", observation)
		}
		if observation.Lineage["selection"] != "mapped_raw_fact_only_no_amendment_or_ytd_resolution" {
			t.Fatalf("lineage selection missing: %#v", observation.Lineage)
		}
	}
}

func TestMetricObservationsFromCompanyFactsSkipsQuarantinedFacts(t *testing.T) {
	company, ok := ResolveSECCompany("NVDA")
	if !ok {
		t.Fatal("NVDA fixture company not found")
	}
	snapshot, err := ParseSECCompanyFactsSnapshot(
		[]byte(secCompanyFactsWithUnsupportedFixture()),
		company,
		time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	observations, err := MetricObservationsFromCompanyFacts(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 1 {
		t.Fatalf("observations = %d, want 1", len(observations))
	}
	if observations[0].MetricID != "alpha_metric_revenue" {
		t.Fatalf("metric = %s, want alpha_metric_revenue", observations[0].MetricID)
	}
}

func TestWriteAlphaMetricObservationsSeedSQL(t *testing.T) {
	snapshot := mustCompanyFactsSnapshot(t)
	var builder strings.Builder
	err := WriteAlphaMetricObservationsSeedSQL(
		&builder,
		"10000000-0000-4000-8000-000000000001",
		"10000000-0000-4000-8000-000000000002",
		snapshot,
	)
	if err != nil {
		t.Fatal(err)
	}
	sql := builder.String()
	required := []string{
		"BEGIN;",
		"COMMIT;",
		"INSERT INTO forja.alpha_metric_observations",
		"alpha_metric_revenue",
		"alpha_metric_operating_income",
		"alpha_metric_net_income",
		"mapped_raw_fact_only_no_amendment_or_ytd_resolution",
		"2023-01-30",
		"period_start",
		"60922000000",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("metric observation SQL missing %q", fragment)
		}
	}
	if strings.Count(sql, "INSERT INTO forja.alpha_metric_observations") != 4 {
		t.Fatalf("observation insert count mismatch:\n%s", sql)
	}
}

func TestWriteAlphaMetricObservationsSeedSQLRejectsInvalidScope(t *testing.T) {
	snapshot := mustCompanyFactsSnapshot(t)
	var builder strings.Builder
	if err := WriteAlphaMetricObservationsSeedSQL(&builder, "not-a-uuid", "10000000-0000-4000-8000-000000000002", snapshot); err == nil {
		t.Fatal("invalid tenant accepted")
	}
	if err := WriteAlphaMetricObservationsSeedSQL(&builder, "10000000-0000-4000-8000-000000000001", "not-a-uuid", snapshot); err == nil {
		t.Fatal("invalid repository accepted")
	}
}

func mustCompanyFactsSnapshot(t *testing.T) SECCompanyFactsSnapshot {
	t.Helper()
	company, ok := ResolveSECCompany("NVDA")
	if !ok {
		t.Fatal("NVDA fixture company not found")
	}
	snapshot, err := ParseSECCompanyFactsSnapshot(
		[]byte(secCompanyFactsFixture()),
		company,
		time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
