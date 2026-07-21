package alpha

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	secSourceSystemID = "alpha_source_sec_edgar"
	secTickersURL     = "https://www.sec.gov/files/company_tickers.json"
	seedValidFrom     = "2000-01-01T00:00:00Z"
)

var uuidLiteralPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type SECCompany struct {
	Ticker        string
	Name          string
	CIK           string
	IssuerID      string
	SecurityID    string
	Exchange      string
	Currency      string
	SourceName    string
	SearchAliases []string
}

type SECSnapshot struct {
	ContentSHA256 string
	SizeBytes     int
	AvailableAt   time.Time
	Matches       []SECCompany
}

type secCompanyTickerEntry struct {
	CIK    int    `json:"cik_str"`
	Ticker string `json:"ticker"`
	Title  string `json:"title"`
}

func MagnificentSevenSECCompanies() []SECCompany {
	companies := []SECCompany{
		{Ticker: "AAPL", Name: "Apple Inc.", CIK: "0000320193", IssuerID: "alpha_issuer_aapl", SecurityID: "alpha_security_aapl_common", Exchange: "NASDAQ", Currency: "USD", SourceName: "Apple Inc.", SearchAliases: []string{"apple"}},
		{Ticker: "MSFT", Name: "Microsoft Corporation", CIK: "0000789019", IssuerID: "alpha_issuer_msft", SecurityID: "alpha_security_msft_common", Exchange: "NASDAQ", Currency: "USD", SourceName: "MICROSOFT CORP", SearchAliases: []string{"microsoft"}},
		{Ticker: "GOOGL", Name: "Alphabet Inc.", CIK: "0001652044", IssuerID: "alpha_issuer_googl", SecurityID: "alpha_security_googl_common", Exchange: "NASDAQ", Currency: "USD", SourceName: "Alphabet Inc.", SearchAliases: []string{"alphabet", "google"}},
		{Ticker: "AMZN", Name: "Amazon.com, Inc.", CIK: "0001018724", IssuerID: "alpha_issuer_amzn", SecurityID: "alpha_security_amzn_common", Exchange: "NASDAQ", Currency: "USD", SourceName: "AMAZON COM INC", SearchAliases: []string{"amazon", "amazon.com"}},
		{Ticker: "NVDA", Name: "NVIDIA Corporation", CIK: "0001045810", IssuerID: "alpha_issuer_nvda", SecurityID: "alpha_security_nvda_common", Exchange: "NASDAQ", Currency: "USD", SourceName: "NVIDIA CORP", SearchAliases: []string{"nvidia"}},
		{Ticker: "META", Name: "Meta Platforms, Inc.", CIK: "0001326801", IssuerID: "alpha_issuer_meta", SecurityID: "alpha_security_meta_common", Exchange: "NASDAQ", Currency: "USD", SourceName: "Meta Platforms, Inc.", SearchAliases: []string{"meta", "facebook"}},
		{Ticker: "TSLA", Name: "Tesla, Inc.", CIK: "0001318605", IssuerID: "alpha_issuer_tsla", SecurityID: "alpha_security_tsla_common", Exchange: "NASDAQ", Currency: "USD", SourceName: "Tesla, Inc.", SearchAliases: []string{"tesla"}},
	}
	return append([]SECCompany(nil), companies...)
}

func ResolveSECCompany(value string) (SECCompany, bool) {
	needle := normalizeCompanyLookup(value)
	if needle == "" {
		return SECCompany{}, false
	}
	for _, company := range MagnificentSevenSECCompanies() {
		if needle == normalizeCompanyLookup(company.Ticker) ||
			needle == normalizeCompanyLookup(company.CIK) ||
			needle == normalizeCompanyLookup(company.Name) ||
			needle == normalizeCompanyLookup(company.SourceName) {
			return company, true
		}
		for _, alias := range company.SearchAliases {
			if needle == normalizeCompanyLookup(alias) {
				return company, true
			}
		}
	}
	return SECCompany{}, false
}

