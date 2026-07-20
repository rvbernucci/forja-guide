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

const metricRegistryVersion = "alpha-metric-registry-v1"

type AlphaMetricDefinition struct {
	MetricID         string
	MetricKey        string
	DisplayName      string
	MetricKind       string
	UnitPolicy       map[string]any
	FormulaSemantics map[string]any
	Version          string
}

type AlphaMetricMapping struct {
	MappingID       string
	MetricID        string
	ConceptID       string
	Taxonomy        string
	ConceptName     string
	IssuerID        string
	ContextFilter   map[string]any
	ConfidenceClass string
	ReviewedBy      string
	ValidFrom       string
}

func AlphaMetricDefinitions() []AlphaMetricDefinition {
	definitions := []AlphaMetricDefinition{
		reportedMetric("alpha_metric_revenue", "revenue", "Revenue", "monetary", "Reported revenue or equivalent contract revenue concept."),
		reportedMetric("alpha_metric_operating_income", "operating_income", "Operating Income", "monetary", "Reported operating income or loss."),
		reportedMetric("alpha_metric_net_income", "net_income", "Net Income", "monetary", "Reported net income or loss."),
		reportedMetric("alpha_metric_operating_cash_flow", "operating_cash_flow", "Operating Cash Flow", "monetary", "Reported net cash provided by or used in operating activities."),
		reportedMetric("alpha_metric_capital_expenditure", "capital_expenditure", "Capital Expenditure", "monetary", "Reported payments to acquire property, plant, and equipment."),
	}
	return append([]AlphaMetricDefinition(nil), definitions...)
}

func ReviewedUSGAAPMetricMappings(issuerID string) []AlphaMetricMapping {
	issuerID = strings.TrimSpace(issuerID)
	mappings := []AlphaMetricMapping{
		reviewedMetricMapping("revenue", "alpha_metric_revenue", "us-gaap", "Revenues", issuerID),
		reviewedMetricMapping("revenue_contract", "alpha_metric_revenue", "us-gaap", "RevenueFromContractWithCustomerExcludingAssessedTax", issuerID),
		reviewedMetricMapping("operating_income", "alpha_metric_operating_income", "us-gaap", "OperatingIncomeLoss", issuerID),
		reviewedMetricMapping("net_income", "alpha_metric_net_income", "us-gaap", "NetIncomeLoss", issuerID),
		reviewedMetricMapping("operating_cash_flow", "alpha_metric_operating_cash_flow", "us-gaap", "NetCashProvidedByUsedInOperatingActivities", issuerID),
		reviewedMetricMapping("capex", "alpha_metric_capital_expenditure", "us-gaap", "PaymentsToAcquirePropertyPlantAndEquipment", issuerID),
	}
	return append([]AlphaMetricMapping(nil), mappings...)
}

func WriteAlphaMetricRegistrySeedSQL(writer io.Writer, tenantID, repositoryID, issuerID string) error {
	if !uuidLiteralPattern.MatchString(tenantID) {
		return fmt.Errorf("tenant ID must be a UUID")
	}
	if !uuidLiteralPattern.MatchString(repositoryID) {
		return fmt.Errorf("repository ID must be a UUID")
	}
	if strings.TrimSpace(issuerID) == "" {
		return fmt.Errorf("issuer ID is required")
	}
	line := func(format string, args ...any) error {
		_, err := fmt.Fprintf(writer, format+"\n", args...)
		return err
	}
	if err := line("BEGIN;"); err != nil {
		return err
	}
	definitions := AlphaMetricDefinitions()
	sort.Slice(definitions, func(left, right int) bool {
		return definitions[left].MetricKey < definitions[right].MetricKey
	})
	for _, definition := range definitions {
		if err := writeMetricDefinitionSQL(writer, tenantID, repositoryID, definition); err != nil {
			return err
		}
	}
	mappings := ReviewedUSGAAPMetricMappings(issuerID)
	sort.Slice(mappings, func(left, right int) bool {
		return mappings[left].MappingID < mappings[right].MappingID
	})
	for _, mapping := range mappings {
		if err := writeMetricMappingSQL(writer, tenantID, repositoryID, mapping); err != nil {
			return err
		}
	}
	return line("COMMIT;")
}

