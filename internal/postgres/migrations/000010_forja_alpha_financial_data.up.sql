CREATE TABLE forja.alpha_source_systems (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    source_system_id text NOT NULL CHECK (source_system_id ~ '^alpha_source_[a-z0-9][a-z0-9_-]{1,119}$'),
    source_key text NOT NULL CHECK (source_key ~ '^[a-z][a-z0-9_.-]{1,119}$'),
    display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 200),
    data_family text NOT NULL CHECK (data_family IN ('sec', 'treasury', 'fred_alfred', 'market', 'internal')),
    license_policy jsonb NOT NULL CHECK (jsonb_typeof(license_policy)='object'),
    adapter_version text NOT NULL CHECK (char_length(adapter_version) BETWEEN 1 AND 160),
    acquisition_policy jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(acquisition_policy)='object'),
    status text NOT NULL CHECK (status IN ('active', 'disabled', 'quarantined')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, repository_id, source_system_id),
    UNIQUE (tenant_id, repository_id, source_key),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT,
    CHECK (updated_at >= created_at)
);

CREATE TABLE forja.alpha_ingestion_runs (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    ingestion_run_id text NOT NULL CHECK (ingestion_run_id ~ '^alpha_ingest_[a-f0-9]{32,64}$'),
    source_system_id text NOT NULL,
    scope jsonb NOT NULL CHECK (jsonb_typeof(scope)='object'),
    state text NOT NULL CHECK (state IN ('planned', 'running', 'succeeded', 'failed', 'quarantined')),
    code_version text NOT NULL CHECK (char_length(code_version) BETWEEN 1 AND 160),
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    watermark jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(watermark)='object'),
    object_count integer NOT NULL DEFAULT 0 CHECK (object_count >= 0),
    row_count integer NOT NULL DEFAULT 0 CHECK (row_count >= 0),
    failure_class text,
    failure_digest text,
    PRIMARY KEY (tenant_id, repository_id, ingestion_run_id),
    FOREIGN KEY (tenant_id, repository_id, source_system_id)
        REFERENCES forja.alpha_source_systems(tenant_id, repository_id, source_system_id)
        ON DELETE RESTRICT,
    CHECK (finished_at IS NULL OR finished_at >= started_at),
    CHECK ((state IN ('succeeded', 'failed', 'quarantined')) = (finished_at IS NOT NULL))
);

CREATE TABLE forja.alpha_source_objects (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    source_object_id text NOT NULL CHECK (source_object_id ~ '^alpha_object_[a-f0-9]{32,64}$'),
    source_system_id text NOT NULL,
    ingestion_run_id text NOT NULL,
    object_key text NOT NULL CHECK (
        char_length(object_key) BETWEEN 1 AND 700
        AND object_key !~ '(^/|/$|(^|/)\\.\\.?(?:/|$)|//|\\\\)'
    ),
    content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256)=32),
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    media_type text NOT NULL CHECK (char_length(media_type) BETWEEN 1 AND 160),
    source_uri_fingerprint text NOT NULL CHECK (char_length(source_uri_fingerprint) BETWEEN 1 AND 200),
    published_at timestamptz,
    available_at timestamptz NOT NULL,
    ingested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    lifecycle text NOT NULL CHECK (lifecycle IN ('active', 'superseded', 'quarantined', 'tombstoned')),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata)='object'),
    PRIMARY KEY (tenant_id, repository_id, source_object_id),
    UNIQUE (tenant_id, repository_id, content_sha256),
    FOREIGN KEY (tenant_id, repository_id, source_system_id)
        REFERENCES forja.alpha_source_systems(tenant_id, repository_id, source_system_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, ingestion_run_id)
        REFERENCES forja.alpha_ingestion_runs(tenant_id, repository_id, ingestion_run_id)
        ON DELETE RESTRICT,
    CHECK (available_at <= ingested_at)
);

