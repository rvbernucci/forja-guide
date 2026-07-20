package alpha

import (
	"strings"
	"testing"
	"time"
)

func TestMagnificentSevenSECCompaniesAreDeterministicAndResolvable(t *testing.T) {
	companies := MagnificentSevenSECCompanies()
	if len(companies) != 7 {
		t.Fatalf("company count = %d, want 7", len(companies))
	}
	expected := map[string]string{
		"AAPL":  "0000320193",
		"MSFT":  "0000789019",
		"GOOGL": "0001652044",
		"AMZN":  "0001018724",
		"NVDA":  "0001045810",
		"META":  "0001326801",
		"TSLA":  "0001318605",
	}
	for _, company := range companies {
		if expected[company.Ticker] != company.CIK {
			t.Fatalf("%s CIK = %q", company.Ticker, company.CIK)
		}
		for _, lookup := range []string{company.Ticker, company.CIK, company.Name} {
			resolved, ok := ResolveSECCompany(lookup)
			if !ok || resolved.Ticker != company.Ticker {
				t.Fatalf("lookup %q resolved to %#v, ok=%v", lookup, resolved, ok)
			}
		}
	}
	if company, ok := ResolveSECCompany("google"); !ok || company.Ticker != "GOOGL" {
		t.Fatalf("google alias resolved to %#v, ok=%v", company, ok)
	}
	if _, ok := ResolveSECCompany("BRK.A"); ok {
		t.Fatal("out-of-universe company resolved")
	}
}

func TestSECIdentitySeedSQLIsBoundedAndIdempotent(t *testing.T) {
	var builder strings.Builder
	err := WriteSECIdentitySeedSQL(
		&builder,
		"10000000-0000-4000-8000-000000000001",
		"10000000-0000-4000-8000-000000000002",
	)
	if err != nil {
		t.Fatal(err)
	}
	sql := builder.String()
	required := []string{
		"BEGIN;",
		"COMMIT;",
		"INSERT INTO forja.tenants",
		"INSERT INTO forja.repositories",
		"INSERT INTO forja.alpha_source_systems",
		"INSERT INTO forja.alpha_issuers",
		"INSERT INTO forja.alpha_securities",
		"INSERT INTO forja.alpha_identifiers",
		"ON CONFLICT",
		"alpha_source_sec_edgar",
		"alpha_issuer_nvda",
		"0001045810",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("seed SQL missing %q", fragment)
		}
	}
	if strings.Count(sql, "INSERT INTO forja.alpha_issuers") != 7 {
		t.Fatalf("issuer insert count mismatch:\n%s", sql)
	}
	if strings.Count(sql, "INSERT INTO forja.alpha_identifiers") != 14 {
		t.Fatalf("identifier insert count mismatch")
	}
}

func TestParseSECCompanyTickersSnapshotValidatesMagnificentSeven(t *testing.T) {
	content := []byte(secCompanyTickersFixture())
	availableAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	snapshot, err := ParseSECCompanyTickersSnapshot(content, availableAt)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ContentSHA256 == "" {
		t.Fatal("snapshot content hash missing")
	}
	if snapshot.SizeBytes != len(content) {
		t.Fatalf("snapshot size = %d, want %d", snapshot.SizeBytes, len(content))
	}
	if !snapshot.AvailableAt.Equal(availableAt) {
		t.Fatalf("available_at = %s, want %s", snapshot.AvailableAt, availableAt)
	}
	if len(snapshot.Matches) != 7 {
		t.Fatalf("matches = %d, want 7", len(snapshot.Matches))
	}
	for _, company := range snapshot.Matches {
		if strings.TrimSpace(company.SourceName) == "" {
			t.Fatalf("%s source name missing", company.Ticker)
		}
		if _, ok := ResolveSECCompany(company.Ticker); !ok {
			t.Fatalf("%s no longer resolves", company.Ticker)
		}
	}
}

func TestParseSECCompanyTickersSnapshotRejectsMismatchedCIK(t *testing.T) {
	content := strings.Replace(secCompanyTickersFixture(), `"cik_str": 320193`, `"cik_str": 1`, 1)
	_, err := ParseSECCompanyTickersSnapshot([]byte(content), time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("mismatched CIK accepted")
	}
	if !strings.Contains(err.Error(), "CIK mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSECIdentitySeedSQLWithSnapshotRecordsLineage(t *testing.T) {
	snapshot, err := ParseSECCompanyTickersSnapshot(
		[]byte(secCompanyTickersFixture()),
		time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	var builder strings.Builder
	err = WriteSECIdentitySeedSQLWithSnapshot(
		&builder,
		"10000000-0000-4000-8000-000000000001",
		"10000000-0000-4000-8000-000000000002",
		&snapshot,
	)
	if err != nil {
		t.Fatal(err)
	}
	sql := builder.String()
	required := []string{
		"INSERT INTO forja.alpha_ingestion_runs",
		"INSERT INTO forja.alpha_source_objects",
		"alpha-sec-identity-snapshot-v1",
		"alpha/sec/company_tickers/" + snapshot.ContentSHA256 + ".json",
		"decode('" + snapshot.ContentSHA256 + "', 'hex')",
		"SEC company_tickers.json is a discovery snapshot",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("snapshot seed SQL missing %q", fragment)
		}
	}
	if strings.Count(sql, "INSERT INTO forja.alpha_issuers") != 7 {
		t.Fatalf("issuer insert count mismatch:\n%s", sql)
	}
	if strings.Count(sql, "INSERT INTO forja.alpha_source_objects") != 1 {
		t.Fatalf("source object insert count mismatch:\n%s", sql)
	}
}

func TestSECIdentitySeedSQLRejectsUnsafeScope(t *testing.T) {
	var builder strings.Builder
	if err := WriteSECIdentitySeedSQL(&builder, "not-a-uuid", "10000000-0000-4000-8000-000000000002"); err == nil {
		t.Fatal("invalid tenant ID accepted")
	}
	if err := WriteSECIdentitySeedSQL(&builder, "10000000-0000-4000-8000-000000000001", "not-a-uuid"); err == nil {
		t.Fatal("invalid repository ID accepted")
	}
}

func secCompanyTickersFixture() string {
	return `{
  "0": {"cik_str": 320193, "ticker": "AAPL", "title": "Apple Inc."},
  "1": {"cik_str": 789019, "ticker": "MSFT", "title": "MICROSOFT CORP"},
  "2": {"cik_str": 1652044, "ticker": "GOOGL", "title": "Alphabet Inc."},
  "3": {"cik_str": 1018724, "ticker": "AMZN", "title": "AMAZON COM INC"},
  "4": {"cik_str": 1045810, "ticker": "NVDA", "title": "NVIDIA CORP"},
  "5": {"cik_str": 1326801, "ticker": "META", "title": "Meta Platforms, Inc."},
  "6": {"cik_str": 1318605, "ticker": "TSLA", "title": "Tesla, Inc."},
  "7": {"cik_str": 1067983, "ticker": "BRK-B", "title": "Berkshire Hathaway Inc."}
}`
}
