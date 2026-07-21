package alpha

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

const fredSourceSystemID = "alpha_source_fred"

type FREDSeriesSnapshot struct {
	Series        FREDSeries
	ContentSHA256 string
	SizeBytes     int
	AvailableAt   time.Time
	Observations  []FREDSeriesObservation
	SkippedRows   int
}

type FREDSeries struct {
	SeriesID         string
	ProviderSeriesID string
	Frequency        string
	Unit             string
	AdjustmentPolicy string
	Timezone         string
}

type FREDSeriesObservation struct {
	ObservationID string
	ObservedAt    time.Time
	PublishedAt   time.Time
	VintageAt     time.Time
	ValueNumeric  string
}

func DefaultFREDFedFundsSeries() FREDSeries {
	return FREDSeries{
		SeriesID:         "alpha_series_fred.fedfunds",
		ProviderSeriesID: "FRED:FEDFUNDS",
		Frequency:        "monthly",
		Unit:             "percent",
		AdjustmentPolicy: "not-seasonally-adjusted",
		Timezone:         "America/New_York",
	}
}

func ParseFREDSeriesCSVSnapshot(content []byte, series FREDSeries, availableAt time.Time) (FREDSeriesSnapshot, error) {
	if len(content) == 0 {
		return FREDSeriesSnapshot{}, fmt.Errorf("FRED series snapshot is empty")
	}
	if availableAt.IsZero() {
		return FREDSeriesSnapshot{}, fmt.Errorf("FRED series snapshot availability time is required")
	}
	if err := validateFREDSeries(series); err != nil {
		return FREDSeriesSnapshot{}, err
	}
	reader := csv.NewReader(strings.NewReader(string(content)))
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	if err != nil {
		return FREDSeriesSnapshot{}, fmt.Errorf("parse FRED CSV: %w", err)
	}
	if len(records) < 2 {
		return FREDSeriesSnapshot{}, fmt.Errorf("FRED series CSV requires a header and at least one row")
	}
	header := normalizedCSVHeader(records[0])
	dateIndex, ok := firstHeaderIndex(header, "date", "observed_at", "observation_date")
	if !ok {
		return FREDSeriesSnapshot{}, fmt.Errorf("FRED series CSV missing date column")
	}
	valueIndex, ok := firstHeaderIndex(header, "value", "observation_value")
	if !ok {
		return FREDSeriesSnapshot{}, fmt.Errorf("FRED series CSV missing value column")
	}
	realtimeStartIndex, hasRealtimeStart := firstHeaderIndex(header, "realtime_start", "vintage_at", "published_at")
	observations := []FREDSeriesObservation{}
	skipped := 0
	for rowNumber, row := range records[1:] {
		if len(row) <= dateIndex || len(row) <= valueIndex {
			skipped++
			continue
		}
		observedAt, err := parseTreasuryDate(row[dateIndex])
		if err != nil {
			return FREDSeriesSnapshot{}, fmt.Errorf("FRED row %d invalid date: %w", rowNumber+2, err)
		}
		value, ok := normalizeDecimalString(row[valueIndex])
		if !ok {
			skipped++
			continue
		}
		publishedAt := availableAt.UTC()
		vintageAt := time.Time{}
		if hasRealtimeStart && len(row) > realtimeStartIndex && strings.TrimSpace(row[realtimeStartIndex]) != "" {
			parsed, err := parseTreasuryDate(row[realtimeStartIndex])
			if err != nil {
				return FREDSeriesSnapshot{}, fmt.Errorf("FRED row %d invalid realtime_start: %w", rowNumber+2, err)
			}
			publishedAt = parsed
			vintageAt = parsed
		}
		identity := strings.Join([]string{series.SeriesID, observedAt.Format("2006-01-02"), publishedAt.Format(time.RFC3339), value}, "|")
		if !vintageAt.IsZero() {
			identity += "|" + vintageAt.Format(time.RFC3339)
		}
		digest := sha256.Sum256([]byte(identity))
		observations = append(observations, FREDSeriesObservation{
			ObservationID: "alpha_series_obs_" + hex.EncodeToString(digest[:16]),
			ObservedAt:    observedAt,
			PublishedAt:   publishedAt,
			VintageAt:     vintageAt,
			ValueNumeric:  value,
		})
	}
	if len(observations) == 0 {
		return FREDSeriesSnapshot{}, fmt.Errorf("FRED series CSV produced no accepted observations")
	}
	sort.Slice(observations, func(left, right int) bool {
		return observations[left].ObservationID < observations[right].ObservationID
	})
	digest := sha256.Sum256(content)
	return FREDSeriesSnapshot{
		Series:        series,
		ContentSHA256: hex.EncodeToString(digest[:]),
		SizeBytes:     len(content),
		AvailableAt:   availableAt.UTC(),
		Observations:  observations,
		SkippedRows:   skipped,
	}, nil
}

