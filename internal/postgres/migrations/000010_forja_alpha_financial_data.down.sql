LOCK TABLE
    forja.alpha_claim_evidence,
    forja.alpha_claims,
    forja.alpha_tool_invocations,
    forja.alpha_research_sessions,
    forja.alpha_holding_resolutions,
    forja.alpha_holding_positions,
    forja.alpha_holdings_reports,
    forja.alpha_managers,
    forja.alpha_analysis_results,
    forja.alpha_analysis_runs,
    forja.alpha_analysis_specs,
    forja.alpha_series_observations,
    forja.alpha_series,
    forja.alpha_metric_observations,
    forja.alpha_metric_mappings,
    forja.alpha_metric_definitions,
    forja.alpha_xbrl_facts,
    forja.alpha_xbrl_contexts,
    forja.alpha_xbrl_concepts,
    forja.alpha_taxonomies,
    forja.alpha_filing_documents,
    forja.alpha_filings,
    forja.alpha_corporate_actions,
    forja.alpha_identifiers,
    forja.alpha_securities,
    forja.alpha_issuers,
    forja.alpha_quality_findings,
    forja.alpha_source_objects,
    forja.alpha_ingestion_runs,
    forja.alpha_source_systems
IN ACCESS EXCLUSIVE MODE NOWAIT;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM forja.alpha_claim_evidence)
       OR EXISTS (SELECT 1 FROM forja.alpha_claims)
       OR EXISTS (SELECT 1 FROM forja.alpha_tool_invocations)
       OR EXISTS (SELECT 1 FROM forja.alpha_research_sessions)
       OR EXISTS (SELECT 1 FROM forja.alpha_holding_resolutions)
       OR EXISTS (SELECT 1 FROM forja.alpha_holding_positions)
       OR EXISTS (SELECT 1 FROM forja.alpha_holdings_reports)
       OR EXISTS (SELECT 1 FROM forja.alpha_managers)
       OR EXISTS (SELECT 1 FROM forja.alpha_analysis_results)
       OR EXISTS (SELECT 1 FROM forja.alpha_analysis_runs)
       OR EXISTS (SELECT 1 FROM forja.alpha_analysis_specs)
       OR EXISTS (SELECT 1 FROM forja.alpha_series_observations)
       OR EXISTS (SELECT 1 FROM forja.alpha_series)
       OR EXISTS (SELECT 1 FROM forja.alpha_metric_observations)
       OR EXISTS (SELECT 1 FROM forja.alpha_metric_mappings)
       OR EXISTS (SELECT 1 FROM forja.alpha_metric_definitions)
       OR EXISTS (SELECT 1 FROM forja.alpha_xbrl_facts)
       OR EXISTS (SELECT 1 FROM forja.alpha_xbrl_contexts)
       OR EXISTS (SELECT 1 FROM forja.alpha_xbrl_concepts)
       OR EXISTS (SELECT 1 FROM forja.alpha_taxonomies)
       OR EXISTS (SELECT 1 FROM forja.alpha_filing_documents)
       OR EXISTS (SELECT 1 FROM forja.alpha_filings)
       OR EXISTS (SELECT 1 FROM forja.alpha_corporate_actions)
       OR EXISTS (SELECT 1 FROM forja.alpha_identifiers)
       OR EXISTS (SELECT 1 FROM forja.alpha_securities)
       OR EXISTS (SELECT 1 FROM forja.alpha_issuers)
       OR EXISTS (SELECT 1 FROM forja.alpha_quality_findings)
       OR EXISTS (SELECT 1 FROM forja.alpha_source_objects)
       OR EXISTS (SELECT 1 FROM forja.alpha_ingestion_runs)
       OR EXISTS (SELECT 1 FROM forja.alpha_source_systems) THEN
        RAISE EXCEPTION USING
            ERRCODE='55000',
            MESSAGE='migration 010 rollback requires unused Forja Alpha financial state';
    END IF;
END
$$;

DROP TABLE forja.alpha_claim_evidence;
DROP TABLE forja.alpha_claims;
DROP TABLE forja.alpha_tool_invocations;
DROP TABLE forja.alpha_research_sessions;
DROP TABLE forja.alpha_holding_resolutions;
DROP TABLE forja.alpha_holding_positions;
DROP TABLE forja.alpha_holdings_reports;
DROP TABLE forja.alpha_managers;
DROP TABLE forja.alpha_analysis_results;
DROP TABLE forja.alpha_analysis_runs;
DROP TABLE forja.alpha_analysis_specs;
DROP TABLE forja.alpha_series_observations;
DROP TABLE forja.alpha_series;
DROP TABLE forja.alpha_metric_observations;
DROP TABLE forja.alpha_metric_mappings;
DROP TABLE forja.alpha_metric_definitions;
DROP TABLE forja.alpha_xbrl_facts;
DROP TABLE forja.alpha_xbrl_contexts;
DROP TABLE forja.alpha_xbrl_concepts;
DROP TABLE forja.alpha_taxonomies;
DROP TABLE forja.alpha_filing_documents;
DROP TABLE forja.alpha_filings;
DROP TABLE forja.alpha_corporate_actions;
DROP TABLE forja.alpha_identifiers;
DROP TABLE forja.alpha_securities;
DROP TABLE forja.alpha_issuers;
DROP TABLE forja.alpha_quality_findings;
DROP TABLE forja.alpha_source_objects;
DROP TABLE forja.alpha_ingestion_runs;
DROP TABLE forja.alpha_source_systems;