func ParseSECCompanyTickersSnapshot(content []byte, availableAt time.Time) (SECSnapshot, error) {
	if len(content) == 0 {
		return SECSnapshot{}, fmt.Errorf("SEC company tickers snapshot is empty")
	}
	if availableAt.IsZero() {
		return SECSnapshot{}, fmt.Errorf("SEC company tickers snapshot availability time is required")
	}
	var entries map[string]secCompanyTickerEntry
	if err := json.Unmarshal(content, &entries); err != nil {
		return SECSnapshot{}, fmt.Errorf("parse SEC company tickers snapshot: %w", err)
	}
	if len(entries) == 0 {
		return SECSnapshot{}, fmt.Errorf("SEC company tickers snapshot has no entries")
	}
	byTicker := make(map[string]secCompanyTickerEntry, len(entries))
	for _, entry := range entries {
		ticker := strings.ToUpper(strings.TrimSpace(entry.Ticker))
		if ticker == "" {
			continue
		}
		byTicker[ticker] = entry
	}
	matches := MagnificentSevenSECCompanies()
	for index, company := range matches {
		entry, ok := byTicker[company.Ticker]
		if !ok {
			return SECSnapshot{}, fmt.Errorf("SEC company tickers snapshot missing %s", company.Ticker)
		}
		if paddedCIK(entry.CIK) != company.CIK {
			return SECSnapshot{}, fmt.Errorf("SEC company tickers snapshot CIK mismatch for %s: got %s want %s", company.Ticker, paddedCIK(entry.CIK), company.CIK)
		}
		title := strings.TrimSpace(entry.Title)
		if title == "" {
			return SECSnapshot{}, fmt.Errorf("SEC company tickers snapshot title missing for %s", company.Ticker)
		}
		matches[index].SourceName = title
	}
	digest := sha256.Sum256(content)
	sort.Slice(matches, func(left, right int) bool {
		return matches[left].Ticker < matches[right].Ticker
	})
	return SECSnapshot{
		ContentSHA256: hex.EncodeToString(digest[:]),
		SizeBytes:     len(content),
		AvailableAt:   availableAt.UTC(),
		Matches:       matches,
	}, nil
}

func WriteSECIdentitySeedSQL(writer io.Writer, tenantID, repositoryID string) error {
	return WriteSECIdentitySeedSQLWithSnapshot(writer, tenantID, repositoryID, nil)
}

func WriteSECIdentitySeedSQLWithSnapshot(writer io.Writer, tenantID, repositoryID string, snapshot *SECSnapshot) error {
	if !uuidLiteralPattern.MatchString(tenantID) {
		return fmt.Errorf("tenant ID must be a UUID")
	}
	if !uuidLiteralPattern.MatchString(repositoryID) {
		return fmt.Errorf("repository ID must be a UUID")
	}
	companies := MagnificentSevenSECCompanies()
	if snapshot != nil {
		if err := validateSECSnapshot(*snapshot); err != nil {
			return err
		}
		companies = append([]SECCompany(nil), snapshot.Matches...)
	}
	sort.Slice(companies, func(left, right int) bool {
		return companies[left].Ticker < companies[right].Ticker
	})
	line := func(format string, args ...any) error {
		_, err := fmt.Fprintf(writer, format+"\n", args...)
		return err
	}
	if err := line("BEGIN;"); err != nil {
		return err
	}
	if err := line(`
INSERT INTO forja.tenants (
    tenant_id, slug, display_name
) VALUES (
    %s::uuid, 'forja-alpha', 'Forja Alpha'
) ON CONFLICT (tenant_id) DO UPDATE SET
    slug=EXCLUDED.slug,
    display_name=EXCLUDED.display_name;

INSERT INTO forja.repositories (
    tenant_id, repository_id, canonical_name, default_branch
) VALUES (
    %s::uuid, %s::uuid, 'forja-alpha-financial-research', 'main'
) ON CONFLICT (tenant_id, repository_id) DO UPDATE SET
    canonical_name=EXCLUDED.canonical_name,
    default_branch=EXCLUDED.default_branch;`,
		sqlString(tenantID), sqlString(tenantID), sqlString(repositoryID)); err != nil {
		return err
	}
	if err := line(`
INSERT INTO forja.alpha_source_systems (
    tenant_id, repository_id, source_system_id, source_key, display_name,
    data_family, license_policy, adapter_version, acquisition_policy, status
) VALUES (
    %s::uuid, %s::uuid, %s, 'sec.edgar', 'SEC EDGAR',
    'sec', '{"redistribution":"public-source-with-attribution","access":"fair-access"}'::jsonb,
    'alpha-sec-identity-seed-v1', '{"mode":"versioned-public-snapshot"}'::jsonb, 'active'
) ON CONFLICT (tenant_id, repository_id, source_system_id) DO UPDATE SET
    source_key=EXCLUDED.source_key,
    display_name=EXCLUDED.display_name,
    data_family=EXCLUDED.data_family,
    license_policy=EXCLUDED.license_policy,
    adapter_version=EXCLUDED.adapter_version,
    acquisition_policy=EXCLUDED.acquisition_policy,
    status=EXCLUDED.status,
    updated_at=clock_timestamp();`,
		sqlString(tenantID), sqlString(repositoryID), sqlString(secSourceSystemID)); err != nil {
		return err
	}
	if snapshot != nil {
		if err := writeSnapshotLineageSQL(writer, tenantID, repositoryID, *snapshot); err != nil {
			return err
		}
	}
	for _, company := range companies {
		if err := writeCompanySeedSQL(writer, tenantID, repositoryID, company); err != nil {
			return err
		}
	}
	return line("COMMIT;")
}

