package postgres

import "testing"

func TestAlphaPointInTimeViewsExposeAvailabilityClock(t *testing.T) {
	pool := migratedPool(t)

	viewColumns := map[string][]string{
		"alpha_v_source_coverage": {
			"tenant_id",
			"repository_id",
			"source_object_id",
			"source_key",
			"data_family",
			"ingestion_state",
			"object_count",
			"row_count",
			"available_at",
			"ingested_at",
			"metadata",
		},
		"alpha_v_issuer_filing_timeline": {
			"tenant_id",
			"repository_id",
			"issuer_id",
			"accession_number",
			"form_type",
			"period_end",
			"filed_at",
			"available_at",
			"source_object_id",
		},
		"alpha_v_reported_metric_panel": {
			"tenant_id",
			"repository_id",
			"metric_id",
			"metric_key",
			"issuer_id",
			"filing_id",
			"period_end",
			"available_at",
			"value_numeric",
			"lineage",
		},
	}

	for viewName, columns := range viewColumns {
		t.Run(viewName, func(t *testing.T) {
			var exists bool
			if err := pool.QueryRow(t.Context(), `
				SELECT EXISTS (
					SELECT 1
					FROM information_schema.views
					WHERE table_schema='forja' AND table_name=$1
				)`, viewName).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if !exists {
				t.Fatalf("view %s was not created", viewName)
			}

			for _, column := range columns {
				var columnExists bool
				if err := pool.QueryRow(t.Context(), `
					SELECT EXISTS (
						SELECT 1
						FROM information_schema.columns
						WHERE table_schema='forja' AND table_name=$1 AND column_name=$2
					)`, viewName, column).Scan(&columnExists); err != nil {
					t.Fatal(err)
				}
				if !columnExists {
					t.Fatalf("view %s missing column %s", viewName, column)
				}
			}
		})
	}
}
