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
	RawFacts      []SECCompanyFact
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

type SECCompanyFact struct {
	FactID       string
	TaxonomyID   string
	Taxonomy     string
	ConceptID    string
	ConceptName  string
	ConceptLabel string
	ContextID    string
	ContextHash  string
	FilingID     string
	Accession    string
	Unit         string
	Currency     string
	LexicalValue string
	NumericValue string
	PeriodEnd    string
	FormType     string
	FiscalYear   int
	FiscalPeriod string
	FiledAt      string
	Frame        string
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
	End   string          `json:"end"`
	Val   json.RawMessage `json:"val"`
	Accn  string          `json:"accn"`
	FY    int             `json:"fy"`
	FP    string          `json:"fp"`
	Form  string          `json:"form"`
	Filed string          `json:"filed"`
	Frame string          `json:"frame"`
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
	rawFacts, err := extractCompanyFacts(payload, company)
	if err != nil {
		return SECCompanyFactsSnapshot{}, err
	}
	digest := sha256.Sum256(content)
	return SECCompanyFactsSnapshot{
		Company:       company,
		ContentSHA256: hex.EncodeToString(digest[:]),
		SizeBytes:     len(content),
		AvailableAt:   availableAt.UTC(),
		Coverage:      coverage,
		RawFacts:      rawFacts,
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
	if err := writeCompanyFactsRawSQL(writer, tenantID, repositoryID, snapshot); err != nil {
		return err
	}
	return line("COMMIT;")
}

func extractCompanyFacts(payload secCompanyFactsPayload, company SECCompany) ([]SECCompanyFact, error) {
	facts := []SECCompanyFact{}
	taxonomies := sortedTaxonomyNames(payload.Facts)
	for _, taxonomy := range taxonomies {
		conceptNames := sortedConceptNames(payload.Facts[taxonomy])
		for _, conceptName := range conceptNames {
			concept := payload.Facts[taxonomy][conceptName]
			units := sortedFactUnits(concept.Units)
			for _, unit := range units {
				for _, row := range concept.Units[unit] {
					fact, err := companyFactFromRow(company, taxonomy, conceptName, concept, unit, row)
					if err != nil {
						return nil, err
					}
					facts = append(facts, fact)
				}
			}
		}
	}
	sort.Slice(facts, func(left, right int) bool {
		return facts[left].FactID < facts[right].FactID
	})
	return facts, nil
}

func companyFactFromRow(company SECCompany, taxonomy, conceptName string, concept secFactConcept, unit string, row secFactRow) (SECCompanyFact, error) {
	accession := strings.TrimSpace(row.Accn)
	if accession == "" {
		return SECCompanyFact{}, fmt.Errorf("SEC Company Facts row for %s:%s missing accession", taxonomy, conceptName)
	}
	periodEnd := strings.TrimSpace(row.End)
	if _, err := parseSECFilingDate(periodEnd); err != nil {
		return SECCompanyFact{}, fmt.Errorf("SEC Company Facts row for %s:%s accession %s has invalid end date: %w", taxonomy, conceptName, accession, err)
	}
	filedAt := strings.TrimSpace(row.Filed)
	if filedAt != "" {
		if _, err := parseSECFilingDate(filedAt); err != nil {
			return SECCompanyFact{}, fmt.Errorf("SEC Company Facts row for %s:%s accession %s has invalid filed date: %w", taxonomy, conceptName, accession, err)
		}
	}
	lexical, numeric, err := factValueStrings(row.Val)
	if err != nil {
		return SECCompanyFact{}, fmt.Errorf("SEC Company Facts row for %s:%s accession %s has invalid value: %w", taxonomy, conceptName, accession, err)
	}
	form := normalizeSECForm(row.Form)
	if form == "other" {
		form = strings.ToUpper(strings.TrimSpace(row.Form))
	}
	contextHashInput := strings.Join([]string{company.CIK, accession, taxonomy, conceptName, unit, periodEnd, row.Frame}, "|")
	contextDigest := sha256.Sum256([]byte(contextHashInput))
	contextHash := hex.EncodeToString(contextDigest[:])
	factHashInput := strings.Join([]string{contextHashInput, lexical}, "|")
	factDigest := sha256.Sum256([]byte(factHashInput))
	return SECCompanyFact{
		FactID:       "alpha_fact_" + hex.EncodeToString(factDigest[:16]),
		TaxonomyID:   taxonomyID(taxonomy),
		Taxonomy:     taxonomy,
		ConceptID:    conceptID(taxonomy, conceptName),
		ConceptName:  conceptName,
		ConceptLabel: strings.TrimSpace(concept.Label),
		ContextID:    "alpha_context_" + hex.EncodeToString(contextDigest[:16]),
		ContextHash:  contextHash,
		FilingID:     filingID(company.CIK, accession),
		Accession:    accession,
		Unit:         strings.TrimSpace(unit),
		Currency:     currencyFromUnit(unit),
		LexicalValue: lexical,
		NumericValue: numeric,
		PeriodEnd:    periodEnd,
		FormType:     form,
		FiscalYear:   row.FY,
		FiscalPeriod: strings.TrimSpace(row.FP),
		FiledAt:      filedAt,
		Frame:        strings.TrimSpace(row.Frame),
	}, nil
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
	if len(snapshot.RawFacts) != snapshot.Coverage.FactCount {
		return fmt.Errorf("SEC Company Facts raw fact count = %d, want %d", len(snapshot.RawFacts), snapshot.Coverage.FactCount)
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
		"source_limits":   "SEC Company Facts is a structured fact snapshot; raw XBRL-like facts are persisted, while canonical metric mapping and accounting selection remain separate reviewed steps.",
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

func writeCompanyFactsRawSQL(writer io.Writer, tenantID, repositoryID string, snapshot SECCompanyFactsSnapshot) error {
	if len(snapshot.RawFacts) == 0 {
		return nil
	}
	taxonomies := map[string]SECCompanyFact{}
	concepts := map[string]SECCompanyFact{}
	contexts := map[string]SECCompanyFact{}
	for _, fact := range snapshot.RawFacts {
		taxonomies[fact.TaxonomyID] = fact
		concepts[fact.ConceptID] = fact
		contexts[fact.ContextID] = fact
	}
	for _, taxonomyID := range sortedFactKeys(taxonomies) {
		fact := taxonomies[taxonomyID]
		if err := writeTaxonomySQL(writer, tenantID, repositoryID, snapshot.CompanyFactsSourceObjectID(), fact); err != nil {
			return err
		}
	}
	for _, conceptID := range sortedFactKeys(concepts) {
		fact := concepts[conceptID]
		if err := writeConceptSQL(writer, tenantID, repositoryID, fact); err != nil {
			return err
		}
	}
	for _, contextID := range sortedFactKeys(contexts) {
		fact := contexts[contextID]
		if err := writeContextSQL(writer, tenantID, repositoryID, snapshot.Company, fact); err != nil {
			return err
		}
	}
	for _, fact := range snapshot.RawFacts {
		if err := writeRawFactSQL(writer, tenantID, repositoryID, snapshot.CompanyFactsSourceObjectID(), fact); err != nil {
			return err
		}
	}
	return nil
}

func writeTaxonomySQL(writer io.Writer, tenantID, repositoryID, sourceObjectID string, fact SECCompanyFact) error {
	authority := "other"
	if strings.EqualFold(fact.Taxonomy, "us-gaap") {
		authority = "us-gaap"
	} else if strings.EqualFold(fact.Taxonomy, "dei") {
		authority = "sec"
	}
	_, err := fmt.Fprintf(writer, `
INSERT INTO forja.alpha_taxonomies (
    tenant_id, repository_id, taxonomy_id, namespace_uri, taxonomy_version,
    authority, source_object_id
) VALUES (
    %s::uuid, %s::uuid, %s, %s, 'companyfacts-snapshot',
    %s, %s
) ON CONFLICT (tenant_id, repository_id, taxonomy_id) DO UPDATE SET
    namespace_uri=EXCLUDED.namespace_uri,
    taxonomy_version=EXCLUDED.taxonomy_version,
    authority=EXCLUDED.authority,
    source_object_id=EXCLUDED.source_object_id;
`,
		sqlString(tenantID), sqlString(repositoryID), sqlString(fact.TaxonomyID), sqlString("sec:"+fact.Taxonomy),
		sqlString(authority), sqlString(sourceObjectID))
	return err
}

func writeConceptSQL(writer io.Writer, tenantID, repositoryID string, fact SECCompanyFact) error {
	label := strings.TrimSpace(fact.ConceptLabel)
	if label == "" {
		label = fact.ConceptName
	}
	_, err := fmt.Fprintf(writer, `
INSERT INTO forja.alpha_xbrl_concepts (
    tenant_id, repository_id, concept_id, taxonomy_id, qualified_name,
    data_type, balance, period_type, standard_class
) VALUES (
    %s::uuid, %s::uuid, %s, %s, %s,
    'monetary_or_numeric', NULL, 'instant', 'standard'
) ON CONFLICT (tenant_id, repository_id, concept_id) DO UPDATE SET
    taxonomy_id=EXCLUDED.taxonomy_id,
    qualified_name=EXCLUDED.qualified_name,
    data_type=EXCLUDED.data_type,
    balance=EXCLUDED.balance,
    period_type=EXCLUDED.period_type,
    standard_class=EXCLUDED.standard_class;
`,
		sqlString(tenantID), sqlString(repositoryID), sqlString(fact.ConceptID), sqlString(fact.TaxonomyID), sqlString(fact.Taxonomy+":"+label))
	return err
}

func writeContextSQL(writer io.Writer, tenantID, repositoryID string, company SECCompany, fact SECCompanyFact) error {
	_, err := fmt.Fprintf(writer, `
INSERT INTO forja.alpha_xbrl_contexts (
    tenant_id, repository_id, context_id, filing_id, entity_identifier,
    period_start, period_end, instant, dimensions, context_sha256
) VALUES (
    %s::uuid, %s::uuid, %s, %s, %s,
    NULL, %s::date, %s::date, %s::jsonb, decode(%s, 'hex')
) ON CONFLICT (tenant_id, repository_id, filing_id, context_sha256) DO UPDATE SET
    context_id=EXCLUDED.context_id,
    entity_identifier=EXCLUDED.entity_identifier,
    period_end=EXCLUDED.period_end,
    instant=EXCLUDED.instant,
    dimensions=EXCLUDED.dimensions;
`,
		sqlString(tenantID), sqlString(repositoryID), sqlString(fact.ContextID), sqlString(fact.FilingID), sqlString(company.CIK),
		sqlString(fact.PeriodEnd), sqlString(fact.PeriodEnd), sqlString(contextDimensionsJSON(fact)), sqlString(fact.ContextHash))
	return err
}

func writeRawFactSQL(writer io.Writer, tenantID, repositoryID, sourceObjectID string, fact SECCompanyFact) error {
	numericValue := "NULL"
	if fact.NumericValue != "" {
		numericValue = fact.NumericValue
	}
	currency := "NULL"
	if fact.Currency != "" {
		currency = sqlString(fact.Currency)
	}
	_, err := fmt.Fprintf(writer, `
INSERT INTO forja.alpha_xbrl_facts (
    tenant_id, repository_id, fact_id, filing_id, concept_id, context_id,
    source_object_id, unit, decimals, lexical_value, numeric_value, currency,
    scale, quality_state
) VALUES (
    %s::uuid, %s::uuid, %s, %s, %s, %s,
    %s, %s, NULL, %s, %s, %s,
    0, 'accepted'
) ON CONFLICT (tenant_id, repository_id, fact_id) DO UPDATE SET
    filing_id=EXCLUDED.filing_id,
    concept_id=EXCLUDED.concept_id,
    context_id=EXCLUDED.context_id,
    source_object_id=EXCLUDED.source_object_id,
    unit=EXCLUDED.unit,
    lexical_value=EXCLUDED.lexical_value,
    numeric_value=EXCLUDED.numeric_value,
    currency=EXCLUDED.currency,
    quality_state=EXCLUDED.quality_state;
`,
		sqlString(tenantID), sqlString(repositoryID), sqlString(fact.FactID), sqlString(fact.FilingID), sqlString(fact.ConceptID), sqlString(fact.ContextID),
		sqlString(sourceObjectID), sqlString(fact.Unit), sqlString(fact.LexicalValue), numericValue, currency)
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

func factValueStrings(raw json.RawMessage) (string, string, error) {
	lexical := strings.TrimSpace(string(raw))
	if lexical == "" || lexical == "null" {
		return "", "", fmt.Errorf("missing fact value")
	}
	if strings.HasPrefix(lexical, `"`) {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", "", err
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return "", "", fmt.Errorf("empty string fact value")
		}
		return text, "", nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return "", "", err
	}
	return number.String(), number.String(), nil
}

func taxonomyID(taxonomy string) string {
	digest := sha256.Sum256([]byte("taxonomy:" + strings.ToLower(strings.TrimSpace(taxonomy))))
	return "alpha_taxonomy_" + hex.EncodeToString(digest[:16])
}

func conceptID(taxonomy, conceptName string) string {
	digest := sha256.Sum256([]byte("concept:" + strings.ToLower(strings.TrimSpace(taxonomy)) + ":" + strings.ToLower(strings.TrimSpace(conceptName))))
	return "alpha_concept_" + hex.EncodeToString(digest[:16])
}

func currencyFromUnit(unit string) string {
	if isCurrencyUnit(unit) {
		return strings.ToUpper(strings.TrimSpace(unit))
	}
	return ""
}

func contextDimensionsJSON(fact SECCompanyFact) string {
	dimensions := map[string]any{}
	if fact.Frame != "" {
		dimensions["sec_frame"] = fact.Frame
	}
	if fact.FormType != "" {
		dimensions["form"] = fact.FormType
	}
	if fact.FiscalYear != 0 {
		dimensions["fy"] = fact.FiscalYear
	}
	if fact.FiscalPeriod != "" {
		dimensions["fp"] = fact.FiscalPeriod
	}
	encoded, err := json.Marshal(dimensions)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func sortedTaxonomyNames(values map[string]map[string]secFactConcept) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedConceptNames(values map[string]secFactConcept) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedFactUnits(values map[string][]secFactRow) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedFactKeys(values map[string]SECCompanyFact) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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
