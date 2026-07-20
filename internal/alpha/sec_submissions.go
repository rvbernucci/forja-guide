package alpha

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

type SECSubmissionsSnapshot struct {
	Company       SECCompany
	ContentSHA256 string
	SizeBytes     int
	AvailableAt   time.Time
	Filings       []SECFiling
}

type SECFiling struct {
	FilingID       string
	Accession      string
	FormType       string
	PrimaryDoc     string
	FiscalYear     int
	FiscalPeriod   string
	PeriodEnd      string
	FiledAt        time.Time
	AvailableAt    time.Time
	SourceObjectID string
}

type secSubmissionsPayload struct {
	CIK     string   `json:"cik"`
	Name    string   `json:"name"`
	Tickers []string `json:"tickers"`
	Filings struct {
		Recent secRecentFilings `json:"recent"`
	} `json:"filings"`
}

type secRecentFilings struct {
	AccessionNumber   []string `json:"accessionNumber"`
	FilingDate        []string `json:"filingDate"`
	ReportDate        []string `json:"reportDate"`
	AcceptanceDate    []string `json:"acceptanceDateTime"`
	Form              []string `json:"form"`
	PrimaryDocument   []string `json:"primaryDocument"`
	PrimaryDocDesc    []string `json:"primaryDocDescription"`
	InlineXBRL        []int    `json:"isInlineXBRL"`
	Size              []int    `json:"size"`
	Items             []string `json:"items"`
	FilmNumber        []string `json:"filmNumber"`
	FileNumber        []string `json:"fileNumber"`
	Act               []string `json:"act"`
	CoreType          []string `json:"core_type"`
	SubmissionType    []string `json:"submissionType"`
	AssistantDirector []string `json:"assistantDirector"`
}

func ParseSECSubmissionsSnapshot(content []byte, company SECCompany, availableAt time.Time) (SECSubmissionsSnapshot, error) {
	if len(content) == 0 {
		return SECSubmissionsSnapshot{}, fmt.Errorf("SEC submissions snapshot is empty")
	}
	if availableAt.IsZero() {
		return SECSubmissionsSnapshot{}, fmt.Errorf("SEC submissions snapshot availability time is required")
	}
	if strings.TrimSpace(company.CIK) == "" || strings.TrimSpace(company.IssuerID) == "" {
		return SECSubmissionsSnapshot{}, fmt.Errorf("SEC company identity is required")
	}
	var payload secSubmissionsPayload
	if err := json.Unmarshal(content, &payload); err != nil {
		return SECSubmissionsSnapshot{}, fmt.Errorf("parse SEC submissions snapshot: %w", err)
	}
	if paddedCIKString(payload.CIK) != company.CIK {
		return SECSubmissionsSnapshot{}, fmt.Errorf("SEC submissions CIK mismatch for %s: got %s want %s", company.Ticker, paddedCIKString(payload.CIK), company.CIK)
	}
	if !submissionTickersContain(payload.Tickers, company.Ticker) {
		return SECSubmissionsSnapshot{}, fmt.Errorf("SEC submissions snapshot for %s does not list ticker %s", company.CIK, company.Ticker)
	}
	if err := validateRecentLengths(payload.Filings.Recent); err != nil {
		return SECSubmissionsSnapshot{}, err
	}
	digest := sha256.Sum256(content)
	sourceObjectID := "alpha_object_" + hex.EncodeToString(digest[:16])
	filings, err := extractRecentFilings(payload.Filings.Recent, company, sourceObjectID)
	if err != nil {
		return SECSubmissionsSnapshot{}, err
	}
	return SECSubmissionsSnapshot{
		Company:       company,
		ContentSHA256: hex.EncodeToString(digest[:]),
		SizeBytes:     len(content),
		AvailableAt:   availableAt.UTC(),
		Filings:       filings,
	}, nil
}