CREATE TABLE forja.alpha_quality_findings (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    quality_finding_id text NOT NULL CHECK (quality_finding_id ~ '^alpha_quality_[a-f0-9]{32,64}$'),
    source_system_id text NOT NULL,
    entity_kind text NOT NULL CHECK (entity_kind IN (
        'source_object', 'issuer', 'security', 'identifier', 'filing', 'document',
        'taxonomy', 'concept', 'context', 'fact', 'metric', 'series', 'holding',
        'analysis', 'claim'
    )),
    entity_id text NOT NULL CHECK (char_length(entity_id) BETWEEN 1 AND 240),
    severity text NOT NULL CHECK (severity IN ('info', 'warning', 'error', 'critical')),
    finding_type text NOT NULL CHECK (finding_type ~ '^[a-z][a-z0-9_.-]{1,119}$'),
    quarantined boolean NOT NULL DEFAULT false,
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(evidence)='object'),
    resolved_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, repository_id, quality_finding_id),
    FOREIGN KEY (tenant_id, repository_id, source_system_id)
        REFERENCES forja.alpha_source_systems(tenant_id, repository_id, source_system_id)
        ON DELETE RESTRICT,
    CHECK ((quarantined=false) OR severity IN ('error', 'critical'))
);

CREATE TABLE forja.alpha_issuers (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    issuer_id text NOT NULL CHECK (issuer_id ~ '^alpha_issuer_[a-z0-9][a-z0-9_-]{1,119}$'),
    canonical_name text NOT NULL CHECK (char_length(canonical_name) BETWEEN 1 AND 240),
    jurisdiction text NOT NULL CHECK (char_length(jurisdiction) BETWEEN 2 AND 80),
    lifecycle text NOT NULL CHECK (lifecycle IN ('active', 'inactive', 'merged', 'quarantined')),
    valid_from timestamptz NOT NULL,
    valid_to timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, repository_id, issuer_id),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT,
    CHECK (valid_to IS NULL OR valid_to > valid_from),
    CHECK (updated_at >= created_at)
);

CREATE TABLE forja.alpha_securities (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    security_id text NOT NULL CHECK (security_id ~ '^alpha_security_[a-z0-9][a-z0-9_-]{1,119}$'),
    issuer_id text NOT NULL,
    security_type text NOT NULL CHECK (security_type IN ('common_stock', 'preferred_stock', 'adr', 'etf', 'index', 'other')),
    share_class text,
    exchange text,
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    lifecycle text NOT NULL CHECK (lifecycle IN ('active', 'inactive', 'delisted', 'quarantined')),
    valid_from timestamptz NOT NULL,
    valid_to timestamptz,
    PRIMARY KEY (tenant_id, repository_id, security_id),
    FOREIGN KEY (tenant_id, repository_id, issuer_id)
        REFERENCES forja.alpha_issuers(tenant_id, repository_id, issuer_id)
        ON DELETE RESTRICT,
    CHECK (valid_to IS NULL OR valid_to > valid_from)
);

CREATE TABLE forja.alpha_identifiers (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    identifier_id text NOT NULL CHECK (identifier_id ~ '^alpha_identifier_[a-f0-9]{32,64}$'),
    entity_kind text NOT NULL CHECK (entity_kind IN ('issuer', 'security', 'manager')),
    entity_id text NOT NULL CHECK (char_length(entity_id) BETWEEN 1 AND 160),
    identifier_type text NOT NULL CHECK (identifier_type IN ('cik', 'ticker', 'cusip', 'figi', 'lei', 'source_native')),
    identifier_value text NOT NULL CHECK (char_length(identifier_value) BETWEEN 1 AND 120),
    source_system_id text NOT NULL,
    authority_class text NOT NULL CHECK (authority_class IN ('canonical', 'reviewed', 'source_reported', 'candidate')),
    valid_from timestamptz NOT NULL,
    valid_to timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, repository_id, identifier_id),
    UNIQUE (tenant_id, repository_id, identifier_type, identifier_value, valid_from),
    FOREIGN KEY (tenant_id, repository_id, source_system_id)
        REFERENCES forja.alpha_source_systems(tenant_id, repository_id, source_system_id)
        ON DELETE RESTRICT,
    CHECK (valid_to IS NULL OR valid_to > valid_from)
);

CREATE TABLE forja.alpha_corporate_actions (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    corporate_action_id text NOT NULL CHECK (corporate_action_id ~ '^alpha_action_[a-f0-9]{32,64}$'),
    security_id text NOT NULL,
    action_type text NOT NULL CHECK (action_type IN ('split', 'dividend', 'symbol_change', 'share_class_change', 'merger', 'spinoff')),
    observed_at timestamptz NOT NULL,
    available_at timestamptz NOT NULL,
    terms jsonb NOT NULL CHECK (jsonb_typeof(terms)='object'),
    source_object_id text NOT NULL,
    PRIMARY KEY (tenant_id, repository_id, corporate_action_id),
    FOREIGN KEY (tenant_id, repository_id, security_id)
        REFERENCES forja.alpha_securities(tenant_id, repository_id, security_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, source_object_id)
        REFERENCES forja.alpha_source_objects(tenant_id, repository_id, source_object_id)
        ON DELETE RESTRICT
);

