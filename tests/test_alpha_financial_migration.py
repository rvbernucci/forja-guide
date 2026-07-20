"""Validate the public Forja Alpha financial schema migration text."""

from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
UP = ROOT / "internal/postgres/migrations/000010_forja_alpha_financial_data.up.sql"
DOWN = ROOT / "internal/postgres/migrations/000010_forja_alpha_financial_data.down.sql"


EXPECTED_TABLES = [
    "alpha_source_systems",
    "alpha_ingestion_runs",
    "alpha_source_objects",
    "alpha_quality_findings",
    "alpha_issuers",
    "alpha_securities",
    "alpha_identifiers",
    "alpha_corporate_actions",
    "alpha_filings",
    "alpha_filing_documents",
    "alpha_taxonomies",
    "alpha_xbrl_concepts",
    "alpha_xbrl_contexts",
    "alpha_xbrl_facts",
    "alpha_metric_definitions",
    "alpha_metric_mappings",
    "alpha_metric_observations",
    "alpha_series",
    "alpha_series_observations",
    "alpha_analysis_specs",
    "alpha_analysis_runs",
    "alpha_analysis_results",
    "alpha_managers",
    "alpha_holdings_reports",
    "alpha_holding_positions",
    "alpha_holding_resolutions",
    "alpha_research_sessions",
    "alpha_tool_invocations",
    "alpha_claims",
    "alpha_claim_evidence",
]


class AlphaFinancialMigrationTests(unittest.TestCase):
    def test_alpha_financial_schema_covers_canonical_products(self) -> None:
        sql = UP.read_text(encoding="utf-8")
        down_sql = DOWN.read_text(encoding="utf-8")
        for table in EXPECTED_TABLES:
            self.assertIn(f"CREATE TABLE forja.{table}", sql)
            self.assertIn(f"DROP TABLE forja.{table}", down_sql)

    def test_alpha_financial_schema_enforces_time_lineage_and_quality_boundaries(self) -> None:
        sql = UP.read_text(encoding="utf-8")
        required_fragments = [
            "available_at timestamptz NOT NULL",
            "content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256)=32)",
            "quality_state text NOT NULL CHECK (quality_state IN ('accepted', 'quarantined', 'superseded'))",
            "CHECK (available_at <= ingested_at)",
            "CHECK (available_at >= filed_at)",
            "CHECK ((confidence_class='reviewed') = (reviewed_by IS NOT NULL))",
            "CHECK (as_of >= window_end)",
            "evidence jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(evidence)='object')",
        ]
        for fragment in required_fragments:
            self.assertIn(fragment, sql)


if __name__ == "__main__":
    unittest.main()