func reportedMetric(id, key, displayName, unitKind, description string) AlphaMetricDefinition {
	return AlphaMetricDefinition{
		MetricID:    id,
		MetricKey:   key,
		DisplayName: displayName,
		MetricKind:  "reported",
		UnitPolicy: map[string]any{
			"kind":               unitKind,
			"currency_required":  true,
			"accepted_currency":  "USD",
			"source_value_basis": "as_reported",
		},
		FormulaSemantics: map[string]any{
			"description": description,
			"selection":   "reviewed_xbrl_mapping_required",
			"authority":   "reported_fact_before_derived_metric",
		},
		Version: "v1",
	}
}

func reviewedMetricMapping(mappingKey, metricID, taxonomy, conceptName, issuerID string) AlphaMetricMapping {
	identity := strings.Join([]string{mappingKey, metricID, taxonomy, conceptName, issuerID}, "|")
	digest := sha256.Sum256([]byte(identity))
	return AlphaMetricMapping{
		MappingID:       "alpha_mapping_" + hex.EncodeToString(digest[:16]),
		MetricID:        metricID,
		ConceptID:       conceptID(taxonomy, conceptName),
		Taxonomy:        taxonomy,
		ConceptName:     conceptName,
		IssuerID:        issuerID,
		ContextFilter:   map[string]any{"source": "companyfacts", "taxonomy": taxonomy, "concept": conceptName, "unit": "USD"},
		ConfidenceClass: "reviewed",
		ReviewedBy:      metricRegistryVersion,
		ValidFrom:       seedValidFrom,
	}
}

func writeMetricDefinitionSQL(writer io.Writer, tenantID, repositoryID string, definition AlphaMetricDefinition) error {
	unitPolicy, err := json.Marshal(definition.UnitPolicy)
	if err != nil {
		return err
	}
	formulaSemantics, err := json.Marshal(definition.FormulaSemantics)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, `
INSERT INTO forja.alpha_metric_definitions (
    tenant_id, repository_id, metric_id, metric_key, display_name,
    metric_kind, unit_policy, formula_semantics, version, status
) VALUES (
    %s::uuid, %s::uuid, %s, %s, %s,
    %s, %s::jsonb, %s::jsonb, %s, 'active'
) ON CONFLICT (tenant_id, repository_id, metric_id) DO UPDATE SET
    metric_key=EXCLUDED.metric_key,
    display_name=EXCLUDED.display_name,
    metric_kind=EXCLUDED.metric_kind,
    unit_policy=EXCLUDED.unit_policy,
    formula_semantics=EXCLUDED.formula_semantics,
    version=EXCLUDED.version,
    status=EXCLUDED.status;
`,
		sqlString(tenantID), sqlString(repositoryID), sqlString(definition.MetricID), sqlString(definition.MetricKey), sqlString(definition.DisplayName),
		sqlString(definition.MetricKind), sqlString(string(unitPolicy)), sqlString(string(formulaSemantics)), sqlString(definition.Version))
	return err
}

func writeMetricMappingSQL(writer io.Writer, tenantID, repositoryID string, mapping AlphaMetricMapping) error {
	contextFilter, err := json.Marshal(mapping.ContextFilter)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, `
INSERT INTO forja.alpha_metric_mappings (
    tenant_id, repository_id, metric_mapping_id, metric_id, concept_id,
    issuer_id, context_filter, confidence_class, valid_from, valid_to,
    reviewed_by
) VALUES (
    %s::uuid, %s::uuid, %s, %s, %s,
    %s, %s::jsonb, %s, %s::timestamptz, NULL,
    %s
) ON CONFLICT (tenant_id, repository_id, metric_mapping_id) DO UPDATE SET
    metric_id=EXCLUDED.metric_id,
    concept_id=EXCLUDED.concept_id,
    issuer_id=EXCLUDED.issuer_id,
    context_filter=EXCLUDED.context_filter,
    confidence_class=EXCLUDED.confidence_class,
    valid_from=EXCLUDED.valid_from,
    valid_to=EXCLUDED.valid_to,
    reviewed_by=EXCLUDED.reviewed_by;
`,
		sqlString(tenantID), sqlString(repositoryID), sqlString(mapping.MappingID), sqlString(mapping.MetricID), sqlString(mapping.ConceptID),
		sqlString(mapping.IssuerID), sqlString(string(contextFilter)), sqlString(mapping.ConfidenceClass), sqlString(mapping.ValidFrom),
		sqlString(mapping.ReviewedBy))
	return err
}
