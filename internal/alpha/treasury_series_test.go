package alpha

import (
	"strings"
	"testing"
	"time"
)

func TestParseTreasurySeriesCSVSnapshotPreservesTemporalClocks(t *testing.T) {
	snapshot, err := ParseTreasurySeriesCSVSnapshot(
		[]byte(treasurySeriesFixture()),
		DefaultTreasuryRealYield10YSeries(),
		time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ContentSHA256 == "" || snapshot.SizeBytes == 0 {
		t.Fatalf("snapshot hash/size missing: %#v", snapshot)
	}
	if len(snapshot.Observations) != 2 || snapshot.SkippedRows != 1 {
		t.Fatalf("observations=%d skipped=%d, want 2/1", len(snapshot.Observations), snapshot.SkippedRows)
	}
	for _, observation := range snapshot.Observations {
		if observation.ObservationID == "" || observation.ValueNumeric == "" {
			t.Fatalf("observation missing identity/value: %#v", observation)
		}
		if observation.ObservedAt.IsZero() || observation.PublishedAt.IsZero() || observation.VintageAt.IsZero() {
			t.Fatalf("observation missing temporal clocks: %#v", observation)
		}
	}
}

func TestWriteTreasurySeriesSeedSQL(t *testing.T) {
	snapshot, err := ParseTreasurySeriesCSVSnapshot(
		[]byte(treasurySeriesFixture()),
		DefaultTreasuryRealYield10YSeries(),
		time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	var builder strings.Builder
	err = WriteTreasurySeriesSeedSQL(
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
		"INSERT INTO forja.alpha_source_systems",
		"INSERT INTO forja.alpha_source_objects",
		"INSERT INTO forja.alpha_series",
		"INSERT INTO forja.alpha_series_observations",
		"alpha_source_treasury",
		"alpha_series_treasury.real-yield.10y",
		"treasury.real-yield.10y",
		"skipped_rows",
		"2026-07-20T12:00:00Z",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("Treasury SQL missing %q", fragment)
		}
	}
	if strings.Count(sql, "INSERT INTO forja.alpha_series_observations") != 2 {
		t.Fatalf("observation insert count mismatch:\n%s", sql)
	}
}

func TestParseTreasurySeriesCSVSnapshotRejectsMissingValueColumn(t *testing.T) {
	_, err := ParseTreasurySeriesCSVSnapshot(
		[]byte("date,foo\n2026-01-02,1.5\n"),
		DefaultTreasuryRealYield10YSeries(),
		time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	)
	if err == nil {
		t.Fatal("missing value column accepted")
	}
	if !strings.Contains(err.Error(), "missing value/rate column") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func treasurySeriesFixture() string {
	return `date,value,published_at,vintage_at
2026-01-02,1.73,2026-01-02,2026-01-02
2026-01-05,N/A,2026-01-05,2026-01-05
2026-01-06,1.81,2026-01-06,2026-01-06
`
}
