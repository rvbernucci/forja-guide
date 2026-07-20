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

const marketSourceSystemID = "alpha_source_market_csv"

type MarketSeriesSnapshot struct {
	Company             SECCompany
	Provider            string
	ContentSHA256       string
	SizeBytes           int
	AvailableAt         time.Time
	PriceSeries         FREDSeries
	ReturnSeries        FREDSeries
	PriceObservations   []FREDSeriesObservation
	ReturnObservations  []FREDSeriesObservation
	SkippedRows         int
	CorporateActionNote string
}

type marketPriceRow struct {
	ObservedAt  time.Time
	PublishedAt time.Time
	VintageAt   time.Time
	Value       string
	FloatValue  float64
}

func ParseMarketSeriesCSVSnapshot(content []byte, company SECCompany, provider string, availableAt time.Time) (MarketSeriesSnapshot, error) {
	if len(content) == 0 {
		return MarketSeriesSnapshot{}, fmt.Errorf("market series snapshot is empty")
	}
	if strings.TrimSpace(company.Ticker) == "" || strings.TrimSpace(company.SecurityID) == "" {
		return MarketSeriesSnapshot{}, fmt.Errorf("market series company identity is required")
	}
	if strings.TrimSpace(provider) == "" {
		return MarketSeriesSnapshot{}, fmt.Errorf("market series provider is required")
	}
	if availableAt.IsZero() {
		return MarketSeriesSnapshot{}, fmt.Errorf("market series snapshot availability time is required")
	}
	reader := csv.NewReader(strings.NewReader(string(content)))
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	if err != nil {
		return MarketSeriesSnapshot{}, fmt.Errorf("parse market CSV: %w", err)
	}
	if len(records) < 2 {
		return MarketSeriesSnapshot{}, fmt.Errorf("market series CSV requires a header and at least one row")
	}
	header := normalizedCSVHeader(records[0])
	dateIndex, ok := firstHeaderIndex(header, "date", "observed_at", "trading_date")
	if !ok {
		return MarketSeriesSnapshot{}, fmt.Errorf("market series CSV missing date column")
	}
	priceIndex, ok := firstHeaderIndex(header, "adjusted_close", "adj_close", "adjclose", "close_adjusted")
	if !ok {
		return MarketSeriesSnapshot{}, fmt.Errorf("market series CSV missing adjusted close column")
	}
	publishedIndex, hasPublished := firstHeaderIndex(header, "published_at", "available_on", "as_of")
	vintageIndex, hasVintage := firstHeaderIndex(header, "vintage_at", "revision_date")
	rows := []marketPriceRow{}
	skipped := 0
	for rowNumber, row := range records[1:] {
		if len(row) <= dateIndex || len(row) <= priceIndex {
			skipped++
			continue
		}
		observedAt, err := parseTreasuryDate(row[dateIndex])
		if err != nil {
			return MarketSeriesSnapshot{}, fmt.Errorf("market row %d invalid date: %w", rowNumber+2, err)
		}
		value, ok := normalizeDecimalString(row[priceIndex])
		if !ok {
			skipped++
			continue
		}
		floatValue, err := strconv.ParseFloat(value, 64)
		if err != nil || floatValue <= 0 {
			skipped++
			continue
		}
		publishedAt := availableAt.UTC()
		if hasPublished && len(row) > publishedIndex && strings.TrimSpace(row[publishedIndex]) != "" {
			parsed, err := parseTreasuryDate(row[publishedIndex])
			if err != nil {
				return MarketSeriesSnapshot{}, fmt.Errorf("market row %d invalid published_at: %w", rowNumber+2, err)
			}
			publishedAt = parsed
		}
		vintageAt := time.Time{}
		if hasVintage && len(row) > vintageIndex && strings.TrimSpace(row[vintageIndex]) != "" {
			parsed, err := parseTreasuryDate(row[vintageIndex])
			if err != nil {
				return MarketSeriesSnapshot{}, fmt.Errorf("market row %d invalid vintage_at: %w", rowNumber+2, err)
			}
			vintageAt = parsed
		}
		rows = append(rows, marketPriceRow{
			ObservedAt:  observedAt,
			PublishedAt: publishedAt,
			VintageAt:   vintageAt,
			Value:       value,
			FloatValue:  floatValue,
		})
	}
	if len(rows) == 0 {
		return MarketSeriesSnapshot{}, fmt.Errorf("market series CSV produced no accepted prices")
	}
	sort.Slice(rows, func(left, right int) bool {
		return rows[left].ObservedAt.Before(rows[right].ObservedAt)
	})
	ticker := strings.ToLower(company.Ticker)
	priceSeries := FREDSeries{
		SeriesID:         "alpha_series_market." + ticker + ".adjusted_close",
		ProviderSeriesID: strings.TrimSpace(provider) + ":" + company.Ticker + ":adjusted_close",
		Frequency:        "daily",
		Unit:             company.Currency,
		AdjustmentPolicy: "provider-adjusted-close",
		Timezone:         "America/New_York",
	}
	returnSeries := FREDSeries{
		SeriesID:         "alpha_series_market." + ticker + ".daily_return",
		ProviderSeriesID: strings.TrimSpace(provider) + ":" + company.Ticker + ":daily_return",
		Frequency:        "daily",
		Unit:             "ratio",
		AdjustmentPolicy: "derived-from-provider-adjusted-close-simple-return",
		Timezone:         "America/New_York",
	}
	prices := make([]FREDSeriesObservation, 0, len(rows))
	returns := make([]FREDSeriesObservation, 0, len(rows)-1)
	for index, row := range rows {
		prices = append(prices, marketObservation(priceSeries.SeriesID, row.ObservedAt, row.PublishedAt, row.VintageAt, row.Value))
		if index == 0 {
			continue
		}
		previous := rows[index-1]
		if previous.FloatValue <= 0 {
			continue
		}
		returnValue := strconv.FormatFloat((row.FloatValue/previous.FloatValue)-1, 'f', -1, 64)
		returns = append(returns, marketObservation(returnSeries.SeriesID, row.ObservedAt, row.PublishedAt, row.VintageAt, returnValue))
	}
	if len(returns) == 0 {
		return MarketSeriesSnapshot{}, fmt.Errorf("market series CSV produced no derived returns")
	}
	digest := sha256.Sum256(content)
	return MarketSeriesSnapshot{
		Company:             company,
		Provider:            strings.TrimSpace(provider),
		ContentSHA256:       hex.EncodeToString(digest[:]),
		SizeBytes:           len(content),
		AvailableAt:         availableAt.UTC(),
		PriceSeries:         priceSeries,
		ReturnSeries:        returnSeries,
		PriceObservations:   prices,
		ReturnObservations:  returns,
		SkippedRows:         skipped,
		CorporateActionNote: "Adjusted close is accepted from the reviewed provider snapshot; daily returns are simple returns derived mechanically from adjacent accepted adjusted closes.",
	}, nil
}

