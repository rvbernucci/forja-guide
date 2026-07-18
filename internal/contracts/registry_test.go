package contracts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestWorkerContractsEnforceResultStateCoupling(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	exitCode := 0
	result := WorkerResult{
		TaskID:        "task_00010203-0405-4607-8809-0a0b0c0d0e0f",
		AttemptID:     "attempt_11111111-2222-4333-8444-555555555555",
		RunID:         "run_aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		SchemaVersion: "1.0", Adapter: "fixture", Status: "succeeded",
		Retryable: false, TerminationReason: "completed",
		StartedAt: now, FinishedAt: now.Add(time.Second), DurationMS: 1000,
		ExitCode: &exitCode, Stdout: "", Stderr: "",
		StdoutSHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		StderrSHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Usage:        WorkerUsage{},
		Report:       &WorkerReport{Status: "completed", Summary: "validated", ChangedPaths: []string{}, EvidenceRefs: []string{}, Risks: []string{}},
		EvidenceRefs: []string{},
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateJSON("worker-result.schema.json", data); err != nil {
		t.Fatalf("valid worker result rejected: %v", err)
	}
	for name, mutate := range map[string]func(*WorkerResult){
		"retryable success":         func(value *WorkerResult) { value.Retryable = true },
		"blocked report on success": func(value *WorkerResult) { value.Report.Status = "blocked" },
		"missing success report":    func(value *WorkerResult) { value.Report = nil },
	} {
		t.Run(name, func(t *testing.T) {
			copy := result
			report := *result.Report
			copy.Report = &report
			mutate(&copy)
			encoded, marshalErr := json.Marshal(copy)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if err := registry.ValidateJSON("worker-result.schema.json", encoded); err == nil {
				t.Fatal("invalid worker result passed canonical schema")
			}
		})
	}
}

func TestRegistryValidatesDeliveryContracts(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	request := validDeliveryRequest()
	zero := 0
	emptyDigest := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	report := ValidationReport{
		ValidationID: "validation_33333333-4444-4555-8666-777777777777",
		DeliveryID:   request.DeliveryID, SchemaVersion: DeliverySchemaVersion, Status: "passed",
		TenantID: request.TenantID, RepositoryID: request.RepositoryID,
		BaseCommit: request.BaseCommit, ResultCommit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		PatchSHA256: strings.Repeat("c", 64), AuthorID: request.AuthorID,
		ValidatorID: request.ValidatorID, CleanCheckout: true,
		Checks: []ValidationCheck{
			{
				CheckID: "clean-checkout", Kind: "independent", Status: "passed",
				StartedAt: now, FinishedAt: now.Add(time.Second), DurationMS: 1000,
				ExitCode: &zero, StdoutSHA256: emptyDigest, StderrSHA256: emptyDigest,
			},
			passingValidationCheck("filesystem-safety", "built_in", now, &zero, emptyDigest),
			passingValidationCheck("generated-file-policy", "built_in", now, &zero, emptyDigest),
			{
				CheckID: "go-format", Kind: "configured", Status: "passed",
				StartedAt: now.Add(time.Second), FinishedAt: now.Add(2 * time.Second), DurationMS: 1000,
				ExitCode: &zero, StdoutSHA256: emptyDigest, StderrSHA256: emptyDigest,
			},
			{
				CheckID: "go-test", Kind: "configured", Status: "passed",
				StartedAt: now.Add(time.Second), FinishedAt: now.Add(2 * time.Second), DurationMS: 1000,
				ExitCode: &zero, StdoutSHA256: emptyDigest, StderrSHA256: emptyDigest,
			},
			passingValidationCheck("schema-validation", "built_in", now, &zero, emptyDigest),
			passingValidationCheck("scope-boundary", "built_in", now, &zero, emptyDigest),
			passingValidationCheck("secret-scan", "built_in", now, &zero, emptyDigest),
		},
		CreatedAt: now.Add(2 * time.Second),
	}
	canonicalReport, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	evidenceManifest := canonicalEvidenceManifest(t, request, canonicalReport)
	receipt := DeliveryReceipt{
		DeliveryID: request.DeliveryID, SchemaVersion: DeliverySchemaVersion, Status: "published",
		TenantID: request.TenantID, RepositoryID: request.RepositoryID,
		BaseCommit: request.BaseCommit, ResultCommit: report.ResultCommit,
		ResultTree:  "dddddddddddddddddddddddddddddddddddddddd",
		PatchSHA256: report.PatchSHA256, ChangedPaths: []string{"internal/delivery/delivery.go"},
		PublicationRef: request.PublicationRef, AuthorID: request.AuthorID,
		ValidatorID: request.ValidatorID,
		LeaseFences: []DeliveryLeaseFence{
			{ResourceType: "artifact", ResourceID: "evidence", OwnerID: "delivery-service", FencingToken: 4},
			{ResourceType: "file", ResourceID: "internal", OwnerID: "delivery-service", FencingToken: 7},
			{ResourceType: "file", ResourceID: "internal/delivery", OwnerID: "delivery-service", FencingToken: 8},
			{ResourceType: "worktree", ResourceID: request.DeliveryID, OwnerID: "delivery-service", FencingToken: 2},
		},
		ValidationReportRef: "evidence/attempt-1/validation.json#sha256=" + sha256Hex(canonicalReport),
		EvidenceManifestRef: "evidence/attempt-1/manifest.json#sha256=" + sha256Hex(evidenceManifest),
		CreatedAt:           now.Add(2 * time.Second), PublishedAt: now.Add(3 * time.Second),
	}
	for name, value := range map[string]any{
		"delivery-request.schema.json":  request,
		"evidence-manifest.schema.json": mustDecodeEvidenceManifest(t, evidenceManifest),
		"validation-report.schema.json": report,
		"delivery-receipt.schema.json":  receipt,
	} {
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err := registry.ValidateJSON(name, data); err != nil {
			t.Fatalf("%s rejected canonical value: %v", name, err)
		}
	}
	if err := ValidateDeliveryRequest(request); err != nil {
		t.Fatalf("valid delivery request rejected: %v", err)
	}
	if err := ValidateValidationReport(report); err != nil {
		t.Fatalf("valid validation report rejected: %v", err)
	}
	oldReport := report
	oldReport.SchemaVersion = "1.0"
	if err := ValidateValidationReport(oldReport); err == nil {
		t.Fatal("validation report with superseded schema version passed")
	}
	oldReceipt := receipt
	oldReceipt.SchemaVersion = "1.0"
	if err := ValidateDeliveryPublication(request, report, evidenceManifest, oldReceipt); err == nil {
		t.Fatal("delivery receipt with superseded schema version passed")
	}
	failedReport := report
	failedReport.Status = "failed"
	failedReport.CleanCheckout = false
	failedReport.Checks = append([]ValidationCheck(nil), report.Checks...)
	failedReport.Checks[0].Status = "failed"
	if err := ValidateValidationReport(failedReport); err != nil {
		t.Fatalf("truthful failed validation report rejected: %v", err)
	}
	failedJSON, err := json.Marshal(failedReport)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateJSON("validation-report.schema.json", failedJSON); err != nil {
		t.Fatalf("truthful failed validation report rejected by schema: %v", err)
	}
	if err := ValidateDeliveryPublication(request, report, evidenceManifest, receipt); err != nil {
		t.Fatalf("valid delivery receipt rejected: %v", err)
	}
	pathTransitionRequest := request
	pathTransitionRequest.WriteScopes = []string{"config"}
	pathTransitionReceipt := receipt
	pathTransitionReceipt.ChangedPaths = []string{"config", "config/settings.json"}
	pathTransitionReceipt.LeaseFences = []DeliveryLeaseFence{
		{ResourceType: "artifact", ResourceID: "evidence", OwnerID: "delivery-service", FencingToken: 4},
		{ResourceType: "file", ResourceID: "config", OwnerID: "delivery-service", FencingToken: 7},
		{ResourceType: "worktree", ResourceID: request.DeliveryID, OwnerID: "delivery-service", FencingToken: 2},
	}
	if err := ValidateDeliveryPublication(pathTransitionRequest, report, evidenceManifest, pathTransitionReceipt); err != nil {
		t.Fatalf("legitimate file-directory path transition rejected: %v", err)
	}
	withoutIndependent := report
	withoutIndependent.Checks = append([]ValidationCheck(nil), report.Checks[1:]...)
	if err := ValidateValidationReport(withoutIndependent); err == nil {
		t.Fatal("validation report without an independent check passed")
	}
	traversalReceipt := receipt
	traversalReceipt.ValidationReportRef = "../../validation.json#sha256=" + strings.Repeat("e", 64)
	if err := ValidateDeliveryPublication(request, report, evidenceManifest, traversalReceipt); err == nil {
		t.Fatal("delivery receipt with traversal evidence passed semantic validation")
	}
	data, err := json.Marshal(traversalReceipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateJSON("delivery-receipt.schema.json", data); err == nil {
		t.Fatal("delivery receipt with traversal evidence passed canonical schema")
	}
	invalidWorktreeFence := receipt
	invalidWorktreeFence.LeaseFences = append([]DeliveryLeaseFence(nil), receipt.LeaseFences...)
	invalidWorktreeFence.LeaseFences[3].ResourceID = "delivery_ffffffff-ffff-4fff-8fff-ffffffffffff"
	if err := ValidateDeliveryPublication(request, report, evidenceManifest, invalidWorktreeFence); err == nil {
		t.Fatal("delivery receipt with unrelated worktree fence passed")
	}
	invalidFileFence := receipt
	invalidFileFence.LeaseFences = append([]DeliveryLeaseFence(nil), receipt.LeaseFences...)
	invalidFileFence.LeaseFences[1].ResourceID = "../escape"
	if err := ValidateDeliveryPublication(request, report, evidenceManifest, invalidFileFence); err == nil {
		t.Fatal("delivery receipt with traversal file fence passed")
	}
	wrongReport := report
	wrongReport.DeliveryID = "delivery_ffffffff-ffff-4fff-8fff-ffffffffffff"
	if err := ValidateDeliveryPublication(request, wrongReport, evidenceManifest, receipt); err == nil {
		t.Fatal("delivery receipt linked to another validation report passed")
	}
	uncoveredReceipt := receipt
	uncoveredReceipt.ChangedPaths = []string{"cmd/escape.go"}
	if err := ValidateDeliveryPublication(request, report, evidenceManifest, uncoveredReceipt); err == nil {
		t.Fatal("delivery receipt with an unfenced changed path passed")
	}
	omittedValidatorReport := report
	omittedValidatorReport.Checks = append([]ValidationCheck(nil), report.Checks[:1]...)
	omittedValidatorReport.Checks = append(omittedValidatorReport.Checks, report.Checks[2:]...)
	omittedValidatorJSON, err := json.Marshal(omittedValidatorReport)
	if err != nil {
		t.Fatal(err)
	}
	omittedValidatorReceipt := receipt
	omittedValidatorReceipt.ValidationReportRef = "evidence/attempt-1/validation.json#sha256=" + sha256Hex(omittedValidatorJSON)
	omittedValidatorManifest := canonicalEvidenceManifest(t, request, omittedValidatorJSON)
	omittedValidatorReceipt.EvidenceManifestRef = "evidence/attempt-1/manifest.json#sha256=" + sha256Hex(omittedValidatorManifest)
	if err := ValidateDeliveryPublication(request, omittedValidatorReport, omittedValidatorManifest, omittedValidatorReceipt); err == nil {
		t.Fatal("delivery receipt with an omitted approved validator passed")
	}
	spoofedValidatorReport := report
	spoofedValidatorReport.Checks = append([]ValidationCheck(nil), report.Checks...)
	for index := range spoofedValidatorReport.Checks {
		if spoofedValidatorReport.Checks[index].CheckID == "go-format" {
			spoofedValidatorReport.Checks[index].Kind = "built_in"
		}
	}
	spoofedValidatorJSON, err := json.Marshal(spoofedValidatorReport)
	if err != nil {
		t.Fatal(err)
	}
	spoofedValidatorReceipt := receipt
	spoofedValidatorReceipt.ValidationReportRef = "evidence/attempt-1/validation.json#sha256=" + sha256Hex(spoofedValidatorJSON)
	spoofedValidatorManifest := canonicalEvidenceManifest(t, request, spoofedValidatorJSON)
	spoofedValidatorReceipt.EvidenceManifestRef = "evidence/attempt-1/manifest.json#sha256=" + sha256Hex(spoofedValidatorManifest)
	if err := ValidateDeliveryPublication(request, spoofedValidatorReport, spoofedValidatorManifest, spoofedValidatorReceipt); err == nil {
		t.Fatal("delivery receipt with a spoofed validator check kind passed")
	}
	missingBuiltInReport := report
	missingBuiltInReport.Checks = removeValidationCheck(report.Checks, "filesystem-safety")
	missingBuiltInJSON, err := json.Marshal(missingBuiltInReport)
	if err != nil {
		t.Fatal(err)
	}
	missingBuiltInManifest := canonicalEvidenceManifest(t, request, missingBuiltInJSON)
	missingBuiltInReceipt := receipt
	missingBuiltInReceipt.ValidationReportRef = "evidence/attempt-1/validation.json#sha256=" + sha256Hex(missingBuiltInJSON)
	missingBuiltInReceipt.EvidenceManifestRef = "evidence/attempt-1/manifest.json#sha256=" + sha256Hex(missingBuiltInManifest)
	if err := ValidateDeliveryPublication(request, missingBuiltInReport, missingBuiltInManifest, missingBuiltInReceipt); err == nil {
		t.Fatal("delivery receipt without a mandatory built-in check passed")
	}
	outOfScopeManifest := mustDecodeEvidenceManifest(t, evidenceManifest)
	outOfScopeManifest.Entries = append(outOfScopeManifest.Entries, EvidenceEntry{
		Path: "outside/secret.txt", SHA256: emptyDigest, SizeBytes: 0, MediaType: "text/plain",
	})
	outOfScopeManifestBytes, err := json.Marshal(outOfScopeManifest)
	if err != nil {
		t.Fatal(err)
	}
	outOfScopeManifestReceipt := receipt
	outOfScopeManifestReceipt.EvidenceManifestRef = "evidence/attempt-1/manifest.json#sha256=" + sha256Hex(outOfScopeManifestBytes)
	if err := ValidateDeliveryPublication(request, report, outOfScopeManifestBytes, outOfScopeManifestReceipt); err == nil {
		t.Fatal("delivery receipt with out-of-scope manifest evidence passed")
	}
	previousCommit := strings.Repeat("9", 40)
	unexpectedPreviousReceipt := receipt
	unexpectedPreviousReceipt.PublicationPreviousCommit = &previousCommit
	if err := ValidateDeliveryPublication(request, report, evidenceManifest, unexpectedPreviousReceipt); err == nil {
		t.Fatal("delivery receipt with an unapproved previous commit passed")
	}
}

func passingValidationCheck(
	checkID string,
	kind string,
	now time.Time,
	exitCode *int,
	emptyDigest string,
) ValidationCheck {
	return ValidationCheck{
		CheckID: checkID, Kind: kind, Status: "passed",
		StartedAt: now, FinishedAt: now.Add(time.Second), DurationMS: 1000,
		ExitCode: exitCode, StdoutSHA256: emptyDigest, StderrSHA256: emptyDigest,
	}
}

func canonicalEvidenceManifest(t *testing.T, request DeliveryRequest, canonicalReport []byte) []byte {
	t.Helper()
	manifest := EvidenceManifest{
		DeliveryID: request.DeliveryID, TenantID: request.TenantID,
		RepositoryID: request.RepositoryID, SchemaVersion: DeliverySchemaVersion,
		Entries: []EvidenceEntry{{
			Path: "evidence/attempt-1/validation.json", SHA256: sha256Hex(canonicalReport),
			SizeBytes: int64(len(canonicalReport)), MediaType: "application/json",
		}},
	}
	content, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func mustDecodeEvidenceManifest(t *testing.T, content []byte) EvidenceManifest {
	t.Helper()
	var manifest EvidenceManifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func removeValidationCheck(checks []ValidationCheck, checkID string) []ValidationCheck {
	filtered := make([]ValidationCheck, 0, len(checks)-1)
	for _, check := range checks {
		if check.CheckID != checkID {
			filtered = append(filtered, check)
		}
	}
	return filtered
}

func TestDeliverySemanticValidationFailsClosed(t *testing.T) {
	t.Parallel()
	base := validDeliveryRequest()
	for name, mutate := range map[string]func(*DeliveryRequest){
		"superseded schema version": func(value *DeliveryRequest) { value.SchemaVersion = "1.0" },
		"invalid tenant identity":   func(value *DeliveryRequest) { value.TenantID = "tenant" },
		"invalid repository identity": func(value *DeliveryRequest) {
			value.RepositoryID = "repository"
		},
		"self validation":          func(value *DeliveryRequest) { value.ValidatorID = value.AuthorID },
		"unscoped evidence":        func(value *DeliveryRequest) { value.EvidenceScope = "other/evidence" },
		"injected publication ref": func(value *DeliveryRequest) { value.PublicationRef += "/other" },
		"short lease":              func(value *DeliveryRequest) { value.LeaseTTLMS = value.WorkerBudgets.WallClockMS },
		"path traversal":           func(value *DeliveryRequest) { value.WriteScopes = []string{"../escape"} },
		"unclean repository root": func(value *DeliveryRequest) {
			value.RepositoryPath = "/srv/repos/../sensitive"
		},
		"filesystem root worktrees": func(value *DeliveryRequest) { value.WorktreeRoot = "/" },
		"nested worktree root":      func(value *DeliveryRequest) { value.WorktreeRoot = "/srv/repos/forja/worktrees" },
		"overlapping scopes": func(value *DeliveryRequest) {
			value.WriteScopes = []string{"internal", "internal/delivery"}
		},
		"write artifact overlap": func(value *DeliveryRequest) {
			value.WriteScopes = []string{"evidence/attempt-1/result.json"}
		},
		"unsorted validators": func(value *DeliveryRequest) {
			value.MechanicalValidatorIDs = []string{"go-test", "go-format"}
		},
		"duplicate validators": func(value *DeliveryRequest) {
			value.MechanicalValidatorIDs = []string{"go-format", "go-format"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := base
			value.ReadScopes = append([]string(nil), base.ReadScopes...)
			value.WriteScopes = append([]string(nil), base.WriteScopes...)
			value.ArtifactScopes = append([]string(nil), base.ArtifactScopes...)
			value.MechanicalValidatorIDs = append([]string(nil), base.MechanicalValidatorIDs...)
			mutate(&value)
			if err := ValidateDeliveryRequest(value); err == nil {
				t.Fatal("invalid delivery request passed semantic validation")
			}
		})
	}
}

func validDeliveryRequest() DeliveryRequest {
	return DeliveryRequest{
		DeliveryID:    "delivery_00010203-0405-4607-8809-0a0b0c0d0e0f",
		TenantID:      "tenant_00000000-0000-4000-8000-000000000001",
		RepositoryID:  "repo_00000000-0000-4000-8000-000000000002",
		TaskID:        "task_11111111-2222-4333-8444-555555555555",
		AttemptID:     "attempt_22222222-3333-4444-8555-666666666666",
		RunID:         "run_aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		SchemaVersion: DeliverySchemaVersion, RepositoryPath: "/srv/repos/forja",
		WorktreeRoot: "/srv/worktrees", BaseCommit: strings.Repeat("a", 40),
		PublicationRef:            "refs/forja/deliveries/delivery_00010203-0405-4607-8809-0a0b0c0d0e0f",
		PublicationPreviousCommit: nil,
		AuthorID:                  "author-agent", ValidatorID: "independent-validator",
		Role: "implementer", Objective: "Implement one bounded synthetic change",
		ReadScopes: []string{"."}, WriteScopes: []string{"internal/delivery"},
		ArtifactScopes: []string{"evidence"}, EvidenceScope: "evidence/attempt-1",
		AttemptOrdinal: 1,
		WorkerBudgets: WorkerBudgets{
			WallClockMS: 1000, InactivityMS: 500, MaxOutputBytes: 4096,
			CancellationGraceMS: 100, MaxRetries: 1,
		},
		MechanicalValidatorIDs: []string{"go-format", "go-test"},
		LeaseTTLMS:             5000,
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