CREATE TABLE forja.alpha_filings (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    filing_id text NOT NULL CHECK (filing_id ~ '^alpha_filing_[a-f0-9]{32,64}$'),
    issuer_id text NOT NULL,
    source_system_id text NOT NULL,
    source_object_id text NOT NULL,
    accession_number text NOT NULL CHECK (accession_number ~ '^[0-9]{10}-[0-9]{2}-[0-9]{6}$'),
    form_type text NOT NULL CHECK (form_type IN ('10-K', '10-K/A', '10-Q', '10-Q/A', '13F-HR', '13F-HR/A', '8-K', 'other')),
    fiscal_year integer CHECK (fiscal_year BETWEEN 1900 AND 2200),
    fiscal_period text CHECK (fiscal_period IS NULL OR fiscal_period IN ('FY', 'Q1', 'Q2', 'Q3', 'Q4')),
    period_start date,
    period_end date,
    filed_at timestamptz NOT NULL,
    available_at timestamptz NOT NULL,
    amendment_of_filing_id text,
    lifecycle text NOT NULL CHECK (lifecycle IN ('active', 'amended', 'superseded', 'quarantined')),
    PRIMARY KEY (tenant_id, repository_id, filing_id),
    UNIQUE (tenant_id, repository_id, accession_number),
    FOREIGN KEY (tenant_id, repository_id, issuer_id)
        REFERENCES forja.alpha_issuers(tenant_id, repository_id, issuer_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, source_system_id)
        REFERENCES forja.alpha_source_systems(tenant_id, repository_id, source_system_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, source_object_id)
        REFERENCES forja.alpha_source_objects(tenant_id, repository_id, source_object_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, amendment_of_filing_id)
        REFERENCES forja.alpha_filings(tenant_id, repository_id, filing_id)
        ON DELETE RESTRICT,
    CHECK (period_start IS NULL OR period_end IS NULL OR period_end >= period_start),
    CHECK (available_at >= filed_at)
);

CREATE TABLE forja.alpha_filing_documents (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    filing_document_id text NOT NULL CHECK (filing_document_id ~ '^alpha_document_[a-f0-9]{32,64}$'),
    filing_id text NOT NULL,
    source_object_id text NOT NULL,
    sequence_number integer NOT NULL CHECK (sequence_number >= 0),
    document_role text NOT NULL CHECK (document_role IN ('primary', 'xbrl_instance', 'exhibit', 'cover', 'other')),
    document_name text NOT NULL CHECK (char_length(document_name) BETWEEN 1 AND 300),
    media_type text NOT NULL CHECK (char_length(media_type) BETWEEN 1 AND 160),
    PRIMARY KEY (tenant_id, repository_id, filing_document_id),
    UNIQUE (tenant_id, repository_id, filing_id, sequence_number),
    FOREIGN KEY (tenant_id, repository_id, filing_id)
        REFERENCES forja.alpha_filings(tenant_id, repository_id, filing_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, source_object_id)
        REFERENCES forja.alpha_source_objects(tenant_id, repository_id, source_object_id)
        ON DELETE RESTRICT
);

CREATE TABLE forja.alpha_taxonomies (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    taxonomy_id text NOT NULL CHECK (taxonomy_id ~ '^alpha_taxonomy_[a-f0-9]{32,64}$'),
    namespace_uri text NOT NULL CHECK (char_length(namespace_uri) BETWEEN 1 AND 400),
    taxonomy_version text NOT NULL CHECK (char_length(taxonomy_version) BETWEEN 1 AND 120),
    authority text NOT NULL CHECK (authority IN ('us-gaap', 'ifrs', 'sec', 'issuer-extension', 'other')),
    source_object_id text,
    PRIMARY KEY (tenant_id, repository_id, taxonomy_id),
    UNIQUE (tenant_id, repository_id, namespace_uri, taxonomy_version),
    FOREIGN KEY (tenant_id, repository_id, source_object_id)
        REFERENCES forja.alpha_source_objects(tenant_id, repository_id, source_object_id)
        ON DELETE RESTRICT
);