func WriteMarketSeriesSeedSQL(writer io.Writer, tenantID, repositoryID string, snapshot MarketSeriesSnapshot) error {
	if !uuidLiteralPattern.MatchString(tenantID) {
		return fmt.Errorf("tenant ID must be a UUID")
	}
	if !uuidLiteralPattern.MatchString(repositoryID) {
		return fmt.Errorf("repository ID must be a UUID")
	}
	if len(snapshot.ContentSHA256) != 64 || snapshot.SizeBytes <= 0 || snapshot.AvailableAt.IsZero() {
		return fmt.Errorf("market series snapshot is incomplete")
	}
	if len(snapshot.PriceObservations) == 0 || len(snapshot.ReturnObservations) == 0 {
		return fmt.Errorf("market series observations are incomplete")
	}
	line := func(format string, args ...any) error {
		_, err := fmt.Fprintf(writer, format+"\n", args...)
		return err
	}
	if err := line("BEGIN;"); err != nil {
		return err
	}
	if err := writeMarketSourceSystemSQL(writer, tenantID, repositoryID); err != nil {
		return err
	}
	if err := writeMarketSourceObjectSQL(writer, tenantID, repositoryID, snapshot); err != nil {
		return err
	}
	for _, series := range []FREDSeries{snapshot.PriceSeries, snapshot.ReturnSeries} {
		if err := writeMarketSeriesSQL(writer, tenantID, repositoryID, series); err != nil {
			return err
		}
	}
	for _, observation := range snapshot.PriceObservations {
		if err := writeMarketSeriesObservationSQL(writer, tenantID, repositoryID, snapshot, snapshot.PriceSeries.SeriesID, observation); err != nil {
			return err
		}
	}
	for _, observation := range snapshot.ReturnObservations {
		if err := writeMarketSeriesObservationSQL(writer, tenantID, repositoryID, snapshot, snapshot.ReturnSeries.SeriesID, observation); err != nil {
			return err
		}
	}
	return line("COMMIT;")
}

func marketObservation(seriesID string, observedAt, publishedAt, vintageAt time.Time, value string) FREDSeriesObservation {
	identity := strings.Join([]string{seriesID, observedAt.Format("2006-01-02"), publishedAt.Format(time.RFC3339), value}, "|")
	if !vintageAt.IsZero() {
		identity += "|" + vintageAt.Format(time.RFC3339)
	}
	digest := sha256.Sum256([]byte(identity))
	return FREDSeriesObservation{
		ObservationID: "alpha_series_obs_" + hex.EncodeToString(digest[:16]),
		ObservedAt:    observedAt,
		PublishedAt:   publishedAt,
		VintageAt:     vintageAt,
		ValueNumeric:  value,
	}
}

