package postgres

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/retrieval"
)

func TestIncidentFromAttemptEventReadsSafeSubsetOfCanonicalEvent(t *testing.T) {
	t.Parallel()
	attemptID := "attempt_00000000-0000-4000-8000-000000000011"
	runID := "run_00000000-0000-4000-8000-000000000012"
	payload, err := json.Marshal(map[string]any{
		"attempt": map[string]any{
			"attempt_id": attemptID, "run_id": runID, "ordinal": 1,
			"status": "failed_terminal", "lease_resource_type": "scheduler",
			"lease_resource_id": "fixture", "worker_id": "worker", "fencing_token": 4,
			"started_at": "2026-07-19T18:00:00Z", "finished_at": "2026-07-19T18:00:01Z",
			"version": 3, "created_at": "2026-07-19T17:59:00Z", "updated_at": "2026-07-19T18:00:01Z",
		},
		"result": map[string]any{
			"task_id": "task_00000000-0000-4000-8000-000000000013", "adapter": "codex",
			"status": "failed_terminal", "retryable": false, "termination_reason": "process_failure",
			"started_at": "2026-07-19T18:00:00Z", "finished_at": "2026-07-19T18:00:01Z",
			"duration_ms": 1000, "exit_code": 1, "stdout_sha256": strings.Repeat("a", 64),
			"stderr_sha256": strings.Repeat("b", 64), "report_sha256": strings.Repeat("c", 64),
			"usage":         map[string]any{"input_tokens": 4, "cached_input_tokens": 0, "output_tokens": 2, "tool_calls": 1},
			"evidence_refs": []string{"evidence/result.json#sha256=" + strings.Repeat("d", 64)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	incident, err := incidentFromAttemptEvent(
		"tenant_00000000-0000-4000-8000-000000000001",
		"repo_00000000-0000-4000-8000-000000000002",
		"event_incident_fixture", "attempt.finished", time.Date(2026, 7, 19, 18, 0, 1, 0, time.UTC),
		payload, runID, attemptID, "failed_terminal", 3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := contracts.ValidateIncident(incident); err != nil || incident.Classification != "process_failure" || len(incident.EvidenceRefs) != 4 {
		t.Fatalf("incident=%#v err=%v", incident, err)
	}
	source, err := retrieval.BuildIncidentSource(incident)
	if err != nil {
		t.Fatal(err)
	}
	card, err := retrieval.BuildCardText(source)
	if err != nil || strings.Contains(card, "usage") || strings.Contains(card, "worker_id") || strings.Contains(card, "SECRET_") {
		t.Fatalf("card=%q err=%v", card, err)
	}
}

func TestIncidentFromAttemptEventRejectsMismatchedCurrentAttempt(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"attempt":{"attempt_id":"attempt_00000000-0000-4000-8000-000000000011","run_id":"run_00000000-0000-4000-8000-000000000012","status":"failed_terminal","version":3},"result":{"status":"failed_terminal","retryable":false,"termination_reason":"process_failure"}}`)
	_, err := incidentFromAttemptEvent(
		"tenant_00000000-0000-4000-8000-000000000001",
		"repo_00000000-0000-4000-8000-000000000002",
		"event_incident_fixture", "attempt.finished", time.Now().UTC(), payload,
		"run_00000000-0000-4000-8000-000000000012",
		"attempt_00000000-0000-4000-8000-000000000011", "failed_retryable", 3,
	)
	if err == nil {
		t.Fatal("mismatched current attempt was accepted")
	}
}