CREATE TABLE forja.alpha_xbrl_concepts (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    concept_id text NOT NULL CHECK (concept_id ~ '^alpha_concept_[a-f0-9]{32,64}$'),
    taxonomy_id text NOT NULL,
    qualified_name text NOT NULL CHECK (char_length(qualified_name) BETWEEN 1 AND 300),
    data_type text NOT NULL CHECK (char_length(data_type) BETWEEN 1 AND 160),
    balance text CHECK (balance IS NULL OR balance IN ('debit', 'credit')),
    period_type text NOT NULL CHECK (period_type IN ('instant', 'duration')),
    standard_class text NOT NULL CHECK (standard_class IN ('standard', 'extension', 'unknown')),
    PRIMARY KEY (tenant_id, repository_id, concept_id),
    UNIQUE (tenant_id, repository_id, taxonomy_id, qualified_name),
    FOREIGN KEY (tenant_id, repository_id, taxonomy_id)
        REFERENCES forja.alpha_taxonomies(tenant_id, repository_id, taxonomy_id)
        ON DELETE RESTRICT
);

CREATE TABLE forja.alpha_xbrl_contexts (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    context_id text NOT NULL CHECK (context_id ~ '^alpha_context_[a-f0-9]{32,64}$'),
    filing_id text NOT NULL,
    entity_identifier text NOT NULL CHECK (char_length(entity_identifier) BETWEEN 1 AND 160),
    period_start date,
    period_end date NOT NULL,
    instant date,
    dimensions jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(dimensions)='object'),
    context_sha256 bytea NOT NULL CHECK (octet_length(context_sha256)=32),
    PRIMARY KEY (tenant_id, repository_id, context_id),
    UNIQUE (tenant_id, repository_id, filing_id, context_sha256),
    FOREIGN KEY (tenant_id, repository_id, filing_id)
        REFERENCES forja.alpha_filings(tenant_id, repository_id, filing_id)
        ON DELETE RESTRICT,
    CHECK (
        (instant IS NOT NULL AND period_start IS NULL)
        OR (instant IS NULL AND period_start IS NOT NULL AND period_end >= period_start)
    )
);

CREATE TABLE forja.alpha_xbrl_facts (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    fact_id text NOT NULL CHECK (fact_id ~ '^alpha_fact_[a-f0-9]{32,64}$'),
    filing_id text NOT NULL,
    concept_id text NOT NULL,
    context_id text NOT NULL,
    source_object_id text NOT NULL,
    unit text,
    decimals text,
    lexical_value text NOT NULL CHECK (char_length(lexical_value) BETWEEN 1 AND 2000),
    numeric_value numeric,
    currency text CHECK (currency IS NULL OR currency ~ '^[A-Z]{3}$'),
    scale integer NOT NULL DEFAULT 0 CHECK (scale BETWEEN -18 AND 18),
    quality_state text NOT NULL CHECK (quality_state IN ('accepted', 'quarantined', 'superseded')),
    PRIMARY KEY (tenant_id, repository_id, fact_id),
    UNIQUE (tenant_id, repository_id, filing_id, concept_id, context_id, unit, lexical_value),
    FOREIGN KEY (tenant_id, repository_id, filing_id)
        REFERENCES forja.alpha_filings(tenant_id, repository_id, filing_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, concept_id)
        REFERENCES forja.alpha_xbrl_concepts(tenant_id, repository_id, concept_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, context_id)
        REFERENCES forja.alpha_xbrl_contexts(tenant_id, repository_id, context_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, source_object_id)
        REFERENCES forja.alpha_source_objects(tenant_id, repository_id, source_object_id)
        ON DELETE RESTRICT
);

CREATE TABLE forja.alpha_metric_definitions (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    metric_id text NOT NULL CHECK (metric_id ~ '^alpha_metric_[a-z0-9][a-z0-9_.-]{1,119}$'),
    metric_key text NOT NULL CHECK (metric_key ~ '^[a-z][a-z0-9_.-]{1,119}$'),
    display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 200),
    metric_kind text NOT NULL CHECK (metric_kind IN ('reported', 'derived', 'statistical')),
    unit_policy jsonb NOT NULL CHECK (jsonb_typeof(unit_policy)='object'),
    formula_semantics jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(formula_semantics)='object'),
    version text NOT NULL CHECK (char_length(version) BETWEEN 1 AND 80),
    status text NOT NULL CHECK (status IN ('active', 'retired')),
    PRIMARY KEY (tenant_id, repository_id, metric_id),
    UNIQUE (tenant_id, repository_id, metric_key, version)
);

