package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/retrieval"
)

// GetIncidentForAttempt derives the one safe canonical incident view for a
// failed terminal attempt. It uses the append-only terminal event as evidence,
// validates it against the current attempt row, and never returns raw output.
func (s *Store) GetIncidentForAttempt(ctx context.Context, attemptID string) (contracts.Incident, bool, error) {
	if err := contracts.ValidateDeliveryAttemptID(attemptID); err != nil {
		return contracts.Incident{}, false, err
	}
	var eventID, eventType, runID, status string
	var version int
	var occurredAt time.Time
	var payload []byte
	err := s.pool.QueryRow(ctx, `
		SELECT event.event_id, event.event_type, event.occurred_at, event.payload,
		       attempt.run_id, attempt.status, attempt.version
		FROM forja.attempts AS attempt
		JOIN forja.runs AS run
		  ON run.tenant_id=attempt.tenant_id AND run.run_id=attempt.run_id
		JOIN forja.events AS event
		  ON event.tenant_id=attempt.tenant_id
		 AND event.repository_id=run.repository_id
		 AND event.aggregate_type='attempt'
		 AND event.aggregate_id=attempt.attempt_id
		 AND event.aggregate_version=attempt.version
		WHERE attempt.tenant_id=$1 AND run.repository_id=$2 AND attempt.attempt_id=$3
		  AND attempt.status IN ('failed_retryable', 'failed_terminal')
		  AND event.event_type IN ('attempt.finished', 'attempt.reconciled')`,
		s.tenantID, s.repositoryID, attemptID,
	).Scan(&eventID, &eventType, &occurredAt, &payload, &runID, &status, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.Incident{}, false, nil
	}
	if err != nil {
		return contracts.Incident{}, false, databaseError("postgres.GetIncidentForAttempt", err)
	}
	incident, err := incidentFromAttemptEvent(
		"tenant_"+s.tenantID, "repo_"+s.repositoryID, eventID, eventType,
		occurredAt.UTC(), payload, runID, attemptID, status, version,
	)
	if err != nil {
		return contracts.Incident{}, false, fmt.Errorf("decode canonical incident: %w", err)
	}
	return incident, true, nil
}

type incidentAttemptEventPayload struct {
	Attempt struct {
		AttemptID string `json:"attempt_id"`
		RunID     string `json:"run_id"`
		Status    string `json:"status"`
		Version   int    `json:"version"`
	} `json:"attempt"`
	Result *incidentWorkerResult `json:"result"`
}

type incidentWorkerResult struct {
	Status            string   `json:"status"`
	Retryable         bool     `json:"retryable"`
	TerminationReason string   `json:"termination_reason"`
	StdoutSHA256      string   `json:"stdout_sha256"`
	StderrSHA256      string   `json:"stderr_sha256"`
	ReportSHA256      string   `json:"report_sha256"`
	EvidenceRefs      []string `json:"evidence_refs"`
}

func incidentFromAttemptEvent(
	tenantID, repositoryID, eventID, eventType string,
	occurredAt time.Time, rawPayload []byte,
	runID, attemptID, status string, version int,
) (contracts.Incident, error) {
	var payload incidentAttemptEventPayload
	// Attempt events intentionally carry more fields than a safe incident card.
	// Decode the canonical event envelope permissively, then verify every field
	// used for authority below; raw output and unused metadata stay unobserved.
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return contracts.Incident{}, fmt.Errorf("decode attempt event payload: %w", err)
	}
	if payload.Attempt.AttemptID != attemptID || payload.Attempt.RunID != runID ||
		payload.Attempt.Status != status || payload.Attempt.Version != version {
		return contracts.Incident{}, fmt.Errorf("attempt event does not match canonical attempt")
	}
	incident := contracts.Incident{
		IncidentID: contracts.IncidentIDForAttempt(attemptID), SchemaVersion: contracts.IncidentSchemaVersion,
		TenantID: tenantID, RepositoryID: repositoryID, RunID: runID, AttemptID: attemptID,
		AttemptVersion: version, EventID: eventID, EventType: eventType, Status: "open",
		OccurredAt: occurredAt.UTC(),
	}
	switch eventType {
	case "attempt.finished":
		if payload.Result == nil || payload.Result.Status != status ||
			(payload.Result.Retryable && status != "failed_retryable") ||
			(!payload.Result.Retryable && status != "failed_terminal") ||
			strings.TrimSpace(payload.Result.TerminationReason) != payload.Result.TerminationReason ||
			payload.Result.TerminationReason == "" {
			return contracts.Incident{}, fmt.Errorf("terminal attempt event is internally inconsistent")
		}
		incident.Retryable = payload.Result.Retryable
		incident.Classification = payload.Result.TerminationReason
		incident.EvidenceRefs = incidentEvidenceRefs(payload.Result)
	case "attempt.reconciled":
		if payload.Result != nil || status != "failed_retryable" {
			return contracts.Incident{}, fmt.Errorf("reconciled attempt event is internally inconsistent")
		}
		incident.Retryable = true
		incident.Classification = "scheduler_fence_lost"
	default:
		return contracts.Incident{}, fmt.Errorf("attempt event cannot create an incident")
	}
	if incident.Retryable {
		incident.Severity = "warning"
	} else {
		incident.Severity = "critical"
	}
	var err error
	incident.SourceHash, err = contracts.IncidentSourceHash(incident)
	if err != nil {
		return contracts.Incident{}, err
	}
	if err := contracts.ValidateIncident(incident); err != nil {
		return contracts.Incident{}, err
	}
	return incident, nil
}

func incidentEvidenceRefs(result *incidentWorkerResult) []string {
	refs := append([]string(nil), result.EvidenceRefs...)
	for name, digest := range map[string]string{
		"worker_stdout": result.StdoutSHA256,
		"worker_stderr": result.StderrSHA256,
		"worker_report": result.ReportSHA256,
	} {
		if len(digest) == 64 {
			refs = append(refs, name+"#sha256="+digest)
		}
	}
	sort.Strings(refs)
	return compactIncidentRefs(refs)
}

func compactIncidentRefs(refs []string) []string {
	result := refs[:0]
	for _, ref := range refs {
		if len(result) == 0 || result[len(result)-1] != ref {
			result = append(result, ref)
		}
	}
	return result
}

var _ retrieval.ActiveIncidentSource = (*Store)(nil)