func validateSECSnapshot(snapshot SECSnapshot) error {
	if len(snapshot.ContentSHA256) != 64 {
		return fmt.Errorf("SEC snapshot content SHA-256 must be hex encoded")
	}
	if _, err := hex.DecodeString(snapshot.ContentSHA256); err != nil {
		return fmt.Errorf("SEC snapshot content SHA-256 must be valid hex: %w", err)
	}
	if snapshot.SizeBytes <= 0 {
		return fmt.Errorf("SEC snapshot size must be positive")
	}
	if snapshot.AvailableAt.IsZero() {
		return fmt.Errorf("SEC snapshot availability time is required")
	}
	if len(snapshot.Matches) != 7 {
		return fmt.Errorf("SEC snapshot matches = %d, want 7", len(snapshot.Matches))
	}
	expected := MagnificentSevenSECCompanies()
	expectedByTicker := make(map[string]SECCompany, len(expected))
	for _, company := range expected {
		expectedByTicker[company.Ticker] = company
	}
	seen := make(map[string]bool, len(snapshot.Matches))
	for _, company := range snapshot.Matches {
		expectedCompany, ok := expectedByTicker[company.Ticker]
		if !ok {
			return fmt.Errorf("SEC snapshot contains out-of-universe ticker %s", company.Ticker)
		}
		if seen[company.Ticker] {
			return fmt.Errorf("SEC snapshot contains duplicate ticker %s", company.Ticker)
		}
		seen[company.Ticker] = true
		if company.CIK != expectedCompany.CIK {
			return fmt.Errorf("SEC snapshot CIK mismatch for %s: got %s want %s", company.Ticker, company.CIK, expectedCompany.CIK)
		}
		if strings.TrimSpace(company.SourceName) == "" {
			return fmt.Errorf("SEC snapshot source name missing for %s", company.Ticker)
		}
	}
	return nil
}

func writeSnapshotLineageSQL(writer io.Writer, tenantID, repositoryID string, snapshot SECSnapshot) error {
	ingestionRunID := "alpha_ingest_" + snapshot.ContentSHA256[:32]
	sourceObjectID := "alpha_object_" + snapshot.ContentSHA256[:32]
	sourceURLDigest := sha256.Sum256([]byte(secTickersURL))
	availableAt := snapshot.AvailableAt.UTC().Format(time.RFC3339)
	metadata := map[string]any{
		"source":            secTickersURL,
		"expected_universe": []string{"AAPL", "AMZN", "GOOGL", "META", "MSFT", "NVDA", "TSLA"},
		"matched_issuers":   len(snapshot.Matches),
		"source_limits":     "SEC company_tickers.json is a discovery snapshot; canonical filing facts still come from SEC submissions, filing archives, and Company Facts lineage.",
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
    '{"source":"sec.company_tickers","universe":"magnificent-seven"}'::jsonb,
    'succeeded', 'alpha-sec-identity-snapshot-v1',
    %s::timestamptz, %s::timestamptz,
    '{"mode":"snapshot","endpoint":"company_tickers"}'::jsonb, 1, %d
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
		sqlString(availableAt), sqlString(availableAt), len(snapshot.Matches),
		sqlString(tenantID), sqlString(repositoryID), sqlString(sourceObjectID), sqlString(secSourceSystemID),
		sqlString(ingestionRunID), sqlString("alpha/sec/company_tickers/"+snapshot.ContentSHA256+".json"),
		sqlString(snapshot.ContentSHA256), snapshot.SizeBytes, sqlString(hex.EncodeToString(sourceURLDigest[:])),
		sqlString(availableAt), sqlString(availableAt), sqlString(availableAt), sqlString(string(metadataJSON)))
	return err
}

