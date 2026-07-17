package contracts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRegistryValidatesGovernanceContracts(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	decisionID := "decision_11111111-2222-4333-8444-555555555555"
	sprint := Sprint{
		SprintID: "sprint_00010203-0405-4607-8809-0a0b0c0d0e0f", SchemaVersion: "1.0",
		SequenceNumber: 3, Title: "Governed Sprint", Objective: "Validate governed contracts",
		Status: "awaiting_approval", Version: 2,
		RunID:             "run_aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		PendingDecisionID: &decisionID, CreatedAt: now, UpdatedAt: now,
	}
	decision := Decision{
		DecisionID: decisionID, SchemaVersion: "1.0", SprintID: sprint.SprintID,
		RunID: sprint.RunID, Action: "submit_sprint", RiskClass: "medium",
		Status: "pending", Version: 1, RequestedBy: "co-architect",
		CreatedAt: now, UpdatedAt: now,
	}
	for name, value := range map[string]any{
		"sprint.schema.json":   sprint,
		"decision.schema.json": decision,
	} {
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err := registry.ValidateJSON(name, data); err != nil {
			t.Fatalf("%s rejected canonical value: %v", name, err)
		}
	}
}

func TestDecisionSchemaMatchesResolutionInvariants(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{
		"decision_id":    "decision_11111111-2222-4333-8444-555555555555",
		"schema_version": "1.0",
		"sprint_id":      "sprint_00010203-0405-4607-8809-0a0b0c0d0e0f",
		"run_id":         "run_aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		"action":         "submit_sprint", "risk_class": "medium", "version": 1,
		"requested_by": "co-architect", "created_at": "2026-07-16T12:00:00Z",
		"updated_at": "2026-07-16T12:00:00Z",
	}
	for name, mutate := range map[string]func(map[string]any){
		"resolved decision without resolution fields": func(value map[string]any) {
			value["status"] = "approved"
		},
		"pending decision with resolution fields": func(value map[string]any) {
			value["status"] = "pending"
			value["decided_by"] = "approver"
			value["reason"] = "Should not be present"
			value["decided_at"] = "2026-07-16T12:00:00Z"
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := make(map[string]any, len(base)+4)
			for key, item := range base {
				value[key] = item
			}
			mutate(value)
			data, marshalErr := json.Marshal(value)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if err := registry.ValidateJSON("decision.schema.json", data); err == nil {
				t.Fatal("decision invariant violation passed canonical schema")
			}
		})
	}
}

func TestSprintSchemaAcceptsLegacySequenceZero(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{
		"sprint_id":"sprint_00010203-0405-4607-8809-0a0b0c0d0e0f",
		"schema_version":"1.0",
		"sequence_number":0,
		"title":"Legacy Sprint",
		"objective":"Migrated legacy Sprint",
		"status":"proposed",
		"version":1,
		"run_id":"run_aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		"created_at":"2026-07-16T12:00:00Z",
		"updated_at":"2026-07-16T12:00:00Z"
	}`)
	if err := registry.ValidateJSON("sprint.schema.json", data); err != nil {
		t.Fatalf("legacy sequence zero rejected: %v", err)
	}
}

func TestSprintSchemaEnforcesPendingDecisionLifecycle(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{
		"sprint_id":       "sprint_00010203-0405-4607-8809-0a0b0c0d0e0f",
		"schema_version":  "1.0",
		"sequence_number": 3,
		"title":           "Governed Sprint",
		"objective":       "Validate pending decision coupling",
		"version":         2,
		"run_id":          "run_aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		"created_at":      "2026-07-16T12:00:00Z",
		"updated_at":      "2026-07-16T12:00:00Z",
	}
	for name, mutate := range map[string]func(map[string]any){
		"awaiting approval without pending decision": func(value map[string]any) {
			value["status"] = "awaiting_approval"
		},
		"resolved Sprint with pending decision": func(value map[string]any) {
			value["status"] = "approved"
			value["pending_decision_id"] = "decision_11111111-2222-4333-8444-555555555555"
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := make(map[string]any, len(base)+2)
			for key, item := range base {
				value[key] = item
			}
			mutate(value)
			data, marshalErr := json.Marshal(value)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if err := registry.ValidateJSON("sprint.schema.json", data); err == nil {
				t.Fatal("Sprint pending-decision invariant violation passed schema")
			}
		})
	}
}

func TestRegistryValidatesContractFixtures(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile(filepath.Join("testdata", "run.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := DecodeStrict[Run](registry, "run.schema.json", valid)
	if err != nil {
		t.Fatal(err)
	}
	if run.RunID != "run_00010203-0405-4607-8809-0a0b0c0d0e0f" {
		t.Fatalf("unexpected run ID: %s", run.RunID)
	}

	invalid, err := os.ReadFile(filepath.Join("testdata", "run.invalid.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeStrict[Run](registry, "run.schema.json", invalid); err == nil {
		t.Fatal("expected invalid fixture to fail")
	}
}

func TestRegistryRejectsUnknownSchemaAndTrailingDocument(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateJSON("missing.schema.json", []byte(`{}`)); err == nil {
		t.Fatal("expected unknown schema to fail")
	}
	data := []byte(`{
		"run_id": "run_00010203-0405-4607-8809-0a0b0c0d0e0f",
		"schema_version": "1.0",
		"objective": "Synthetic run",
		"state": "draft",
		"version": 1,
		"created_at": "2026-07-16T12:00:00Z",
		"updated_at": "2026-07-16T12:00:00Z"
	} {}`)
	if _, err := DecodeStrict[Run](registry, "run.schema.json", data); err == nil {
		t.Fatal("expected trailing document to fail")
	}
}

func FuzzRunContract(f *testing.F) {
	valid, err := os.ReadFile(filepath.Join("testdata", "run.valid.json"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte(`{}`))
	registry, err := NewRegistry()
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeStrict[Run](registry, "run.schema.json", data)
	})
}
