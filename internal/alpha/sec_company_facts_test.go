package alpha

import (
	"strings"
	"testing"
	"time"
)

func TestParseSECCompanyFactsSnapshotSummarizesCoverage(t *testing.T) {
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
	if snapshot.Company.Ticker != "NVDA" {
		t.Fatalf("ticker = %s, want NVDA", snapshot.Company.Ticker)
	}
	if snapshot.ContentSHA256 == "" || snapshot.SizeBytes == 0 {
		t.Fatalf("snapshot hash/size missing: %#v", snapshot)
	}
	coverage := snapshot.Coverage
	if coverage.EntityName != "NVIDIA CORP" {
		t.Fatalf("entity name = %q", coverage.EntityName)
	}
	if coverage.TaxonomyCount != 1 || coverage.ConceptCount != 3 || coverage.UnitCount != 3 || coverage.FactCount != 4 {
		t.Fatalf("unexpected coverage: %#v", coverage)
	}
	if len(snapshot.RawFacts) != 4 {
		t.Fatalf("raw facts = %d, want 4", len(snapshot.RawFacts))
	}
	for _, fact := range snapshot.RawFacts {
		if fact.FactID == "" || fact.ConceptID == "" || fact.ContextID == "" || fact.FilingID == "" {
			t.Fatalf("raw fact missing deterministic IDs: %#v", fact)
		}
		if fact.LexicalValue == "" {
			t.Fatalf("raw fact missing lexical value: %#v", fact)
		}
	}
	if strings.Join(coverage.Forms, ",") != "10-K,10-Q" {
		t.Fatalf("forms = %#v", coverage.Forms)
	}
	if strings.Join(coverage.Currencies, ",") != "USD" {
		t.Fatalf("currencies = %#v", coverage.Currencies)
	}
	if strings.Join(coverage.CanonicalHints, ",") != "net_income,operating_income,revenue" {
		t.Fatalf("canonical hints = %#v", coverage.CanonicalHints)
	}
}

func TestParseSECCompanyFactsSnapshotRejectsWrongCIK(t *testing.T) {
	company, ok := ResolveSECCompany("MSFT")
	if !ok {
		t.Fatal("MSFT fixture company not found")
	}
	_, err := ParseSECCompanyFactsSnapshot(
		[]byte(secCompanyFactsFixture()),
		company,
		time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	)
	if err == nil {
		t.Fatal("wrong CIK accepted")
	}
	if !strings.Contains(err.Error(), "CIK mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWriteSECCompanyFactsSeedSQLRecordsSourceObjectAndCoverage(t *testing.T) {
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
	var builder strings.Builder
	err = WriteSECCompanyFactsSeedSQL(
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
		"INSERT INTO forja.alpha_ingestion_runs",
		"INSERT INTO forja.alpha_source_objects",
		"INSERT INTO forja.alpha_taxonomies",
		"INSERT INTO forja.alpha_xbrl_concepts",
		"INSERT INTO forja.alpha_xbrl_contexts",
		"INSERT INTO forja.alpha_xbrl_facts",
		"alpha-sec-company-facts-snapshot-v1",
		"alpha/sec/companyfacts/CIK0001045810/" + snapshot.ContentSHA256 + ".json",
		"SEC Company Facts is a structured fact snapshot",
		"canonical_hints",
		"60922000000",
		"alpha_filing_",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("company facts SQL missing %q", fragment)
		}
	}
	if strings.Count(sql, "INSERT INTO forja.alpha_source_objects") != 1 {
		t.Fatalf("source object insert count mismatch:\n%s", sql)
	}
	if strings.Count(sql, "INSERT INTO forja.alpha_xbrl_facts") != 4 {
		t.Fatalf("raw fact insert count mismatch:\n%s", sql)
	}
}

func secCompanyFactsFixture() string {
	return `{
  "cik": 1045810,
  "entityName": "NVIDIA CORP",
  "facts": {
    "us-gaap": {
      "Revenues": {
        "label": "Revenues",
        "description": "Revenue from contracts with customers",
        "units": {
          "USD": [
            {"end": "2024-01-28", "val": 60922000000, "accn": "0001045810-24-000029", "fy": 2024, "fp": "FY", "form": "10-K", "filed": "2024-02-21", "frame": "CY2023"},
            {"end": "2024-07-28", "val": 30040000000, "accn": "0001045810-24-000227", "fy": 2025, "fp": "Q2", "form": "10-Q", "filed": "2024-08-28", "frame": "CY2024Q2"}
          ]
        }
      },
      "OperatingIncomeLoss": {
        "label": "Operating Income (Loss)",
        "description": "Operating income or loss",
        "units": {
          "USD": [
            {"end": "2024-01-28", "val": 32972000000, "accn": "0001045810-24-000029", "fy": 2024, "fp": "FY", "form": "10-K", "filed": "2024-02-21", "frame": "CY2023"}
          ]
        }
      },
      "NetIncomeLoss": {
        "label": "Net Income (Loss)",
        "description": "Net income or loss",
        "units": {
          "USD": [
            {"end": "2024-01-28", "val": 29760000000, "accn": "0001045810-24-000029", "fy": 2024, "fp": "FY", "form": "10-K", "filed": "2024-02-21", "frame": "CY2023"}
          ]
        }
      }
    }
  }
}`
}
