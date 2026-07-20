CREATE VIEW forja.alpha_v_issuer_filing_timeline AS
SELECT
    f.tenant_id,
    f.repository_id,
    f.filing_id,
    f.issuer_id,
    i.canonical_name,
    cik.identifier_value AS cik,
    ticker.identifier_value AS ticker,
    f.accession_number,
    f.form_type,
    f.fiscal_year,
    f.fiscal_period,
    f.period_start,
    f.period_end,
    f.filed_at,
    f.available_at,
    f.lifecycle,
    f.source_system_id,
    f.source_object_id
FROM forja.alpha_filings AS f
JOIN forja.alpha_issuers AS i
  ON i.tenant_id = f.tenant_id
 AND i.repository_id = f.repository_id
 AND i.issuer_id = f.issuer_id
LEFT JOIN forja.alpha_identifiers AS cik
  ON cik.tenant_id = f.tenant_id
 AND cik.repository_id = f.repository_id
 AND cik.entity_kind = 'issuer'
 AND cik.entity_id = f.issuer_id
 AND cik.identifier_type = 'cik'
 AND cik.valid_from <= f.available_at
 AND (cik.valid_to IS NULL OR cik.valid_to > f.available_at)
LEFT JOIN forja.alpha_identifiers AS ticker
  ON ticker.tenant_id = f.tenant_id
 AND ticker.repository_id = f.repository_id
 AND ticker.entity_kind = 'issuer'
 AND ticker.entity_id = f.issuer_id
 AND ticker.identifier_type = 'ticker'
 AND ticker.valid_from <= f.available_at
 AND (ticker.valid_to IS NULL OR ticker.valid_to > f.available_at)
WHERE f.lifecycle <> 'quarantined';

COMMENT ON VIEW forja.alpha_v_issuer_filing_timeline IS
    'Point-in-time filing timeline. Research queries must filter available_at <= requested as_of timestamp.';

CREATE VIEW forja.alpha_v_reported_metric_panel AS
SELECT
    mo.tenant_id,
    mo.repository_id,
    mo.metric_observation_id,
    mo.metric_id,
    md.metric_key,
    md.display_name AS metric_name,
    mo.issuer_id,
    i.canonical_name,
    mo.security_id,
    mo.filing_id,
    f.accession_number,
    f.form_type,
    mo.source_fact_id,
    mo.observed_at,
    mo.period_start,
    mo.period_end,
    mo.available_at,
    mo.value_numeric,
    mo.unit,
    mo.currency,
    mo.lineage,
    mo.quality_state
FROM forja.alpha_metric_observations AS mo
JOIN forja.alpha_metric_definitions AS md
  ON md.tenant_id = mo.tenant_id
 AND md.repository_id = mo.repository_id
 AND md.metric_id = mo.metric_id
JOIN forja.alpha_issuers AS i
  ON i.tenant_id = mo.tenant_id
 AND i.repository_id = mo.repository_id
 AND i.issuer_id = mo.issuer_id
LEFT JOIN forja.alpha_filings AS f
  ON f.tenant_id = mo.tenant_id
 AND f.repository_id = mo.repository_id
 AND f.filing_id = mo.filing_id
WHERE mo.observation_kind = 'reported'
  AND mo.quality_state = 'accepted';

COMMENT ON VIEW forja.alpha_v_reported_metric_panel IS
    'Point-in-time reported metric panel. Research queries must filter available_at <= requested as_of timestamp.';