func WriteFREDSeriesSeedSQL(writer io.Writer, tenantID, repositoryID string, snapshot FREDSeriesSnapshot) error {
	if !uuidLiteralPattern.MatchString(tenantID) {
		return fmt.Errorf("tenant ID must be a UUID")
	}
	if !uuidLiteralPattern.MatchString(repositoryID) {
		return fmt.Errorf("repository ID must be a UUID")
	}
	if err := validateFREDSeries(snapshot.Series); err != nil {
		return err
	}
	if len(snapshot.ContentSHA256) != 64 {
		return fmt.Errorf("FRED series snapshot content SHA-256 must be hex encoded")
	}
	if snapshot.SizeBytes <= 0 || snapshot.AvailableAt.IsZero() || len(snapshot.Observations) == 0 {
		return fmt.Errorf("FRED series snapshot is incomplete")
	}
	line := func(format string, args ...any) error {
		_, err := fmt.Fprintf(writer, format+"\n", args...)
		return err
	}
	if err := line("BEGIN;"); err != nil {
		return err
	}
	if err := writeFREDSourceSystemSQL(writer, tenantID, repositoryID); err != nil {
		return err
	}
	if err := writeFREDSourceObjectSQL(writer, tenantID, repositoryID, snapshot); err != nil {
		return err
	}
	if err := writeFREDSeriesSQL(writer, tenantID, repositoryID, snapshot.Series); err != nil {
		return err
	}
	for _, observation := range snapshot.Observations {
		if err := writeFREDSeriesObservationSQL(writer, tenantID, repositoryID, snapshot, observation); err != nil {
			return err
		}
	}
	return line("COMMIT;")
}

func validateFREDSeries(series FREDSeries) error {
	if !strings.HasPrefix(series.SeriesID, "alpha_series_") || strings.TrimSpace(series.ProviderSeriesID) == "" {
		return fmt.Errorf("FRED series identity is required")
	}
	if series.Frequency == "" || series.Unit == "" || series.AdjustmentPolicy == "" || series.Timezone == "" {
		return fmt.Errorf("FRED series metadata is required")
	}
	return nil
}

func writeFREDSourceSystemSQL(writer io.Writer, tenantID, repositoryID string) error {
	_, err := fmt.Fprintf(writer, `
INSERT INTO forja.alpha_source_systems (
    tenant_id, repository_id, source_system_id, source_key, display_name,
    data_family, license_policy, adapter_version, acquisition_policy, status
) VALUES (
    %s::uuid, %s::uuid, %s, 'fred.alfred', 'FRED/ALFRED Economic Data',
    'macro', '{"redistribution":"source-terms-required","access":"public-api-or-local-snapshot"}'::jsonb,
    'alpha-fred-series-snapshot-v1', '{"mode":"hash-pinned-csv-snapshot","vintage_columns":["realtime_start"]}'::jsonb, 'active'
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
		sqlString(tenantID), sqlString(repositoryID), sqlString(fredSourceSystemID))
	return err
}

func writeFREDSourceObjectSQL(writer io.Writer, tenantID, repositoryID string, snapshot FREDSeriesSnapshot) error {
	ingestionRunID := "alpha_ingest_" + snapshot.ContentSHA256[:32]
	sourceObjectID := snapshot.SourceObjectID()
	objectKey := fmt.Sprintf("alpha/fred/%s/%s.csv", strings.ToLower(snapshot.Series.ProviderSeriesID), snapshot.ContentSHA256)
	sourceURLDigest := sha256.Sum256([]byte("fred:" + snapshot.Series.ProviderSeriesID))
	availableAt := snapshot.AvailableAt.UTC().Format(time.RFC3339)
	scope := map[string]any{
		"source":             "fred.alfred",
		"provider_series_id": snapshot.Series.ProviderSeriesID,
	}
	metadata := map[string]any{
		"provider_series_id": snapshot.Series.ProviderSeriesID,
		"series_id":          snapshot.Series.SeriesID,
		"accepted_rows":      len(snapshot.Observations),
		"skipped_rows":       snapshot.SkippedRows,
		"source_limits":      "Hash-pinned local FRED/ALFRED CSV snapshot; accepted observations preserve observed, published, available, and vintage clocks when realtime_start is present.",
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
    %s::jsonb, 'succeeded', 'alpha-fred-series-snapshot-v1',
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
		sqlString(tenantID), sqlString(repositoryID), sqlString(ingestionRunID), sqlString(fredSourceSystemID),
		sqlString(string(scopeJSON)), sqlString(availableAt), sqlString(availableAt), len(snapshot.Observations),
		sqlString(tenantID), sqlString(repositoryID), sqlString(sourceObjectID), sqlString(fredSourceSystemID),
		sqlString(ingestionRunID), sqlString(objectKey), sqlString(snapshot.ContentSHA256), snapshot.SizeBytes,
		sqlString(hex.EncodeToString(sourceURLDigest[:])), sqlString(availableAt), sqlString(availableAt), sqlString(availableAt),
		sqlString(string(metadataJSON)))
	return err
}

func writeFREDSeriesSQL(writer io.Writer, tenantID, repositoryID string, series FREDSeries) error {
	_, err := fmt.Fprintf(writer, `
INSERT INTO forja.alpha_series (
    tenant_id, repository_id, series_id, source_system_id, provider_series_id,
    frequency, unit, adjustment_policy, timezone, license_policy, status
) VALUES (
    %s::uuid, %s::uuid, %s, %s, %s,
    %s, %s, %s, %s, '{"redistribution":"source-terms-required"}'::jsonb, 'active'
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
		sqlString(tenantID), sqlString(repositoryID), sqlString(series.SeriesID), sqlString(fredSourceSystemID), sqlString(series.ProviderSeriesID),
		sqlString(series.Frequency), sqlString(series.Unit), sqlString(series.AdjustmentPolicy), sqlString(series.Timezone))
	return err
}

func writeFREDSeriesObservationSQL(writer io.Writer, tenantID, repositoryID string, snapshot FREDSeriesSnapshot, observation FREDSeriesObservation) error {
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

func (snapshot FREDSeriesSnapshot) SourceObjectID() string {
	return "alpha_object_" + snapshot.ContentSHA256[:32]
}
