package alpha

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

type SECCompanyFactsSnapshot struct {
	Company       SECCompany
	ContentSHA256 string
	SizeBytes     int
	AvailableAt   time.Time
	Coverage      SECCompanyFactsCoverage
}

type SECCompanyFactsCoverage struct {
	EntityName     string
	TaxonomyCount  int
	ConceptCount   int
	UnitCount      int
	FactCount      int
	Forms          []string
	FiscalYears    []int
	Currencies     []string
	CanonicalHints []string
}

type secCompanyFactsPayload struct {
	CIK        int                                  `json:"cik"`
	EntityName string                               `json:"entityName"`
	Facts      map[string]map[string]secFactConcept `json:"facts"`
}

type secFactConcept struct {
	Label       string                  `json:"label"`
	Description string                  `json:"description"`
	Units       map[string][]secFactRow `json:"units"`
}

type secFactRow struct {
	End   string `json:"end"`
	Val   any    `json:"val"`
	Accn  string `json:"accn"`
	FY    int    `json:"fy"`
	FP    string `json:"fp"`
	Form  string `json:"form"`
	Filed string `json:"filed"`
	Frame string `json:"frame"`
}

func ParseSECCompanyFactsSnapshot(content []byte, company SECCompany, availableAt time.Time) (SECCompanyFactsSnapshot, error) {
	if len(content) == 0 {
		return SECCompanyFactsSnapshot{}, fmt.Errorf("SEC Company Facts snapshot is empty")
	}
	if availableAt.IsZero() {
		return SECCompanyFactsSnapshot{}, fmt.Errorf("SEC Company Facts snapshot availability time is required")
	}
	if strings.TrimSpace(company.CIK) == "" || strings.TrimSpace(company.IssuerID) == "" {
		return SECCompanyFactsSnapshot{}, fmt.Errorf("SEC company identity is required")
	}
	var payload secCompanyFactsPayload
	if err := json.Unmarshal(content, &payload); err != nil {
		return SECCompanyFactsSnapshot{}, fmt.Errorf("parse SEC Company Facts snapshot: %w", err)
	}
	if paddedCIK(payload.CIK) != company.CIK {
		return SECCompanyFactsSnapshot{}, fmt.Errorf("SEC Company Facts CIK mismatch for %s: got %s want %s", company.Ticker, paddedCIK(payload.CIK), company.CIK)
	}
	coverage := summarizeCompanyFacts(payload)
	if coverage.FactCount == 0 {
		return SECCompanyFactsSnapshot{}, fmt.Errorf("SEC Company Facts snapshot for %s has no facts", company.Ticker)
	}
	digest := sha256.Sum256(content)
	return SECCompanyFactsSnapshot{
		Company:       company,
		ContentSHA256: hex.EncodeToString(digest[:]),
		SizeBytes:     len(content),
		AvailableAt:   availableAt.UTC(),
		Coverage:      coverage,
	}, nil
}

func WriteSECCompanyFactsSeedSQL(writer io.Writer, tenantID, repositoryID string, snapshot SECCompanyFactsSnapshot) error {
	if !uuidLiteralPattern.MatchString(tenantID) {
		return fmt.Errorf("tenant ID must be a UUID")
	}
	if !uuidLiteralPattern.MatchString(repositoryID) {
		return fmt.Errorf("repository ID must be a UUID")
	}
	if err := validateSECCompanyFactsSnapshot(snapshot); err != nil {
		return err
	}
	line := func(format string, args ...any) error {
		_, err := fmt.Fprintf(writer, format+"\n", args...)
		return err
	}
	if err := line("BEGIN;"); err != nil {
		return err
	}
	if err := writeSECSourceSystemSQL(writer, tenantID, repositoryID, "alpha-sec-company-facts-snapshot-v1"); err != nil {
		return err
	}
	if err := writeCompanyFactsSourceObjectSQL(writer, tenantID, repositoryID, snapshot); err != nil {
		return err
	}
	return line("COMMIT;")
}

func summarizeCompanyFacts(payload secCompanyFactsPayload) SECCompanyFactsCoverage {
	forms := map[string]bool{}
	years := map[int]bool{}
	currencies := map[string]bool{}
	hints := map[string]bool{}
	unitCount := 0
	factCount := 0
	conceptCount := 0
	for taxonomy, concepts := range payload.Facts {
		_ = taxonomy
		for conceptName, concept := range concepts {
			conceptCount++
			if canonicalCompanyFactHint(conceptName) != "" {
				hints[canonicalCompanyFactHint(conceptName)] = true
			}
			for unit, rows := range concept.Units {
				unitCount++
				if isCurrencyUnit(unit) {
					currencies[strings.ToUpper(unit)] = true
				}
				for _, row := range rows {
					factCount++
					form := normalizeSECForm(row.Form)
					if form != "other" {
						forms[form] = true
					}
					if row.FY != 0 {
						years[row.FY] = true
					}
				}
			}
		}
	}
	return SECCompanyFactsCoverage{
		EntityName:     strings.TrimSpace(payload.EntityName),
		TaxonomyCount:  len(payload.Facts),
		ConceptCount:   conceptCount,
		UnitCount:      unitCount,
		FactCount:      factCount,
		Forms:          sortedStringKeys(forms),
		FiscalYears:    sortedIntKeys(years),
		Currencies:     sortedStringKeys(currencies),
		CanonicalHints: sortedStringKeys(hints),
	}
}

