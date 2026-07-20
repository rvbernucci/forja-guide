package alpha

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

const treasurySourceSystemID = "alpha_source_treasury"

type TreasurySeriesSnapshot struct {
	Series        TreasurySeries
	ContentSHA256 string
	SizeBytes     int
	AvailableAt   time.Time
	Observations  []TreasurySeriesObservation
	SkippedRows   int
}

type TreasurySeries struct {
	SeriesID         string
	ProviderSeriesID string
	Frequency        string
	Unit             string
	AdjustmentPolicy string
	Timezone         string
}

type TreasurySeriesObservation struct {
	ObservationID string
	ObservedAt    time.Time
	PublishedAt   time.Time
	VintageAt     time.Time
	ValueNumeric  string
}

func ParseTreasurySeriesCSVSnapshot(content []byte, series TreasurySeries, availableAt time.Time) (TreasurySeriesSnapshot, error) {
	if len(content) == 0 {
		return TreasurySeriesSnapshot{}, fmt.Errorf("Treasury series snapshot is empty")
	}
	if availableAt.IsZero() {
		return TreasurySeriesSnapshot{}, fmt.Errorf("Treasury series snapshot availability time is required")
	}
	if err := validateTreasurySeries(series); err != nil {
		return TreasurySeriesSnapshot{}, err
	}
	reader := csv.NewReader(strings.NewReader(string(content)))
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	if err != nil {
		return TreasurySeriesSnapshot{}, fmt.Errorf("parse Treasury CSV: %w", err)
	}
	if len(records) < 2 {
		return TreasurySeriesSnapshot{}, fmt.Errorf("Treasury series CSV requires a header and at least one row")
	}
	header := normalizedCSVHeader(records[0])
	dateIndex, ok := firstHeaderIndex(header, "date", "observed_at", "observation_date")
	if !ok {
		return TreasurySeriesSnapshot{}, fmt.Errorf("Treasury series CSV missing date column")
	}
	valueIndex, ok := firstHeaderIndex(header, "value", "rate", "real_yield", "10y_real_yield")
	if !ok {
		return TreasurySeriesSnapshot{}, fmt.Errorf("Treasury series CSV missing value/rate column")
	}
	publishedIndex, hasPublished := firstHeaderIndex(header, "published_at", "published", "publication_date")
	vintageIndex, hasVintage := firstHeaderIndex(header, "vintage_at", "vintage", "revision_date")
	observations := []TreasurySeriesObservation{}
	skipped := 0
	for rowNumber, row := range records[1:] {
		if len(row) <= dateIndex || len(row) <= valueIndex {
			skipped++
			continue
		}
		observedAt, err := parseTreasuryDate(row[dateIndex])
		if err != nil {
			return TreasurySeriesSnapshot{}, fmt.Errorf("Treasury row %d invalid date: %w", rowNumber+2, err)
		}
		value, ok := normalizeDecimalString(row[valueIndex])
		if !ok {
			skipped++
			continue
		}
		publishedAt := availableAt.UTC()
		if hasPublished && len(row) > publishedIndex && strings.TrimSpace(row[publishedIndex]) != "" {
			parsed, err := parseTreasuryDate(row[publishedIndex])
			if err != nil {
				return TreasurySeriesSnapshot{}, fmt.Errorf("Treasury row %d invalid published_at: %w", rowNumber+2, err)
			}
			publishedAt = parsed
		}
		vintageAt := time.Time{}
		if hasVintage && len(row) > vintageIndex && strings.TrimSpace(row[vintageIndex]) != "" {
			parsed, err := parseTreasuryDate(row[vintageIndex])
			if err != nil {
				return TreasurySeriesSnapshot{}, fmt.Errorf("Treasury row %d invalid vintage_at: %w", rowNumber+2, err)
			}
			vintageAt = parsed
		}
		identity := strings.Join([]string{series.SeriesID, observedAt.Format("2006-01-02"), publishedAt.Format(time.RFC3339), value}, "|")
		if !vintageAt.IsZero() {
			identity += "|" + vintageAt.Format(time.RFC3339)
		}
		digest := sha256.Sum256([]byte(identity))
		observations = append(observations, TreasurySeriesObservation{
			ObservationID: "alpha_series_obs_" + hex.EncodeToString(digest[:16]),
			ObservedAt:    observedAt,
			PublishedAt:   publishedAt,
			VintageAt:     vintageAt,
			ValueNumeric:  value,
		})
	}
	if len(observations) == 0 {
		return TreasurySeriesSnapshot{}, fmt.Errorf("Treasury series CSV produced no accepted observations")
	}
	sort.Slice(observations, func(left, right int) bool {
		return observations[left].ObservationID < observations[right].ObservationID
	})
	digest := sha256.Sum256(content)
	return TreasurySeriesSnapshot{
		Series:        series,
		ContentSHA256: hex.EncodeToString(digest[:]),
		SizeBytes:     len(content),
		AvailableAt:   availableAt.UTC(),
		Observations:  observations,
		SkippedRows:   skipped,
	}, nil
}

