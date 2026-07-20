package alpha

import (
	"strings"
	"testing"
)

func TestAlphaMetricDefinitionsAreBounded(t *testing.T) {
	definitions := AlphaMetricDefinitions()
	if len(definitions) != 5 {
		t.Fatalf("metric definitions = %d, want 5", len(definitions))
	}
	expected := map[string]bool{
		"revenue":             true,
		"operating_income":    true,
		"net_income":          true,
		"operating_cash_flow": true,
		"capital_expenditure": true,
	}
	for _, definition := range definitions {
		if !expected[definition.MetricKey] {
			t.Fatalf("unexpected metric key %q", definition.MetricKey)
		}
		if definition.MetricKind != "reported" {
			t.Fatalf("%s kind = %s, want reported", definition.MetricKey, definition.MetricKind)
		}
		if definition.UnitPolicy["accepted_currency"] != "USD" {
			t.Fatalf("%s unit policy = %#v", definition.MetricKey, definition.UnitPolicy)
		}
	}
}

func TestReviewedUSGAAPMetricMappingsAreIssuerScoped(t *testing.T) {
	mappings := ReviewedUSGAAPMetricMappings("alpha_issuer_nvda")
	if len(mappings) != 6 {
		t.Fatalf("metric mappings = %d, want 6", len(mappings))
	}
	for _, mapping := range mappings {
		if mapping.IssuerID != "alpha_issuer_nvda" {
			t.Fatalf("mapping issuer = %s", mapping.IssuerID)
		}
		if mapping.ConfidenceClass != "reviewed" || mapping.ReviewedBy != metricRegistryVersion {
			t.Fatalf("mapping review fields invalid: %#v", mapping)
		}
		if !strings.HasPrefix(mapping.ConceptID, "alpha_concept_") {
			t.Fatalf("mapping concept ID = %s", mapping.ConceptID)
		}
	}
}

func TestWriteAlphaMetricRegistrySeedSQL(t *testing.T) {
	var builder strings.Builder
	err := WriteAlphaMetricRegistrySeedSQL(
		&builder,
		"10000000-0000-4000-8000-000000000001",
		"10000000-0000-4000-8000-000000000002",
		"alpha_issuer_nvda",
	)
	if err != nil {
		t.Fatal(err)
	}
	sql := builder.String()
	required := []string{
		"BEGIN;",
		"COMMIT;",
		"INSERT INTO forja.alpha_metric_definitions",
		"INSERT INTO forja.alpha_metric_mappings",
		"alpha_metric_revenue",
		"alpha_metric_operating_income",
		"RevenueFromContractWithCustomerExcludingAssessedTax",
		"alpha-metric-registry-v1",
		"reviewed_xbrl_mapping_required",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("metric registry SQL missing %q", fragment)
		}
	}
	if strings.Count(sql, "INSERT INTO forja.alpha_metric_definitions") != 5 {
		t.Fatalf("definition insert count mismatch:\n%s", sql)
	}
	if strings.Count(sql, "INSERT INTO forja.alpha_metric_mappings") != 6 {
		t.Fatalf("mapping insert count mismatch:\n%s", sql)
	}
}

func TestWriteAlphaMetricRegistrySeedSQLRejectsInvalidScope(t *testing.T) {
	var builder strings.Builder
	if err := WriteAlphaMetricRegistrySeedSQL(&builder, "not-a-uuid", "10000000-0000-4000-8000-000000000002", "alpha_issuer_nvda"); err == nil {
		t.Fatal("invalid tenant accepted")
	}
	if err := WriteAlphaMetricRegistrySeedSQL(&builder, "10000000-0000-4000-8000-000000000001", "not-a-uuid", "alpha_issuer_nvda"); err == nil {
		t.Fatal("invalid repository accepted")
	}
	if err := WriteAlphaMetricRegistrySeedSQL(&builder, "10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002", ""); err == nil {
		t.Fatal("empty issuer accepted")
	}
}