func validateSECCompanyFactsSnapshot(snapshot SECCompanyFactsSnapshot) error {
	if len(snapshot.ContentSHA256) != 64 {
		return fmt.Errorf("SEC Company Facts snapshot content SHA-256 must be hex encoded")
	}
	if _, err := hex.DecodeString(snapshot.ContentSHA256); err != nil {
		return fmt.Errorf("SEC Company Facts snapshot content SHA-256 must be valid hex: %w", err)
	}
	if snapshot.SizeBytes <= 0 {
		return fmt.Errorf("SEC Company Facts snapshot size must be positive")
	}
	if snapshot.AvailableAt.IsZero() {
		return fmt.Errorf("SEC Company Facts snapshot availability time is required")
	}
	if strings.TrimSpace(snapshot.Company.IssuerID) == "" {
		return fmt.Errorf("SEC Company Facts snapshot company issuer ID is required")
	}
	if snapshot.Coverage.FactCount == 0 {
		return fmt.Errorf("SEC Company Facts snapshot fact count is required")
	}
	return nil
}

func writeCompanyFactsSourceObjectSQL(writer io.Writer, tenantID, repositoryID string, snapshot SECCompanyFactsSnapshot) error {
	ingestionRunID := "alpha_ingest_" + snapshot.ContentSHA256[:32]
	sourceObjectID := snapshot.CompanyFactsSourceObjectID()
	objectKey := fmt.Sprintf("alpha/sec/companyfacts/CIK%s/%s.json", snapshot.Company.CIK, snapshot.ContentSHA256)
	sourceURL := fmt.Sprintf("https://data.sec.gov/api/xbrl/companyfacts/CIK%s.json", snapshot.Company.CIK)
	sourceURLDigest := sha256.Sum256([]byte(sourceURL))
	availableAt := snapshot.AvailableAt.UTC().Format(time.RFC3339)
	scope := map[string]any{
		"source": "sec.companyfacts",
		"cik":    snapshot.Company.CIK,
		"ticker": snapshot.Company.Ticker,
	}
	metadata := map[string]any{
		"source":          sourceURL,
		"ticker":          snapshot.Company.Ticker,
		"cik":             snapshot.Company.CIK,
		"issuer_id":       snapshot.Company.IssuerID,
		"entity_name":     snapshot.Coverage.EntityName,
		"taxonomy_count":  snapshot.Coverage.TaxonomyCount,
		"concept_count":   snapshot.Coverage.ConceptCount,
		"unit_count":      snapshot.Coverage.UnitCount,
		"fact_count":      snapshot.Coverage.FactCount,
		"forms":           snapshot.Coverage.Forms,
		"fiscal_years":    snapshot.Coverage.FiscalYears,
		"currencies":      snapshot.Coverage.Currencies,
		"canonical_hints": snapshot.Coverage.CanonicalHints,
		"source_limits":   "SEC Company Facts is a structured fact snapshot; canonical metric mapping, context construction, and accounting selection remain separate reviewed steps.",
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
    %s::jsonb, 'succeeded', 'alpha-sec-company-facts-snapshot-v1',
    %s::timestamptz, %s::timestamptz,
    '{"mode":"snapshot","endpoint":"companyfacts"}'::jsonb, 1, %d
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
		sqlString(string(scopeJSON)), sqlString(availableAt), sqlString(availableAt), snapshot.Coverage.FactCount,
		sqlString(tenantID), sqlString(repositoryID), sqlString(sourceObjectID), sqlString(secSourceSystemID),
		sqlString(ingestionRunID), sqlString(objectKey), sqlString(snapshot.ContentSHA256), snapshot.SizeBytes,
		sqlString(hex.EncodeToString(sourceURLDigest[:])), sqlString(availableAt), sqlString(availableAt), sqlString(availableAt),
		sqlString(string(metadataJSON)))
	return err
}

func (snapshot SECCompanyFactsSnapshot) CompanyFactsSourceObjectID() string {
	return "alpha_object_" + snapshot.ContentSHA256[:32]
}

func canonicalCompanyFactHint(conceptName string) string {
	switch strings.ToLower(strings.TrimSpace(conceptName)) {
	case "revenues", "revenuefromcontractwithcustomerexcludingassessedtax":
		return "revenue"
	case "operatingincome_loss", "operatingincomeloss":
		return "operating_income"
	case "netincomeloss":
		return "net_income"
	case "netcashprovidedbyusedinoperatingactivities":
		return "operating_cash_flow"
	case "payments_to_acquire_property_plant_and_equipment", "paymentstoacquirepropertyplantandequipment":
		return "capital_expenditure"
	default:
		return ""
	}
}

func isCurrencyUnit(unit string) bool {
	unit = strings.ToUpper(strings.TrimSpace(unit))
	return len(unit) == 3 && unit[0] >= 'A' && unit[0] <= 'Z' && unit[1] >= 'A' && unit[1] <= 'Z' && unit[2] >= 'A' && unit[2] <= 'Z'
}

func sortedStringKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedIntKeys(values map[int]bool) []int {
	keys := make([]int, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	return keys
}