func WriteTreasurySeriesSeedSQL(writer io.Writer, tenantID, repositoryID string, snapshot TreasurySeriesSnapshot) error {
	if !uuidLiteralPattern.MatchString(tenantID) {
		return fmt.Errorf("tenant ID must be a UUID")
	}
	if !uuidLiteralPattern.MatchString(repositoryID) {
		return fmt.Errorf("repository ID must be a UUID")
	}
	if err := validateTreasurySeries(snapshot.Series); err != nil {
		return err
	}
	if len(snapshot.ContentSHA256) != 64 {
		return fmt.Errorf("Treasury series snapshot content SHA-256 must be hex encoded")
	}
	if snapshot.SizeBytes <= 0 || snapshot.AvailableAt.IsZero() || len(snapshot.Observations) == 0 {
		return fmt.Errorf("Treasury series snapshot is incomplete")
	}
	line := func(format string, args ...any) error {
		_, err := fmt.Fprintf(writer, format+"\n", args...)
		return err
	}
	if err := line("BEGIN;"); err != nil {
		return err
	}
	if err := writeTreasurySourceSystemSQL(writer, tenantID, repositoryID); err != nil {
		return err
	}
	if err := writeTreasurySourceObjectSQL(writer, tenantID, repositoryID, snapshot); err != nil {
		return err
	}
	if err := writeTreasurySeriesSQL(writer, tenantID, repositoryID, snapshot.Series); err != nil {
		return err
	}
	for _, observation := range snapshot.Observations {
		if err := writeTreasurySeriesObservationSQL(writer, tenantID, repositoryID, snapshot, observation); err != nil {
			return err
		}
	}
	return line("COMMIT;")
}

func DefaultTreasuryRealYield10YSeries() TreasurySeries {
	return TreasurySeries{
		SeriesID:         "alpha_series_treasury.real-yield.10y",
		ProviderSeriesID: "treasury.real-yield.10y",
		Frequency:        "daily",
		Unit:             "percent",
		AdjustmentPolicy: "not-seasonally-adjusted",
		Timezone:         "America/New_York",
	}
}

func validateTreasurySeries(series TreasurySeries) error {
	if !strings.HasPrefix(series.SeriesID, "alpha_series_") || strings.TrimSpace(series.ProviderSeriesID) == "" {
		return fmt.Errorf("Treasury series identity is required")
	}
	if series.Frequency == "" || series.Unit == "" || series.AdjustmentPolicy == "" || series.Timezone == "" {
		return fmt.Errorf("Treasury series metadata is required")
	}
	return nil
}

func writeTreasurySourceSystemSQL(writer io.Writer, tenantID, repositoryID string) error {
	_, err := fmt.Fprintf(writer, `
INSERT INTO forja.alpha_source_systems (
    tenant_id, repository_id, source_system_id, source_key, display_name,
    data_family, license_policy, adapter_version, acquisition_policy, status
) VALUES (
    %s::uuid, %s::uuid, %s, 'treasury.interest-rates', 'US Treasury Interest Rates',
    'treasury', '{"redistribution":"public-source-with-attribution","access":"public"}'::jsonb,
    'alpha-treasury-series-snapshot-v1', '{"mode":"hash-pinned-csv-snapshot"}'::jsonb, 'active'
) ON CONFLICT (tenant_id, repository_id, source_system_id) DO UPDATE SET
    source_key=EXCLUDED.source_key,
    display_name=EXCLUDED.display_name,
    data_family=EXCLUDED.data_family,
    license_policy=EXCLUDED.license_policy,
    adapter_version=EXCLUDED.adapter_version,
    acquisition_policy=EXCLUDED.acquisition_policy,
    status=EXCLUDED.status,
    updated_at=clock_timestamp();
`,
		sqlString(tenantID), sqlString(repositoryID), sqlString(treasurySourceSystemID))
	return err
}