func writeCompanySeedSQL(writer io.Writer, tenantID, repositoryID string, company SECCompany) error {
	line := func(format string, args ...any) error {
		_, err := fmt.Fprintf(writer, format+"\n", args...)
		return err
	}
	if err := line(`
INSERT INTO forja.alpha_issuers (
    tenant_id, repository_id, issuer_id, canonical_name, jurisdiction,
    lifecycle, valid_from
) VALUES (
    %s::uuid, %s::uuid, %s, %s, 'US', 'active', %s::timestamptz
) ON CONFLICT (tenant_id, repository_id, issuer_id) DO UPDATE SET
    canonical_name=EXCLUDED.canonical_name,
    jurisdiction=EXCLUDED.jurisdiction,
    lifecycle=EXCLUDED.lifecycle,
    valid_from=EXCLUDED.valid_from,
    updated_at=clock_timestamp();`,
		sqlString(tenantID), sqlString(repositoryID), sqlString(company.IssuerID), sqlString(company.Name), sqlString(seedValidFrom)); err != nil {
		return err
	}
	if err := line(`
INSERT INTO forja.alpha_securities (
    tenant_id, repository_id, security_id, issuer_id, security_type,
    share_class, exchange, currency, lifecycle, valid_from
) VALUES (
    %s::uuid, %s::uuid, %s, %s, 'common_stock',
    'common', %s, %s, 'active', %s::timestamptz
) ON CONFLICT (tenant_id, repository_id, security_id) DO UPDATE SET
    issuer_id=EXCLUDED.issuer_id,
    security_type=EXCLUDED.security_type,
    share_class=EXCLUDED.share_class,
    exchange=EXCLUDED.exchange,
    currency=EXCLUDED.currency,
    lifecycle=EXCLUDED.lifecycle,
    valid_from=EXCLUDED.valid_from;`,
		sqlString(tenantID), sqlString(repositoryID), sqlString(company.SecurityID), sqlString(company.IssuerID), sqlString(company.Exchange), sqlString(company.Currency), sqlString(seedValidFrom)); err != nil {
		return err
	}
	if err := writeIdentifierSeedSQL(writer, tenantID, repositoryID, company, "issuer", company.IssuerID, "cik", company.CIK, "canonical"); err != nil {
		return err
	}
	return writeIdentifierSeedSQL(writer, tenantID, repositoryID, company, "security", company.SecurityID, "ticker", company.Ticker, "reviewed")
}

func writeIdentifierSeedSQL(writer io.Writer, tenantID, repositoryID string, company SECCompany, entityKind, entityID, identifierType, identifierValue, authorityClass string) error {
	_, err := fmt.Fprintf(writer, `
INSERT INTO forja.alpha_identifiers (
    tenant_id, repository_id, identifier_id, entity_kind, entity_id,
    identifier_type, identifier_value, source_system_id, authority_class, valid_from
) VALUES (
    %s::uuid, %s::uuid, %s, %s, %s,
    %s, %s, %s, %s, %s::timestamptz
) ON CONFLICT (tenant_id, repository_id, identifier_id) DO UPDATE SET
    entity_kind=EXCLUDED.entity_kind,
    entity_id=EXCLUDED.entity_id,
    identifier_type=EXCLUDED.identifier_type,
    identifier_value=EXCLUDED.identifier_value,
    source_system_id=EXCLUDED.source_system_id,
    authority_class=EXCLUDED.authority_class,
    valid_from=EXCLUDED.valid_from;
`,
		sqlString(tenantID), sqlString(repositoryID), sqlString(identifierID(company.Ticker, identifierType, identifierValue)),
		sqlString(entityKind), sqlString(entityID), sqlString(identifierType), sqlString(identifierValue),
		sqlString(secSourceSystemID), sqlString(authorityClass), sqlString(seedValidFrom))
	return err
}

func identifierID(ticker, identifierType, identifierValue string) string {
	digest := sha256.Sum256([]byte(strings.ToUpper(ticker) + ":" + identifierType + ":" + strings.ToUpper(identifierValue)))
	return "alpha_identifier_" + hex.EncodeToString(digest[:16])
}

func normalizeCompanyLookup(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "000")
	value = strings.ReplaceAll(value, ".", "")
	value = strings.ReplaceAll(value, ",", "")
	return strings.Join(strings.Fields(value), " ")
}

func paddedCIK(value int) string {
	return fmt.Sprintf("%010d", value)
}

func sqlString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