func WriteSECSubmissionsSeedSQL(writer io.Writer, tenantID, repositoryID string, snapshot SECSubmissionsSnapshot) error {
	if !uuidLiteralPattern.MatchString(tenantID) {
		return fmt.Errorf("tenant ID must be a UUID")
	}
	if !uuidLiteralPattern.MatchString(repositoryID) {
		return fmt.Errorf("repository ID must be a UUID")
	}
	if err := validateSECSubmissionsSnapshot(snapshot); err != nil {
		return err
	}
	line := func(format string, args ...any) error {
		_, err := fmt.Fprintf(writer, format+"\n", args...)
		return err
	}
	if err := line("BEGIN;"); err != nil {
		return err
	}
	if err := writeSECSourceSystemSQL(writer, tenantID, repositoryID, "alpha-sec-submissions-snapshot-v1"); err != nil {
		return err
	}
	if err := writeSubmissionsSourceObjectSQL(writer, tenantID, repositoryID, snapshot); err != nil {
		return err
	}
	for _, filing := range snapshot.Filings {
		if err := writeFilingSQL(writer, tenantID, repositoryID, snapshot.Company, filing); err != nil {
			return err
		}
	}
	return line("COMMIT;")
}

func extractRecentFilings(recent secRecentFilings, company SECCompany, sourceObjectID string) ([]SECFiling, error) {
	filings := make([]SECFiling, 0, len(recent.AccessionNumber))
	for index, accession := range recent.AccessionNumber {
		form := normalizeSECForm(recent.Form[index])
		if !isAlphaFilingForm(form) {
			continue
		}
		accession = strings.TrimSpace(accession)
		if accession == "" {
			return nil, fmt.Errorf("SEC submissions filing at index %d missing accession number", index)
		}
		filedAt, err := parseSECFilingDate(recent.FilingDate[index])
		if err != nil {
			return nil, fmt.Errorf("parse filing date for %s: %w", accession, err)
		}
		availableAt, err := parseSECAcceptanceTime(recent.AcceptanceDate[index])
		if err != nil {
			return nil, fmt.Errorf("parse acceptance time for %s: %w", accession, err)
		}
		if availableAt.Before(filedAt) {
			filedAt = availableAt
		}
		fiscalYear, fiscalPeriod := inferFiscalFrame(form, recent.ReportDate[index])
		filings = append(filings, SECFiling{
			FilingID:       filingID(company.CIK, accession),
			Accession:      accession,
			FormType:       form,
			PrimaryDoc:     strings.TrimSpace(recent.PrimaryDocument[index]),
			FiscalYear:     fiscalYear,
			FiscalPeriod:   fiscalPeriod,
			PeriodEnd:      strings.TrimSpace(recent.ReportDate[index]),
			FiledAt:        filedAt.UTC(),
			AvailableAt:    availableAt.UTC(),
			SourceObjectID: sourceObjectID,
		})
	}
	return filings, nil
}

func validateRecentLengths(recent secRecentFilings) error {
	length := len(recent.AccessionNumber)
	fields := map[string]int{
		"filingDate":         len(recent.FilingDate),
		"reportDate":         len(recent.ReportDate),
		"acceptanceDateTime": len(recent.AcceptanceDate),
		"form":               len(recent.Form),
		"primaryDocument":    len(recent.PrimaryDocument),
	}
	for name, fieldLength := range fields {
		if fieldLength != length {
			return fmt.Errorf("SEC submissions recent.%s length = %d, want %d", name, fieldLength, length)
		}
	}
	return nil
}

func validateSECSubmissionsSnapshot(snapshot SECSubmissionsSnapshot) error {
	if len(snapshot.ContentSHA256) != 64 {
		return fmt.Errorf("SEC submissions snapshot content SHA-256 must be hex encoded")
	}
	if _, err := hex.DecodeString(snapshot.ContentSHA256); err != nil {
		return fmt.Errorf("SEC submissions snapshot content SHA-256 must be valid hex: %w", err)
	}
	if snapshot.SizeBytes <= 0 {
		return fmt.Errorf("SEC submissions snapshot size must be positive")
	}
	if snapshot.AvailableAt.IsZero() {
		return fmt.Errorf("SEC submissions snapshot availability time is required")
	}
	if strings.TrimSpace(snapshot.Company.IssuerID) == "" {
		return fmt.Errorf("SEC submissions snapshot company issuer ID is required")
	}
	for _, filing := range snapshot.Filings {
		if filing.FilingID == "" || filing.Accession == "" || filing.FormType == "" {
			return fmt.Errorf("SEC submissions snapshot contains incomplete filing")
		}
		if filing.SourceObjectID == "" {
			return fmt.Errorf("SEC submissions filing %s missing source object ID", filing.Accession)
		}
	}
	return nil
}