CREATE TABLE forja.alpha_metric_mappings (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    metric_mapping_id text NOT NULL CHECK (metric_mapping_id ~ '^alpha_mapping_[a-f0-9]{32,64}$'),
    metric_id text NOT NULL,
    concept_id text NOT NULL,
    issuer_id text,
    context_filter jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(context_filter)='object'),
    confidence_class text NOT NULL CHECK (confidence_class IN ('reviewed', 'candidate', 'rejected')),
    valid_from timestamptz NOT NULL,
    valid_to timestamptz,
    reviewed_by text,
    PRIMARY KEY (tenant_id, repository_id, metric_mapping_id),
    FOREIGN KEY (tenant_id, repository_id, metric_id)
        REFERENCES forja.alpha_metric_definitions(tenant_id, repository_id, metric_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, concept_id)
        REFERENCES forja.alpha_xbrl_concepts(tenant_id, repository_id, concept_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, issuer_id)
        REFERENCES forja.alpha_issuers(tenant_id, repository_id, issuer_id)
        ON DELETE RESTRICT,
    CHECK (valid_to IS NULL OR valid_to > valid_from),
    CHECK ((confidence_class='reviewed') = (reviewed_by IS NOT NULL))
);

CREATE TABLE forja.alpha_metric_observations (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    metric_observation_id text NOT NULL CHECK (metric_observation_id ~ '^alpha_metric_obs_[a-f0-9]{32,64}$'),
    metric_id text NOT NULL,
    issuer_id text NOT NULL,
    security_id text,
    filing_id text,
    source_fact_id text,
    observation_kind text NOT NULL CHECK (observation_kind IN ('reported', 'derived')),
    observed_at timestamptz NOT NULL,
    period_start date,
    period_end date,
    available_at timestamptz NOT NULL,
    value_numeric numeric NOT NULL,
    unit text NOT NULL CHECK (char_length(unit) BETWEEN 1 AND 80),
    currency text CHECK (currency IS NULL OR currency ~ '^[A-Z]{3}$'),
    lineage jsonb NOT NULL CHECK (jsonb_typeof(lineage)='object'),
    quality_state text NOT NULL CHECK (quality_state IN ('accepted', 'quarantined', 'superseded')),
    PRIMARY KEY (tenant_id, repository_id, metric_observation_id),
    FOREIGN KEY (tenant_id, repository_id, metric_id)
        REFERENCES forja.alpha_metric_definitions(tenant_id, repository_id, metric_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, issuer_id)
        REFERENCES forja.alpha_issuers(tenant_id, repository_id, issuer_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, security_id)
        REFERENCES forja.alpha_securities(tenant_id, repository_id, security_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, filing_id)
        REFERENCES forja.alpha_filings(tenant_id, repository_id, filing_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, source_fact_id)
        REFERENCES forja.alpha_xbrl_facts(tenant_id, repository_id, fact_id)
        ON DELETE RESTRICT,
    CHECK (period_start IS NULL OR period_end IS NULL OR period_end >= period_start)
);

CREATE TABLE forja.alpha_series (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    series_id text NOT NULL CHECK (series_id ~ '^alpha_series_[a-z0-9][a-z0-9_.-]{1,119}$'),
    source_system_id text NOT NULL,
    provider_series_id text NOT NULL CHECK (char_length(provider_series_id) BETWEEN 1 AND 160),
    frequency text NOT NULL CHECK (frequency IN ('daily', 'weekly', 'monthly', 'quarterly', 'annual', 'irregular')),
    unit text NOT NULL CHECK (char_length(unit) BETWEEN 1 AND 80),
    adjustment_policy text NOT NULL CHECK (char_length(adjustment_policy) BETWEEN 1 AND 160),
    timezone text NOT NULL CHECK (char_length(timezone) BETWEEN 1 AND 80),
    license_policy jsonb NOT NULL CHECK (jsonb_typeof(license_policy)='object'),
    status text NOT NULL CHECK (status IN ('active', 'disabled', 'quarantined')),
    PRIMARY KEY (tenant_id, repository_id, series_id),
    UNIQUE (tenant_id, repository_id, source_system_id, provider_series_id),
    FOREIGN KEY (tenant_id, repository_id, source_system_id)
        REFERENCES forja.alpha_source_systems(tenant_id, repository_id, source_system_id)
        ON DELETE RESTRICT
);

