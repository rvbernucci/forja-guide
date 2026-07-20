package contracts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	IncidentSchemaVersion = "1.0"
	IncidentSchemaRef     = "https://forja.dev/schemas/incident.schema.json"
)

var (
	incidentIDPattern       = regexp.MustCompile(`^incident_attempt_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	incidentEventIDPattern  = regexp.MustCompile(`^event_[A-Za-z0-9_-]+$`)
	incidentRunIDPattern    = regexp.MustCompile(`^run_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	incidentSourceHashRegex = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// Incident is the safe, immutable operational-failure view of one terminal
// attempt. It is derived from the append-only attempt event rather than raw
// worker output, so it can be retrieved without exposing stdout or stderr.
type Incident struct {
	IncidentID     string    `json:"incident_id"`
	SchemaVersion  string    `json:"schema_version"`
	TenantID       string    `json:"tenant_id"`
	RepositoryID   string    `json:"repository_id"`
	RunID          string    `json:"run_id"`
	AttemptID      string    `json:"attempt_id"`
	AttemptVersion int       `json:"attempt_version"`
	EventID        string    `json:"event_id"`
	EventType      string    `json:"event_type"`
	Status         string    `json:"status"`
	Severity       string    `json:"severity"`
	Classification string    `json:"classification"`
	Retryable      bool      `json:"retryable"`
	OccurredAt     time.Time `json:"occurred_at"`
	EvidenceRefs   []string  `json:"evidence_refs"`
	SourceHash     string    `json:"source_hash"`
}

// IncidentIDForAttempt is stable for the lifetime of one immutable attempt.
func IncidentIDForAttempt(attemptID string) string {
	return "incident_" + attemptID
}

// AttemptIDFromIncidentID returns the exact canonical attempt identity.
func AttemptIDFromIncidentID(incidentID string) (string, bool) {
	if !incidentIDPattern.MatchString(incidentID) {
		return "", false
	}
	return strings.TrimPrefix(incidentID, "incident_"), true
}

// IncidentSourceHash returns the deterministic source identity for a safe
// incident view. It intentionally excludes its derived SourceHash field.
func IncidentSourceHash(incident Incident) (string, error) {
	payload := struct {
		IncidentID     string   `json:"incident_id"`
		SchemaVersion  string   `json:"schema_version"`
		TenantID       string   `json:"tenant_id"`
		RepositoryID   string   `json:"repository_id"`
		RunID          string   `json:"run_id"`
		AttemptID      string   `json:"attempt_id"`
		AttemptVersion int      `json:"attempt_version"`
		EventID        string   `json:"event_id"`
		EventType      string   `json:"event_type"`
		Status         string   `json:"status"`
		Severity       string   `json:"severity"`
		Classification string   `json:"classification"`
		Retryable      bool     `json:"retryable"`
		OccurredAt     string   `json:"occurred_at"`
		EvidenceRefs   []string `json:"evidence_refs"`
	}{
		IncidentID: incident.IncidentID, SchemaVersion: incident.SchemaVersion,
		TenantID: incident.TenantID, RepositoryID: incident.RepositoryID,
		RunID: incident.RunID, AttemptID: incident.AttemptID,
		AttemptVersion: incident.AttemptVersion, EventID: incident.EventID,
		EventType: incident.EventType, Status: incident.Status,
		Severity: incident.Severity, Classification: incident.Classification,
		Retryable: incident.Retryable, OccurredAt: incident.OccurredAt.UTC().Format(time.RFC3339Nano),
		EvidenceRefs: sortedIncidentRefs(incident.EvidenceRefs),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode incident source: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// ValidateIncident ensures an incident is a safe and exact canonical view.
func ValidateIncident(incident Incident) error {
	if incident.SchemaVersion != IncidentSchemaVersion || !incidentIDPattern.MatchString(incident.IncidentID) ||
		IncidentIDForAttempt(incident.AttemptID) != incident.IncidentID ||
		!attemptIDPattern.MatchString(incident.AttemptID) || !incidentRunIDPattern.MatchString(incident.RunID) ||
		!incidentEventIDPattern.MatchString(incident.EventID) || incident.AttemptVersion < 1 ||
		(incident.EventType != "attempt.finished" && incident.EventType != "attempt.reconciled") ||
		incident.Status != "open" ||
		(incident.Severity != "warning" && incident.Severity != "critical") ||
		strings.TrimSpace(incident.Classification) != incident.Classification || incident.Classification == "" || len(incident.Classification) > 120 ||
		incident.OccurredAt.IsZero() || !incidentSourceHashRegex.MatchString(incident.SourceHash) {
		return fmt.Errorf("incident fields are invalid")
	}
	if err := ValidateRepositoryIdentity(incident.TenantID, incident.RepositoryID); err != nil {
		return fmt.Errorf("incident repository identity: %w", err)
	}
	if incident.Severity == "warning" && !incident.Retryable || incident.Severity == "critical" && incident.Retryable {
		return fmt.Errorf("incident severity does not agree with retryability")
	}
	if !slices.IsSorted(incident.EvidenceRefs) || slices.Contains(incident.EvidenceRefs, "") {
		return fmt.Errorf("incident evidence references must be sorted and non-empty")
	}
	for index, ref := range incident.EvidenceRefs {
		if strings.TrimSpace(ref) != ref || len(ref) > 4096 || (index > 0 && ref == incident.EvidenceRefs[index-1]) {
			return fmt.Errorf("incident evidence references are invalid")
		}
	}
	expected, err := IncidentSourceHash(incident)
	if err != nil || expected != incident.SourceHash {
		return fmt.Errorf("incident source hash is invalid")
	}
	return nil
}

func sortedIncidentRefs(refs []string) []string {
	result := append([]string(nil), refs...)
	sort.Strings(result)
	return result
}
