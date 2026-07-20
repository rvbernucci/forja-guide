package alpha

import (
	"strings"
	"testing"
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

func TestSECIdentitySeedSQLRejectsUnsafeScope(t *testing.T) {
	var builder strings.Builder
	if err := WriteSECIdentitySeedSQL(&builder, "not-a-uuid", "10000000-0000-4000-8000-000000000002"); err == nil {
		t.Fatal("invalid tenant ID accepted")
	}
	if err := WriteSECIdentitySeedSQL(&builder, "10000000-0000-4000-8000-000000000001", "not-a-uuid"); err == nil {
		t.Fatal("invalid repository ID accepted")
	}
}
