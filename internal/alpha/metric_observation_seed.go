package alpha

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

type AlphaMetricObservation struct {
	ObservationID string
	MetricID      string
	IssuerID      string
	FilingID      string
	SourceFactID  string
	ObservedAt    string
	PeriodStart   string
	PeriodEnd     string
	ValueNumeric  string
	Unit          string
	Currency      string
	Lineage       map[string]any
}

func MetricObservationsFromCompanyFacts(snapshot SECCompanyFactsSnapshot) ([]AlphaMetricObservation, error) {
	if err := validateSECCompanyFactsSnapshot(snapshot); err != nil {
		return nil, err
	}
	mappingByConcept := map[string]AlphaMetricMapping{}
	for _, mapping := range ReviewedUSGAAPMetricMappings(snapshot.Company.IssuerID) {
		mappingByConcept[mapping.ConceptID] = mapping
	}
	observations := []AlphaMetricObservation{}
	for _, fact := range snapshot.RawFacts {
		if fact.QualityState != "" && fact.QualityState != "accepted" {
			continue
		}
		mapping, ok := mappingByConcept[fact.ConceptID]
		if !ok {
			continue
		}
		if fact.NumericValue == "" {
			continue
		}
		if strings.ToUpper(strings.TrimSpace(fact.Unit)) != "USD" {
			continue
		}
		if fact.PeriodEnd == "" {
			continue
		}
		identity := strings.Join([]string{mapping.MetricID, fact.FactID, fact.PeriodEnd, fact.Unit}, "|")
		digest := sha256.Sum256([]byte(identity))
		observations = append(observations, AlphaMetricObservation{
			ObservationID: "alpha_metric_obs_" + hex.EncodeToString(digest[:16]),
			MetricID:      mapping.MetricID,
			IssuerID:      snapshot.Company.IssuerID,
			FilingID:      fact.FilingID,
			SourceFactID:  fact.FactID,
			ObservedAt:    fact.PeriodEnd,
			PeriodStart:   fact.PeriodStart,
			PeriodEnd:     fact.PeriodEnd,
			ValueNumeric:  fact.NumericValue,
			Unit:          fact.Unit,
			Currency:      fact.Currency,
			Lineage: map[string]any{
				"source":          "sec.companyfacts",
				"source_object":   snapshot.CompanyFactsSourceObjectID(),
				"source_sha256":   snapshot.ContentSHA256,
				"taxonomy":        fact.Taxonomy,
				"concept":         fact.ConceptName,
				"accession":       fact.Accession,
				"form":            fact.FormType,
				"fiscal_year":     fact.FiscalYear,
				"fiscal_period":   fact.FiscalPeriod,
				"period_start":    fact.PeriodStart,
				"period_end":      fact.PeriodEnd,
				"mapping_version": metricRegistryVersion,
				"selection":       "mapped_raw_fact_only_no_amendment_or_ytd_resolution",
			},
		})
	}
	sort.Slice(observations, func(left, right int) bool {
		return observations[left].ObservationID < observations[right].ObservationID
	})
	return observations, nil
}

func WriteAlphaMetricObservationsSeedSQL(writer io.Writer, tenantID, repositoryID string, snapshot SECCompanyFactsSnapshot) error {
	if !uuidLiteralPattern.MatchString(tenantID) {
		return fmt.Errorf("tenant ID must be a UUID")
	}
	if !uuidLiteralPattern.MatchString(repositoryID) {
		return fmt.Errorf("repository ID must be a UUID")
	}
	observations, err := MetricObservationsFromCompanyFacts(snapshot)
	if err != nil {
		return err
	}
	line := func(format string, args ...any) error {
		_, err := fmt.Fprintf(writer, format+"\n", args...)
		return err
	}
	if err := line("BEGIN;"); err != nil {
		return err
	}
	for _, observation := range observations {
		if err := writeMetricObservationSQL(writer, tenantID, repositoryID, observation); err != nil {
			return err
		}
	}
	return line("COMMIT;")
}

func writeMetricObservationSQL(writer io.Writer, tenantID, repositoryID string, observation AlphaMetricObservation) error {
	lineage, err := json.Marshal(observation.Lineage)
	if err != nil {
		return err
	}
	currency := "NULL"
	if observation.Currency != "" {
		currency = sqlString(observation.Currency)
	}
	_, err = fmt.Fprintf(writer, `
INSERT INTO forja.alpha_metric_observations (
    tenant_id, repository_id, metric_observation_id, metric_id, issuer_id,
    security_id, filing_id, source_fact_id, observation_kind, observed_at,
    period_start, period_end, available_at, value_numeric, unit, currency,
    lineage, quality_state
) VALUES (
    %s::uuid, %s::uuid, %s, %s, %s,
    NULL, %s, %s, 'reported', %s::timestamptz,
    %s::date, %s::date, %s::timestamptz, %s, %s, %s,
    %s::jsonb, 'accepted'
) ON CONFLICT (tenant_id, repository_id, metric_observation_id) DO UPDATE SET
    metric_id=EXCLUDED.metric_id,
    issuer_id=EXCLUDED.issuer_id,
    filing_id=EXCLUDED.filing_id,
    source_fact_id=EXCLUDED.source_fact_id,
    observation_kind=EXCLUDED.observation_kind,
    observed_at=EXCLUDED.observed_at,
    period_start=EXCLUDED.period_start,
    period_end=EXCLUDED.period_end,
    available_at=EXCLUDED.available_at,
    value_numeric=EXCLUDED.value_numeric,
    unit=EXCLUDED.unit,
    currency=EXCLUDED.currency,
    lineage=EXCLUDED.lineage,
    quality_state=EXCLUDED.quality_state;
`,
		sqlString(tenantID), sqlString(repositoryID), sqlString(observation.ObservationID), sqlString(observation.MetricID), sqlString(observation.IssuerID),
		sqlString(observation.FilingID), sqlString(observation.SourceFactID), sqlString(observation.ObservedAt),
		sqlString(observation.PeriodStart), sqlString(observation.PeriodEnd), sqlString(observation.ObservedAt), observation.ValueNumeric, sqlString(observation.Unit), currency,
		sqlString(string(lineage)))
	return err
}