func writeMarketSourceSystemSQL(writer io.Writer, tenantID, repositoryID string) error {
	_, err := fmt.Fprintf(writer, `
INSERT INTO forja.alpha_source_systems (
    tenant_id, repository_id, source_system_id, source_key, display_name,
    data_family, license_policy, adapter_version, acquisition_policy, status
) VALUES (
    %s::uuid, %s::uuid, %s, 'market.adjusted-prices.csv', 'Reviewed Market Data CSV',
    'market', '{"redistribution":"provider-or-user-license-required","access":"licensed-or-user-supplied"}'::jsonb,
    'alpha-market-series-snapshot-v1', '{"mode":"hash-pinned-csv-snapshot","derived_series":["daily_return"]}'::jsonb, 'active'
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
		sqlString(tenantID), sqlString(repositoryID), sqlString(marketSourceSystemID))
	return err
}

func writeMarketSourceObjectSQL(writer io.Writer, tenantID, repositoryID string, snapshot MarketSeriesSnapshot) error {
	ingestionRunID := "alpha_ingest_" + snapshot.ContentSHA256[:32]
	sourceObjectID := snapshot.SourceObjectID()
	objectKey := fmt.Sprintf("alpha/market/%s/%s.csv", strings.ToLower(snapshot.Company.Ticker), snapshot.ContentSHA256)
	sourceURLDigest := sha256.Sum256([]byte("market:" + snapshot.Provider + ":" + snapshot.Company.Ticker))
	availableAt := snapshot.AvailableAt.UTC().Format(time.RFC3339)
	scope := map[string]any{
		"source":   "market.adjusted-prices.csv",
		"provider": snapshot.Provider,
		"ticker":   snapshot.Company.Ticker,
	}
	metadata := map[string]any{
		"provider":              snapshot.Provider,
		"ticker":                snapshot.Company.Ticker,
		"security_id":           snapshot.Company.SecurityID,
		"price_series_id":       snapshot.PriceSeries.SeriesID,
		"return_series_id":      snapshot.ReturnSeries.SeriesID,
		"accepted_price_rows":   len(snapshot.PriceObservations),
		"derived_return_rows":   len(snapshot.ReturnObservations),
		"skipped_rows":          snapshot.SkippedRows,
		"corporate_action_note": snapshot.CorporateActionNote,
		"source_limits":         "Hash-pinned licensed or user-supplied adjusted-price CSV snapshot; provider adjustment policy must be reviewed before redistribution.",
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
    %s::jsonb, 'succeeded', 'alpha-market-series-snapshot-v1',
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
		sqlString(tenantID), sqlString(repositoryID), sqlString(ingestionRunID), sqlString(marketSourceSystemID),
		sqlString(string(scopeJSON)), sqlString(availableAt), sqlString(availableAt), len(snapshot.PriceObservations)+len(snapshot.ReturnObservations),
		sqlString(tenantID), sqlString(repositoryID), sqlString(sourceObjectID), sqlString(marketSourceSystemID),
		sqlString(ingestionRunID), sqlString(objectKey), sqlString(snapshot.ContentSHA256), snapshot.SizeBytes,
		sqlString(hex.EncodeToString(sourceURLDigest[:])), sqlString(availableAt), sqlString(availableAt), sqlString(availableAt),
		sqlString(string(metadataJSON)))
	return err
}

func writeMarketSeriesSQL(writer io.Writer, tenantID, repositoryID string, series FREDSeries) error {
	_, err := fmt.Fprintf(writer, `
INSERT INTO forja.alpha_series (
    tenant_id, repository_id, series_id, source_system_id, provider_series_id,
    frequency, unit, adjustment_policy, timezone, license_policy, status
) VALUES (
    %s::uuid, %s::uuid, %s, %s, %s,
    %s, %s, %s, %s, '{"redistribution":"provider-or-user-license-required"}'::jsonb, 'active'
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
		sqlString(tenantID), sqlString(repositoryID), sqlString(series.SeriesID), sqlString(marketSourceSystemID), sqlString(series.ProviderSeriesID),
		sqlString(series.Frequency), sqlString(series.Unit), sqlString(series.AdjustmentPolicy), sqlString(series.Timezone))
	return err
}

func writeMarketSeriesObservationSQL(writer io.Writer, tenantID, repositoryID string, snapshot MarketSeriesSnapshot, seriesID string, observation FREDSeriesObservation) error {
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
		sqlString(tenantID), sqlString(repositoryID), sqlString(observation.ObservationID), sqlString(seriesID), sqlString(observation.ObservedAt.UTC().Format(time.RFC3339)),
		sqlString(observation.PublishedAt.UTC().Format(time.RFC3339)), sqlString(snapshot.AvailableAt.UTC().Format(time.RFC3339)), vintageAt, observation.ValueNumeric, sqlString(snapshot.SourceObjectID()))
	return err
}

func (snapshot MarketSeriesSnapshot) SourceObjectID() string {
	return "alpha_object_" + snapshot.ContentSHA256[:32]
}
