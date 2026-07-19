package contracts

import (
	"encoding/json"
	"testing"
	"time"
)

func TestIncidentContractAndSchemaAgree(t *testing.T) {
	t.Parallel()
	incident := validIncident(t)
	if err := ValidateIncident(incident); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(incident)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateJSON("incident.schema.json", encoded); err != nil {
		t.Fatal(err)
	}
}

func TestIncidentValidationFailsClosed(t *testing.T) {
	t.Parallel()
	incident := validIncident(t)
	incident.EvidenceRefs = []string{"z", "a"}
	if err := ValidateIncident(incident); err == nil {
		t.Fatal("unsorted incident evidence was accepted")
	}
	incident = validIncident(t)
	incident.Retryable = true
	if err := ValidateIncident(incident); err == nil {
		t.Fatal("severity/retryability mismatch was accepted")
	}
	incident = validIncident(t)
	incident.Classification = "altered"
	if err := ValidateIncident(incident); err == nil {
		t.Fatal("altered incident source hash was accepted")
	}
}

func validIncident(t *testing.T) Incident {
	t.Helper()
	incident := Incident{
		IncidentID:    "incident_attempt_00000000-0000-4000-8000-000000000011",
		SchemaVersion: IncidentSchemaVersion,
		TenantID:      testTenantID, RepositoryID: testRepositoryID,
		RunID:          "run_00000000-0000-4000-8000-000000000012",
		AttemptID:      "attempt_00000000-0000-4000-8000-000000000011",
		AttemptVersion: 3, EventID: "event_contract_incident", EventType: "attempt.finished",
		Status: "open", Severity: "critical", Classification: "process_failure", Retryable: false,
		OccurredAt: time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC), EvidenceRefs: []string{},
	}
	var err error
	incident.SourceHash, err = IncidentSourceHash(incident)
	if err != nil {
		t.Fatal(err)
	}
	return incident
}