CREATE TABLE forja.alpha_series_observations (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    series_observation_id text NOT NULL CHECK (series_observation_id ~ '^alpha_series_obs_[a-f0-9]{32,64}$'),
    series_id text NOT NULL,
    observed_at timestamptz NOT NULL,
    published_at timestamptz,
    available_at timestamptz NOT NULL,
    vintage_at timestamptz,
    value_numeric numeric NOT NULL,
    source_object_id text NOT NULL,
    quality_state text NOT NULL CHECK (quality_state IN ('accepted', 'quarantined', 'superseded')),
    PRIMARY KEY (tenant_id, repository_id, series_observation_id),
    FOREIGN KEY (tenant_id, repository_id, series_id)
        REFERENCES forja.alpha_series(tenant_id, repository_id, series_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, source_object_id)
        REFERENCES forja.alpha_source_objects(tenant_id, repository_id, source_object_id)
        ON DELETE RESTRICT
);

CREATE TABLE forja.alpha_analysis_specs (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    analysis_spec_id text NOT NULL CHECK (analysis_spec_id ~ '^alpha_analysis_spec_[a-f0-9]{32,64}$'),
    universe jsonb NOT NULL CHECK (jsonb_typeof(universe)='array'),
    as_of timestamptz NOT NULL,
    window_start timestamptz NOT NULL,
    window_end timestamptz NOT NULL,
    estimator text NOT NULL CHECK (estimator IN ('ols', 'ridge', 'descriptive', 'comparison')),
    missing_data_policy text NOT NULL CHECK (missing_data_policy IN ('fail_closed', 'drop_pairwise', 'explicit_gap')),
    input_hash bytea NOT NULL CHECK (octet_length(input_hash)=32),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, repository_id, analysis_spec_id),
    CHECK (window_end >= window_start),
    CHECK (as_of >= window_end)
);

CREATE TABLE forja.alpha_analysis_runs (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    analysis_run_id text NOT NULL CHECK (analysis_run_id ~ '^alpha_analysis_run_[a-f0-9]{32,64}$'),
    analysis_spec_id text NOT NULL,
    state text NOT NULL CHECK (state IN ('planned', 'running', 'succeeded', 'failed', 'quarantined')),
    code_version text NOT NULL CHECK (char_length(code_version) BETWEEN 1 AND 160),
    model_version text,
    diagnostics jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(diagnostics)='object'),
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    PRIMARY KEY (tenant_id, repository_id, analysis_run_id),
    FOREIGN KEY (tenant_id, repository_id, analysis_spec_id)
        REFERENCES forja.alpha_analysis_specs(tenant_id, repository_id, analysis_spec_id)
        ON DELETE RESTRICT,
    CHECK (finished_at IS NULL OR finished_at >= started_at),
    CHECK ((state IN ('succeeded', 'failed', 'quarantined')) = (finished_at IS NOT NULL))
);

CREATE TABLE forja.alpha_analysis_results (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    analysis_result_id text NOT NULL CHECK (analysis_result_id ~ '^alpha_analysis_result_[a-f0-9]{32,64}$'),
    analysis_run_id text NOT NULL,
    result_kind text NOT NULL CHECK (result_kind IN ('coefficient', 'diagnostic', 'table', 'plot', 'memo_input')),
    result_payload jsonb NOT NULL CHECK (jsonb_typeof(result_payload)='object'),
    output_artifact_id text,
    output_content_sha256 bytea CHECK (output_content_sha256 IS NULL OR octet_length(output_content_sha256)=32),
    PRIMARY KEY (tenant_id, repository_id, analysis_result_id),
    FOREIGN KEY (tenant_id, repository_id, analysis_run_id)
        REFERENCES forja.alpha_analysis_runs(tenant_id, repository_id, analysis_run_id)
        ON DELETE RESTRICT
);

CREATE TABLE forja.alpha_managers (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    manager_id text NOT NULL CHECK (manager_id ~ '^alpha_manager_[a-z0-9][a-z0-9_-]{1,119}$'),
    canonical_name text NOT NULL CHECK (char_length(canonical_name) BETWEEN 1 AND 240),
    manager_class text NOT NULL CHECK (manager_class IN ('hedge_fund', 'asset_manager', 'pension', 'corporate', 'other', 'unknown')),
    lifecycle text NOT NULL CHECK (lifecycle IN ('active', 'inactive', 'quarantined')),
    PRIMARY KEY (tenant_id, repository_id, manager_id)
);

