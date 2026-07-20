package alpha

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

const (
	secSourceSystemID = "alpha_source_sec_edgar"
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

func WriteSECIdentitySeedSQL(writer io.Writer, tenantID, repositoryID string) error {
	if !uuidLiteralPattern.MatchString(tenantID) {
		return fmt.Errorf("tenant ID must be a UUID")
	}
	if !uuidLiteralPattern.MatchString(repositoryID) {
		return fmt.Errorf("repository ID must be a UUID")
	}
	companies := MagnificentSevenSECCompanies()
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
	for _, company := range companies {
		if err := writeCompanySeedSQL(writer, tenantID, repositoryID, company); err != nil {
			return err
		}
	}
	return line("COMMIT;")
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

func sqlString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