func writeTreasurySourceObjectSQL(writer io.Writer, tenantID, repositoryID string, snapshot TreasurySeriesSnapshot) error {
	ingestionRunID := "alpha_ingest_" + snapshot.ContentSHA256[:32]
	sourceObjectID := snapshot.SourceObjectID()
	objectKey := fmt.Sprintf("alpha/treasury/%s/%s.csv", snapshot.Series.ProviderSeriesID, snapshot.ContentSHA256)
	sourceURLDigest := sha256.Sum256([]byte("treasury:" + snapshot.Series.ProviderSeriesID))
	availableAt := snapshot.AvailableAt.UTC().Format(time.RFC3339)
	scope := map[string]any{
		"source":             "treasury.interest-rates",
		"provider_series_id": snapshot.Series.ProviderSeriesID,
	}
	metadata := map[string]any{
		"provider_series_id": snapshot.Series.ProviderSeriesID,
		"series_id":          snapshot.Series.SeriesID,
		"accepted_rows":      len(snapshot.Observations),
		"skipped_rows":       snapshot.SkippedRows,
		"source_limits":      "Hash-pinned local Treasury CSV snapshot; accepted observations preserve observed, published, available, and optional vintage clocks.",
	}
	scopeJSON, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, `
INSERT INTO forja.alpha_ingestion_runs (
    tenant_id, repository_id, ingestion_run_id, source_system_id, scope, state,
    code_version, started_at, finished_at, watermark, object_count, row_count
) VALUES (
    %s::uuid, %s::uuid, %s, %s,
    %s::jsonb, 'succeeded', 'alpha-treasury-series-snapshot-v1',
    %s::timestamptz, %s::timestamptz,
    '{"mode":"snapshot","format":"csv"}'::jsonb, 1, %d
) ON CONFLICT (tenant_id, repository_id, ingestion_run_id) DO UPDATE SET
    state=EXCLUDED.state,
    code_version=EXCLUDED.code_version,
    finished_at=EXCLUDED.finished_at,
    watermark=EXCLUDED.watermark,
    object_count=EXCLUDED.object_count,
    row_count=EXCLUDED.row_count;

INSERT INTO forja.alpha_source_objects (
    tenant_id, repository_id, source_object_id, source_system_id,
    ingestion_run_id, object_key, content_sha256, size_bytes, media_type,
    source_uri_fingerprint, published_at, available_at, ingested_at,
    lifecycle, metadata
) VALUES (
    %s::uuid, %s::uuid, %s, %s,
    %s, %s, decode(%s, 'hex'), %d, 'text/csv',
    %s, %s::timestamptz, %s::timestamptz, %s::timestamptz,
    'active', %s::jsonb
) ON CONFLICT (tenant_id, repository_id, source_object_id) DO UPDATE SET
    source_system_id=EXCLUDED.source_system_id,
    ingestion_run_id=EXCLUDED.ingestion_run_id,
    object_key=EXCLUDED.object_key,
    content_sha256=EXCLUDED.content_sha256,
    size_bytes=EXCLUDED.size_bytes,
    media_type=EXCLUDED.media_type,
    source_uri_fingerprint=EXCLUDED.source_uri_fingerprint,
    published_at=EXCLUDED.published_at,
    available_at=EXCLUDED.available_at,
    ingested_at=EXCLUDED.ingested_at,
    lifecycle=EXCLUDED.lifecycle,
    metadata=EXCLUDED.metadata;
`,
		sqlString(tenantID), sqlString(repositoryID), sqlString(ingestionRunID), sqlString(treasurySourceSystemID),
		sqlString(string(scopeJSON)), sqlString(availableAt), sqlString(availableAt), len(snapshot.Observations),
		sqlString(tenantID), sqlString(repositoryID), sqlString(sourceObjectID), sqlString(treasurySourceSystemID),
		sqlString(ingestionRunID), sqlString(objectKey), sqlString(snapshot.ContentSHA256), snapshot.SizeBytes,
		sqlString(hex.EncodeToString(sourceURLDigest[:])), sqlString(availableAt), sqlString(availableAt), sqlString(availableAt),
		sqlString(string(metadataJSON)))
	return err
}