CREATE TABLE forja.alpha_holdings_reports (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    holdings_report_id text NOT NULL CHECK (holdings_report_id ~ '^alpha_holdings_report_[a-f0-9]{32,64}$'),
    manager_id text NOT NULL,
    filing_id text NOT NULL,
    report_period date NOT NULL,
    filed_at timestamptz NOT NULL,
    available_at timestamptz NOT NULL,
    amendment_of_report_id text,
    lifecycle text NOT NULL CHECK (lifecycle IN ('active', 'amended', 'superseded', 'quarantined')),
    PRIMARY KEY (tenant_id, repository_id, holdings_report_id),
    FOREIGN KEY (tenant_id, repository_id, manager_id)
        REFERENCES forja.alpha_managers(tenant_id, repository_id, manager_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, filing_id)
        REFERENCES forja.alpha_filings(tenant_id, repository_id, filing_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, amendment_of_report_id)
        REFERENCES forja.alpha_holdings_reports(tenant_id, repository_id, holdings_report_id)
        ON DELETE RESTRICT,
    CHECK (available_at >= filed_at)
);

CREATE TABLE forja.alpha_holding_positions (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    holding_position_id text NOT NULL CHECK (holding_position_id ~ '^alpha_position_[a-f0-9]{32,64}$'),
    holdings_report_id text NOT NULL,
    as_filed_name text NOT NULL CHECK (char_length(as_filed_name) BETWEEN 1 AND 300),
    cusip text,
    value_usd_thousands numeric CHECK (value_usd_thousands IS NULL OR value_usd_thousands >= 0),
    shares numeric CHECK (shares IS NULL OR shares >= 0),
    discretion text,
    voting jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(voting)='object'),
    source_object_id text NOT NULL,
    PRIMARY KEY (tenant_id, repository_id, holding_position_id),
    FOREIGN KEY (tenant_id, repository_id, holdings_report_id)
        REFERENCES forja.alpha_holdings_reports(tenant_id, repository_id, holdings_report_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, source_object_id)
        REFERENCES forja.alpha_source_objects(tenant_id, repository_id, source_object_id)
        ON DELETE RESTRICT
);

CREATE TABLE forja.alpha_holding_resolutions (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    holding_resolution_id text NOT NULL CHECK (holding_resolution_id ~ '^alpha_resolution_[a-f0-9]{32,64}$'),
    holding_position_id text NOT NULL,
    security_id text,
    confidence_class text NOT NULL CHECK (confidence_class IN ('reviewed', 'candidate', 'unresolved', 'rejected')),
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(evidence)='object'),
    reviewed_by text,
    resolved_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, repository_id, holding_resolution_id),
    FOREIGN KEY (tenant_id, repository_id, holding_position_id)
        REFERENCES forja.alpha_holding_positions(tenant_id, repository_id, holding_position_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, security_id)
        REFERENCES forja.alpha_securities(tenant_id, repository_id, security_id)
        ON DELETE RESTRICT,
    CHECK ((confidence_class='reviewed') = (reviewed_by IS NOT NULL))
);

CREATE TABLE forja.alpha_research_sessions (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    research_session_id text NOT NULL CHECK (research_session_id ~ '^alpha_session_[a-f0-9]{32,64}$'),
    conversation_id text,
    as_of timestamptz NOT NULL,
    universe jsonb NOT NULL CHECK (jsonb_typeof(universe)='array'),
    policy_version text NOT NULL CHECK (char_length(policy_version) BETWEEN 1 AND 120),
    state text NOT NULL CHECK (state IN ('draft', 'planning', 'running', 'completed', 'failed', 'deleted')),
    created_by text NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 160),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, repository_id, research_session_id),
    FOREIGN KEY (tenant_id, repository_id, conversation_id)
        REFERENCES forja.conversations(tenant_id, repository_id, conversation_id)
        ON DELETE RESTRICT,
    CHECK (updated_at >= created_at)
);

