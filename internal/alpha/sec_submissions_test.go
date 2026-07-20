package alpha

import (
	"strings"
	"testing"
	"time"
)

func TestParseSECSubmissionsSnapshotExtractsAlphaFilings(t *testing.T) {
	company, ok := ResolveSECCompany("NVDA")
	if !ok {
		t.Fatal("NVDA fixture company not found")
	}
	availableAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	snapshot, err := ParseSECSubmissionsSnapshot([]byte(secSubmissionsFixture()), company, availableAt)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Company.Ticker != "NVDA" {
		t.Fatalf("ticker = %s, want NVDA", snapshot.Company.Ticker)
	}
	if snapshot.ContentSHA256 == "" || snapshot.SizeBytes == 0 {
		t.Fatalf("snapshot hash/size missing: %#v", snapshot)
	}
	if len(snapshot.Filings) != 3 {
		t.Fatalf("filings = %d, want 3", len(snapshot.Filings))
	}
	if snapshot.Filings[0].FormType != "10-K" {
		t.Fatalf("first form = %s, want 10-K", snapshot.Filings[0].FormType)
	}
	for _, filing := range snapshot.Filings {
		if filing.SourceObjectID != snapshot.FilingsSourceObjectID() {
			t.Fatalf("source object = %s, want %s", filing.SourceObjectID, snapshot.FilingsSourceObjectID())
		}
		if filing.AvailableAt.Before(filing.FiledAt) {
			t.Fatalf("%s available before filed", filing.Accession)
		}
	}
}

func TestParseSECSubmissionsSnapshotRejectsWrongCompany(t *testing.T) {
	company, ok := ResolveSECCompany("MSFT")
	if !ok {
		t.Fatal("MSFT fixture company not found")
	}
	_, err := ParseSECSubmissionsSnapshot([]byte(secSubmissionsFixture()), company, time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("wrong company accepted")
	}
	if !strings.Contains(err.Error(), "CIK mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWriteSECSubmissionsSeedSQLRecordsFilingsAndLineage(t *testing.T) {
	company, ok := ResolveSECCompany("NVDA")
	if !ok {
		t.Fatal("NVDA fixture company not found")
	}
	snapshot, err := ParseSECSubmissionsSnapshot([]byte(secSubmissionsFixture()), company, time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	var builder strings.Builder
	err = WriteSECSubmissionsSeedSQL(
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
		"INSERT INTO forja.alpha_filings",
		"alpha-sec-submissions-snapshot-v1",
		"alpha/sec/submissions/CIK0001045810/" + snapshot.ContentSHA256 + ".json",
		"0001045810-24-000029",
		"10-K",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("submissions SQL missing %q", fragment)
		}
	}
	if strings.Count(sql, "INSERT INTO forja.alpha_filings") != 3 {
		t.Fatalf("filing insert count mismatch:\n%s", sql)
	}
}

func secSubmissionsFixture() string {
	return `{
  "cik": "0001045810",
  "name": "NVIDIA CORP",
  "tickers": ["NVDA"],
  "exchanges": ["Nasdaq"],
  "filings": {
    "recent": {
      "accessionNumber": [
        "0001045810-24-000029",
        "0001045810-24-000227",
        "0001045810-23-000017",
        "0001045810-24-000013"
      ],
      "filingDate": [
        "2024-02-21",
        "2024-08-28",
        "2023-02-24",
        "2024-01-10"
      ],
      "reportDate": [
        "2024-01-28",
        "2024-07-28",
        "2023-01-29",
        "2024-01-10"
      ],
      "acceptanceDateTime": [
        "2024-02-21T16:36:57.000Z",
        "2024-08-28T16:21:08.000Z",
        "20230224173001",
        "2024-01-10T08:00:00.000Z"
      ],
      "form": [
        "10-K",
        "10-Q",
        "10-K/A",
        "8-K"
      ],
      "primaryDocument": [
        "nvda-20240128.htm",
        "nvda-20240728.htm",
        "nvda-20230129x10ka.htm",
        "nvda-20240110.htm"
      ],
      "primaryDocDescription": [
        "10-K",
        "10-Q",
        "10-K/A",
        "8-K"
      ]
    }
  }
}`
}