func writeTreasurySeriesSQL(writer io.Writer, tenantID, repositoryID string, series TreasurySeries) error {
	_, err := fmt.Fprintf(writer, `
INSERT INTO forja.alpha_series (
    tenant_id, repository_id, series_id, source_system_id, provider_series_id,
    frequency, unit, adjustment_policy, timezone, license_policy, status
) VALUES (
    %s::uuid, %s::uuid, %s, %s, %s,
    %s, %s, %s, %s, '{"redistribution":"public-source-with-attribution"}'::jsonb, 'active'
) ON CONFLICT (tenant_id, repository_id, series_id) DO UPDATE SET
    source_system_id=EXCLUDED.source_system_id,
    provider_series_id=EXCLUDED.provider_series_id,
    frequency=EXCLUDED.frequency,
    unit=EXCLUDED.unit,
    adjustment_policy=EXCLUDED.adjustment_policy,
    timezone=EXCLUDED.timezone,
    license_policy=EXCLUDED.license_policy,
    status=EXCLUDED.status;
`,
		sqlString(tenantID), sqlString(repositoryID), sqlString(series.SeriesID), sqlString(treasurySourceSystemID), sqlString(series.ProviderSeriesID),
		sqlString(series.Frequency), sqlString(series.Unit), sqlString(series.AdjustmentPolicy), sqlString(series.Timezone))
	return err
}

func writeTreasurySeriesObservationSQL(writer io.Writer, tenantID, repositoryID string, snapshot TreasurySeriesSnapshot, observation TreasurySeriesObservation) error {
	vintageAt := "NULL"
	if !observation.VintageAt.IsZero() {
		vintageAt = sqlString(observation.VintageAt.UTC().Format(time.RFC3339)) + "::timestamptz"
	}
	_, err := fmt.Fprintf(writer, `
INSERT INTO forja.alpha_series_observations (
    tenant_id, repository_id, series_observation_id, series_id, observed_at,
    published_at, available_at, vintage_at, value_numeric, source_object_id,
    quality_state
) VALUES (
    %s::uuid, %s::uuid, %s, %s, %s::timestamptz,
    %s::timestamptz, %s::timestamptz, %s, %s, %s,
    'accepted'
) ON CONFLICT (tenant_id, repository_id, series_observation_id) DO UPDATE SET
    observed_at=EXCLUDED.observed_at,
    published_at=EXCLUDED.published_at,
    available_at=EXCLUDED.available_at,
    vintage_at=EXCLUDED.vintage_at,
    value_numeric=EXCLUDED.value_numeric,
    source_object_id=EXCLUDED.source_object_id,
    quality_state=EXCLUDED.quality_state;
`,
		sqlString(tenantID), sqlString(repositoryID), sqlString(observation.ObservationID), sqlString(snapshot.Series.SeriesID), sqlString(observation.ObservedAt.UTC().Format(time.RFC3339)),
		sqlString(observation.PublishedAt.UTC().Format(time.RFC3339)), sqlString(snapshot.AvailableAt.UTC().Format(time.RFC3339)), vintageAt, observation.ValueNumeric, sqlString(snapshot.SourceObjectID()))
	return err
}

func (snapshot TreasurySeriesSnapshot) SourceObjectID() string {
	return "alpha_object_" + snapshot.ContentSHA256[:32]
}

func normalizedCSVHeader(row []string) map[string]int {
	header := map[string]int{}
	for index, value := range row {
		header[strings.ToLower(strings.TrimSpace(value))] = index
	}
	return header
}

func firstHeaderIndex(header map[string]int, names ...string) (int, bool) {
	for _, name := range names {
		if index, ok := header[name]; ok {
			return index, true
		}
	}
	return 0, false
}

func parseTreasuryDate(raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported date %q", raw)
}

func normalizeDecimalString(raw string) (string, bool) {
	trimmed := strings.TrimSpace(strings.ReplaceAll(raw, ",", ""))
	if trimmed == "" || trimmed == "." || strings.EqualFold(trimmed, "N/A") {
		return "", false
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return "", false
	}
	return strconv.FormatFloat(value, 'f', -1, 64), true
}
