package alpha

import (
	"strings"
	"testing"
	"time"
)

func TestParseFREDSeriesCSVSnapshotPreservesVintageClocks(t *testing.T) {
	snapshot, err := ParseFREDSeriesCSVSnapshot(
		[]byte(fredSeriesFixture()),
		DefaultFREDFedFundsSeries(),
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

func TestWriteFREDSeriesSeedSQL(t *testing.T) {
	snapshot, err := ParseFREDSeriesCSVSnapshot(
		[]byte(fredSeriesFixture()),
		DefaultFREDFedFundsSeries(),
		time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	var builder strings.Builder
	err = WriteFREDSeriesSeedSQL(
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
		"alpha_source_fred",
		"alpha_series_fred.fedfunds",
		"FRED:FEDFUNDS",
		"skipped_rows",
		"realtime_start",
		"2026-07-20T12:00:00Z",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("FRED SQL missing %q", fragment)
		}
	}
	if strings.Count(sql, "INSERT INTO forja.alpha_series_observations") != 2 {
		t.Fatalf("observation insert count mismatch:\n%s", sql)
	}
}

func TestParseFREDSeriesCSVSnapshotRejectsMissingValueColumn(t *testing.T) {
	_, err := ParseFREDSeriesCSVSnapshot(
		[]byte("date,foo\n2026-01-02,1.5\n"),
		DefaultFREDFedFundsSeries(),
		time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	)
	if err == nil {
		t.Fatal("missing value column accepted")
	}
	if !strings.Contains(err.Error(), "missing value column") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func fredSeriesFixture() string {
	return `realtime_start,realtime_end,date,value
2026-01-01,2026-02-01,2025-12-01,4.33
2026-02-01,2026-03-01,2026-01-01,.
2026-03-01,2026-04-01,2026-02-01,4.21
`
}
