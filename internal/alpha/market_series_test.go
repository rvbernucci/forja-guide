package alpha

import (
	"strings"
	"testing"
	"time"
)

func TestParseMarketSeriesCSVSnapshotDerivesDailyReturns(t *testing.T) {
	company, ok := ResolveSECCompany("NVDA")
	if !ok {
		t.Fatal("NVDA no longer resolves")
	}
	snapshot, err := ParseMarketSeriesCSVSnapshot(
		[]byte(marketSeriesFixture()),
		company,
		"licensed-csv",
		time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.PriceObservations) != 3 || len(snapshot.ReturnObservations) != 2 {
		t.Fatalf("prices=%d returns=%d, want 3/2", len(snapshot.PriceObservations), len(snapshot.ReturnObservations))
	}
	if snapshot.SkippedRows != 1 {
		t.Fatalf("skipped=%d, want 1", snapshot.SkippedRows)
	}
	if snapshot.PriceSeries.SeriesID != "alpha_series_market.nvda.adjusted_close" {
		t.Fatalf("price series = %s", snapshot.PriceSeries.SeriesID)
	}
	if snapshot.ReturnSeries.SeriesID != "alpha_series_market.nvda.daily_return" {
		t.Fatalf("return series = %s", snapshot.ReturnSeries.SeriesID)
	}
	if snapshot.ReturnObservations[0].ValueNumeric == "" {
		t.Fatalf("derived return missing value: %#v", snapshot.ReturnObservations[0])
	}
}

func TestWriteMarketSeriesSeedSQL(t *testing.T) {
	company, ok := ResolveSECCompany("NVDA")
	if !ok {
		t.Fatal("NVDA no longer resolves")
	}
	snapshot, err := ParseMarketSeriesCSVSnapshot(
		[]byte(marketSeriesFixture()),
		company,
		"licensed-csv",
		time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	var builder strings.Builder
	err = WriteMarketSeriesSeedSQL(
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
		"alpha_source_market_csv",
		"alpha_series_market.nvda.adjusted_close",
		"alpha_series_market.nvda.daily_return",
		"derived_return_rows",
		"corporate_action_note",
		"provider-or-user-license-required",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("market SQL missing %q", fragment)
		}
	}
	if strings.Count(sql, "INSERT INTO forja.alpha_series_observations") != 5 {
		t.Fatalf("observation insert count mismatch:\n%s", sql)
	}
}

func TestParseMarketSeriesCSVSnapshotRejectsMissingAdjustedClose(t *testing.T) {
	company, ok := ResolveSECCompany("NVDA")
	if !ok {
		t.Fatal("NVDA no longer resolves")
	}
	_, err := ParseMarketSeriesCSVSnapshot(
		[]byte("date,close\n2026-01-02,100\n"),
		company,
		"licensed-csv",
		time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	)
	if err == nil {
		t.Fatal("missing adjusted close column accepted")
	}
	if !strings.Contains(err.Error(), "missing adjusted close column") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func marketSeriesFixture() string {
	return `date,adjusted_close,published_at
2026-01-02,100,2026-01-03
2026-01-05,N/A,2026-01-06
2026-01-06,110,2026-01-07
2026-01-07,121,2026-01-08
`
}