CREATE TABLE forja.alpha_tool_invocations (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    tool_invocation_id text NOT NULL CHECK (tool_invocation_id ~ '^alpha_tool_[a-f0-9]{32,64}$'),
    research_session_id text NOT NULL,
    tool_name text NOT NULL CHECK (tool_name ~ '^[a-z][a-z0-9_.-]{1,119}$'),
    capability_id text NOT NULL CHECK (char_length(capability_id) BETWEEN 1 AND 160),
    request_sha256 bytea NOT NULL CHECK (octet_length(request_sha256)=32),
    input_refs jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(input_refs)='array'),
    result_state text NOT NULL CHECK (result_state IN ('planned', 'running', 'succeeded', 'failed', 'rejected')),
    result_sha256 bytea CHECK (result_sha256 IS NULL OR octet_length(result_sha256)=32),
    receipt jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(receipt)='object'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    PRIMARY KEY (tenant_id, repository_id, tool_invocation_id),
    FOREIGN KEY (tenant_id, repository_id, research_session_id)
        REFERENCES forja.alpha_research_sessions(tenant_id, repository_id, research_session_id)
        ON DELETE RESTRICT,
    CHECK (finished_at IS NULL OR finished_at >= created_at),
    CHECK ((result_state IN ('succeeded', 'failed', 'rejected')) = (finished_at IS NOT NULL))
);

CREATE TABLE forja.alpha_claims (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    claim_id text NOT NULL CHECK (claim_id ~ '^alpha_claim_[a-f0-9]{32,64}$'),
    research_session_id text NOT NULL,
    claim_class text NOT NULL CHECK (claim_class IN ('reported_fact', 'calculation', 'statistical_estimate', 'interpretation', 'gap', 'counterargument')),
    text_artifact_id text,
    text_offset_start integer CHECK (text_offset_start IS NULL OR text_offset_start >= 0),
    text_offset_end integer CHECK (text_offset_end IS NULL OR text_offset_end >= 0),
    verification_state text NOT NULL CHECK (verification_state IN ('pending', 'supported', 'unsupported', 'conflicted', 'gap')),
    risk_flags jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(risk_flags)='array'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, repository_id, claim_id),
    FOREIGN KEY (tenant_id, repository_id, research_session_id)
        REFERENCES forja.alpha_research_sessions(tenant_id, repository_id, research_session_id)
        ON DELETE RESTRICT,
    CHECK (
        text_offset_start IS NULL OR text_offset_end IS NULL
        OR text_offset_end >= text_offset_start
    )
);

CREATE TABLE forja.alpha_claim_evidence (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    claim_id text NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    evidence_kind text NOT NULL CHECK (evidence_kind IN ('fact', 'metric_observation', 'series_observation', 'analysis_result', 'filing_document', 'source_object', 'tool_invocation', 'gap')),
    evidence_id text NOT NULL CHECK (char_length(evidence_id) BETWEEN 1 AND 200),
    support_class text NOT NULL CHECK (support_class IN ('supports', 'contradicts', 'qualifies', 'gap')),
    evidence_sha256 bytea CHECK (evidence_sha256 IS NULL OR octet_length(evidence_sha256)=32),
    PRIMARY KEY (tenant_id, repository_id, claim_id, ordinal),
    FOREIGN KEY (tenant_id, repository_id, claim_id)
        REFERENCES forja.alpha_claims(tenant_id, repository_id, claim_id)
        ON DELETE RESTRICT
);

CREATE INDEX alpha_identifiers_lookup_idx
    ON forja.alpha_identifiers (
        tenant_id, repository_id, identifier_type, identifier_value, valid_from, valid_to
    );

CREATE INDEX alpha_filings_point_in_time_idx
    ON forja.alpha_filings (
        tenant_id, repository_id, issuer_id, form_type, available_at, period_end
    ) WHERE lifecycle IN ('active', 'amended');

CREATE INDEX alpha_metric_observations_point_in_time_idx
    ON forja.alpha_metric_observations (
        tenant_id, repository_id, issuer_id, metric_id, available_at, period_end
    ) WHERE quality_state='accepted';

CREATE INDEX alpha_series_observations_point_in_time_idx
    ON forja.alpha_series_observations (
        tenant_id, repository_id, series_id, available_at, observed_at
    ) WHERE quality_state='accepted';

CREATE UNIQUE INDEX alpha_series_observations_unique_vintage_idx
    ON forja.alpha_series_observations (
        tenant_id, repository_id, series_id, observed_at, COALESCE(vintage_at, available_at)
    );

CREATE INDEX alpha_quality_findings_quarantine_idx
    ON forja.alpha_quality_findings (
        tenant_id, repository_id, entity_kind, entity_id, created_at
    ) WHERE quarantined;