func writeSECSourceSystemSQL(writer io.Writer, tenantID, repositoryID, adapterVersion string) error {
	_, err := fmt.Fprintf(writer, `
INSERT INTO forja.alpha_source_systems (
    tenant_id, repository_id, source_system_id, source_key, display_name,
    data_family, license_policy, adapter_version, acquisition_policy, status
) VALUES (
    %s::uuid, %s::uuid, %s, 'sec.edgar', 'SEC EDGAR',
    'sec', '{"redistribution":"public-source-with-attribution","access":"fair-access"}'::jsonb,
    %s, '{"mode":"versioned-public-snapshot"}'::jsonb, 'active'
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
		sqlString(tenantID), sqlString(repositoryID), sqlString(secSourceSystemID), sqlString(adapterVersion))
	return err
}

func writeSubmissionsSourceObjectSQL(writer io.Writer, tenantID, repositoryID string, snapshot SECSubmissionsSnapshot) error {
	ingestionRunID := "alpha_ingest_" + snapshot.ContentSHA256[:32]
	objectKey := fmt.Sprintf("alpha/sec/submissions/CIK%s/%s.json", snapshot.Company.CIK, snapshot.ContentSHA256)
	sourceURL := secSubmissionsURL(snapshot.Company.CIK)
	sourceURLDigest := sha256.Sum256([]byte(sourceURL))
	availableAt := snapshot.AvailableAt.UTC().Format(time.RFC3339)
	scope := map[string]any{
		"source": "sec.submissions",
		"cik":    snapshot.Company.CIK,
		"ticker": snapshot.Company.Ticker,
	}
	metadata := map[string]any{
		"source":           sourceURL,
		"ticker":           snapshot.Company.Ticker,
		"cik":              snapshot.Company.CIK,
		"issuer_id":        snapshot.Company.IssuerID,
		"filtered_forms":   []string{"10-K", "10-K/A", "10-Q", "10-Q/A"},
		"selected_filings": len(snapshot.Filings),
		"source_limits":    "SEC submissions metadata identifies filings and availability; filing documents and Company Facts remain separate source objects.",
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
    %s::jsonb,
    'succeeded', 'alpha-sec-submissions-snapshot-v1',
    %s::timestamptz, %s::timestamptz,
    '{"mode":"snapshot","endpoint":"submissions"}'::jsonb, 1, %d
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
    %s, %s, decode(%s, 'hex'), %d, 'application/json',
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
		sqlString(tenantID), sqlString(repositoryID), sqlString(ingestionRunID), sqlString(secSourceSystemID),
		sqlString(string(scopeJSON)), sqlString(availableAt), sqlString(availableAt), len(snapshot.Filings),
		sqlString(tenantID), sqlString(repositoryID), sqlString(snapshot.FilingsSourceObjectID()), sqlString(secSourceSystemID),
		sqlString(ingestionRunID), sqlString(objectKey), sqlString(snapshot.ContentSHA256), snapshot.SizeBytes,
		sqlString(hex.EncodeToString(sourceURLDigest[:])), sqlString(availableAt), sqlString(availableAt), sqlString(availableAt),
		sqlString(string(metadataJSON)))
	return err
}

func writeFilingSQL(writer io.Writer, tenantID, repositoryID string, company SECCompany, filing SECFiling) error {
	periodEnd := "NULL"
	if filing.PeriodEnd != "" {
		periodEnd = sqlString(filing.PeriodEnd) + "::date"
	}
	fiscalYear := "NULL"
	if filing.FiscalYear != 0 {
		fiscalYear = fmt.Sprintf("%d", filing.FiscalYear)
	}
	fiscalPeriod := "NULL"
	if filing.FiscalPeriod != "" {
		fiscalPeriod = sqlString(filing.FiscalPeriod)
	}
	_, err := fmt.Fprintf(writer, `
INSERT INTO forja.alpha_filings (
    tenant_id, repository_id, filing_id, issuer_id, source_system_id,
    source_object_id, accession_number, form_type, fiscal_year,
    fiscal_period, period_start, period_end, filed_at, available_at,
    amendment_of_filing_id, lifecycle
) VALUES (
    %s::uuid, %s::uuid, %s, %s, %s,
    %s, %s, %s, %s,
    %s, NULL, %s, %s::timestamptz, %s::timestamptz,
    NULL, 'active'
) ON CONFLICT (tenant_id, repository_id, accession_number) DO UPDATE SET
    issuer_id=EXCLUDED.issuer_id,
    source_system_id=EXCLUDED.source_system_id,
    source_object_id=EXCLUDED.source_object_id,
    form_type=EXCLUDED.form_type,
    fiscal_year=EXCLUDED.fiscal_year,
    fiscal_period=EXCLUDED.fiscal_period,
    period_end=EXCLUDED.period_end,
    filed_at=EXCLUDED.filed_at,
    available_at=EXCLUDED.available_at,
    lifecycle=EXCLUDED.lifecycle;
`,
		sqlString(tenantID), sqlString(repositoryID), sqlString(filing.FilingID), sqlString(company.IssuerID), sqlString(secSourceSystemID),
		sqlString(filing.SourceObjectID), sqlString(filing.Accession), sqlString(filing.FormType), fiscalYear,
		fiscalPeriod, periodEnd, sqlString(filing.FiledAt.Format(time.RFC3339)), sqlString(filing.AvailableAt.Format(time.RFC3339)))
	return err
}

func (snapshot SECSubmissionsSnapshot) FilingsSourceObjectID() string {
	return "alpha_object_" + snapshot.ContentSHA256[:32]
}

func filingID(cik, accession string) string {
	digest := sha256.Sum256([]byte("sec-filing:" + cik + ":" + accession))
	return "alpha_filing_" + hex.EncodeToString(digest[:16])
}

func isAlphaFilingForm(form string) bool {
	switch form {
	case "10-K", "10-K/A", "10-Q", "10-Q/A":
		return true
	default:
		return false
	}
}

func normalizeSECForm(form string) string {
	form = strings.ToUpper(strings.TrimSpace(form))
	switch form {
	case "10-K", "10-K/A", "10-Q", "10-Q/A":
		return form
	default:
		return "other"
	}
}

func inferFiscalFrame(form, reportDate string) (int, string) {
	year := 0
	if len(reportDate) >= 4 {
		fmt.Sscanf(reportDate[:4], "%d", &year)
	}
	switch form {
	case "10-K", "10-K/A":
		return year, "FY"
	case "10-Q", "10-Q/A":
		return year, ""
	default:
		return year, ""
	}
}

func parseSECFilingDate(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("empty filing date")
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func parseSECAcceptanceTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("empty acceptance time")
	}
	for _, layout := range []string{time.RFC3339Nano, "20060102150405"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported SEC acceptance time %q", value)
}

func submissionTickersContain(tickers []string, expected string) bool {
	expected = strings.ToUpper(strings.TrimSpace(expected))
	for _, ticker := range tickers {
		if strings.ToUpper(strings.TrimSpace(ticker)) == expected {
			return true
		}
	}
	return false
}

func paddedCIKString(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "CIK")
	value = strings.TrimLeft(value, "0")
	if value == "" {
		value = "0"
	}
	return fmt.Sprintf("%010s", value)
}

func secSubmissionsURL(cik string) string {
	escaped := url.PathEscape(paddedCIKString(cik))
	return "https://data.sec.gov/submissions/CIK" + escaped + ".json"
}
