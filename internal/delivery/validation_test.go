package delivery

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

func TestValidationServiceProducesIndependentCanonicalEvidence(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	evidenceRoot := t.TempDir()
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	worktree, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(worktree.Path, "internal/generated/value.json"),
		[]byte("{\"value\":1}\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	result, err := manager.CreateResultCommit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	service := testValidationService(t, manager, evidenceRoot, []ValidatorDefinition{
		testValidatorDefinition(t, "unit-tests", "pass"),
	}, nil)
	bundle, err := service.Validate(t.Context(), request, result)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Report.Status != "passed" || !bundle.Report.CleanCheckout ||
		bundle.Report.AuthorID == bundle.Report.ValidatorID {
		t.Fatalf("validation report = %#v", bundle.Report)
	}
	wantIDs := []string{
		"clean-checkout", "filesystem-safety", "generated-file-policy",
		"mechanical-preflight", "schema-validation", "scope-boundary",
		"secret-scan", "unit-tests",
	}
	gotIDs := make([]string, 0, len(bundle.Report.Checks))
	for _, check := range bundle.Report.Checks {
		gotIDs = append(gotIDs, check.CheckID)
		if check.Status != "passed" {
			t.Fatalf("check failed: %#v", check)
		}
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("check IDs = %q, want %q", gotIDs, wantIDs)
	}
	if !strings.HasPrefix(bundle.ReportRef, request.EvidenceScope+"/validation.json#sha256=") ||
		!strings.HasPrefix(bundle.ManifestRef, request.EvidenceScope+"/manifest.json#sha256=") {
		t.Fatalf("evidence refs = %q %q", bundle.ReportRef, bundle.ManifestRef)
	}
	mechanicalCheckCount := len(bundle.Report.Checks) - 1
	wantManifestEntries := 2 + 2*(mechanicalCheckCount+len(bundle.Report.Checks))
	if len(bundle.Manifest.Entries) != wantManifestEntries {
		t.Fatalf("manifest entries = %d", len(bundle.Manifest.Entries))
	}
	if content, err := os.ReadFile(filepath.Join(bundle.PhysicalRoot, "evidence", "validation.json")); err != nil || !slices.Equal(content, bundle.ReportJSON) {
		t.Fatalf("persisted report mismatch: err=%v", err)
	}
	var decoded contracts.ValidationReport
	if err := json.Unmarshal(bundle.ReportJSON, &decoded); err != nil || decoded.ResultCommit != result.ResultCommit {
		t.Fatalf("canonical report could not be decoded: %#v err=%v", decoded, err)
	}
	validationCheckout := filepath.Join(root, ".forja-validation", request.DeliveryID, request.AttemptID)
	if _, err := os.Lstat(validationCheckout); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clean independent checkout remains: %v", err)
	}
	if head := strings.TrimSpace(runGitTest(t, worktree.Path, "rev-parse", "HEAD")); head != base {
		t.Fatalf("author checkout HEAD changed to %s", head)
	}
}

func TestValidationServiceLoadsExactPersistedEvidenceForRecovery(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	evidenceRoot := t.TempDir()
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	worktree, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(worktree.Path, "internal/generated/recovery.json"),
		[]byte("{\"recovery\":true}\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	result, err := manager.CreateResultCommit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	service := testValidationService(t, manager, evidenceRoot, []ValidatorDefinition{
		testValidatorDefinition(t, "unit-tests", "pass"),
	}, nil)
	original, err := service.Validate(t.Context(), request, result)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := service.Load(t.Context(), request)
	if err != nil {
		t.Fatalf("load persisted evidence: %v", err)
	}
	if !slices.Equal(loaded.ReportJSON, original.ReportJSON) ||
		!slices.Equal(loaded.ManifestJSON, original.ManifestJSON) ||
		loaded.ReportRef != original.ReportRef || loaded.ManifestRef != original.ManifestRef ||
		loaded.PhysicalRoot != original.PhysicalRoot {
		t.Fatalf("loaded bundle differs from persisted authority")
	}
	if err := os.Remove(filepath.Join(loaded.PhysicalRoot, "evidence", "validation.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Load(t.Context(), request); err == nil {
		t.Fatal("incomplete persisted evidence was accepted during recovery")
	}
}

func TestValidationServiceRejectsMisplacedEvidenceFromAnotherAuthority(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	evidenceRoot := t.TempDir()
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	worktree, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	writeValidationFixture(t, worktree.Path, "authority.txt", "bound\n")
	result, err := manager.CreateResultCommit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	service := testValidationService(t, manager, evidenceRoot, []ValidatorDefinition{
		testValidatorDefinition(t, "unit-tests", "pass"),
	}, nil)
	original, err := service.Validate(t.Context(), request, result)
	if err != nil {
		t.Fatal(err)
	}

	misbound := request
	misbound.DeliveryID = "delivery_00000000-0000-4000-8000-000000000091"
	misbound.AttemptID = "attempt_00000000-0000-4000-8000-000000000092"
	misbound.TaskID = "task_00000000-0000-4000-8000-000000000093"
	misbound.PublicationRef = "refs/forja/deliveries/" + misbound.DeliveryID
	destination := filepath.Join(evidenceRoot, misbound.DeliveryID, misbound.AttemptID)
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(original.PhysicalRoot, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Load(t.Context(), misbound); err == nil ||
		!strings.Contains(err.Error(), "authority envelope") {
		t.Fatalf("misplaced evidence load error = %v", err)
	}
}

func TestValidationServiceRejectsEvidenceCopiedFromAnotherAttempt(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	evidenceRoot := t.TempDir()
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	worktree, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	writeValidationFixture(t, worktree.Path, "attempt.txt", "bound\n")
	result, err := manager.CreateResultCommit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	service := testValidationService(t, manager, evidenceRoot, []ValidatorDefinition{
		testValidatorDefinition(t, "unit-tests", "pass"),
	}, nil)
	original, err := service.Validate(t.Context(), request, result)
	if err != nil {
		t.Fatal(err)
	}

	otherAttempt := request
	otherAttempt.AttemptID = "attempt_00000000-0000-4000-8000-000000000094"
	otherAttempt.AttemptOrdinal = 2
	destination := filepath.Join(
		evidenceRoot, otherAttempt.DeliveryID, otherAttempt.AttemptID,
	)
	if err := os.Rename(original.PhysicalRoot, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Load(t.Context(), otherAttempt); err == nil ||
		!strings.Contains(err.Error(), "approved attempt") {
		t.Fatalf("cross-attempt evidence load error = %v", err)
	}
}

func TestValidationServiceFailsClosedOnMandatoryChecks(t *testing.T) {
	cases := map[string]struct {
		prepare  func(*testing.T, string)
		bindings []SchemaBinding
		failedID string
	}{
		"secret": {
			prepare: func(t *testing.T, path string) {
				writeValidationFixture(t, path, "secret.txt", "hf_"+strings.Repeat("a", 24)+"\n")
			},
			failedID: "secret-scan",
		},
		"duplicate JSON key": {
			prepare: func(t *testing.T, path string) {
				writeValidationFixture(t, path, "value.json", "{\"a\":1,\"a\":2}\n")
			},
			failedID: "schema-validation",
		},
		"registered schema": {
			prepare: func(t *testing.T, path string) {
				writeValidationFixture(t, path, "value.json", "{}\n")
			},
			bindings: []SchemaBinding{{
				Path: "internal/generated/value.json", SchemaName: "run.schema.json",
			}},
			failedID: "schema-validation",
		},
		"generated content": {
			prepare: func(t *testing.T, path string) {
				writeValidationFixture(t, path, "generated.go", "// Code generated by fixture. DO NOT EDIT.\n")
			},
			failedID: "generated-file-policy",
		},
	}
	for name, fixture := range cases {
		t.Run(name, func(t *testing.T) {
			repository, root, base := deliveryRepository(t)
			manager := testWorktreeManager(t)
			request := deliveryRequest(repository, root, base)
			worktree, err := manager.Prepare(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			fixture.prepare(t, worktree.Path)
			result, err := manager.CreateResultCommit(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			service := testValidationService(t, manager, t.TempDir(), []ValidatorDefinition{
				testValidatorDefinition(t, "unit-tests", "pass"),
			}, fixture.bindings)
			bundle, err := service.Validate(t.Context(), request, result)
			if err != nil {
				t.Fatal(err)
			}
			if bundle.Report.Status != "failed" {
				t.Fatalf("unsafe result passed: %#v", bundle.Report)
			}
			failed := false
			configuredRan := false
			for _, check := range bundle.Report.Checks {
				failed = failed || (check.CheckID == fixture.failedID && check.Status == "failed")
				configuredRan = configuredRan || check.CheckID == "unit-tests"
			}
			if !failed || configuredRan {
				t.Fatalf("mandatory failure=%v configuredRan=%v checks=%#v", failed, configuredRan, bundle.Report.Checks)
			}
		})
	}
}

func TestValidationServiceRecompilesUnchangedSchemaDependents(t *testing.T) {
	repository, root, _ := deliveryRepository(t)
	directory := filepath.Join(repository, "internal/generated")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	dependent := `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"https://example.test/a.schema.json","type":"object","properties":{"value":{"$ref":"https://example.test/b.schema.json#/$defs/value"}}}` + "\n"
	dependency := `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"https://example.test/b.schema.json","$defs":{"value":{"type":"string"}}}` + "\n"
	if err := os.WriteFile(filepath.Join(directory, "a.schema.json"), []byte(dependent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "b.schema.json"), []byte(dependency), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "internal/generated/a.schema.json", "internal/generated/b.schema.json")
	runGitTest(t, repository, "commit", "--quiet", "-m", "schema fixtures")
	base := strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD"))
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	worktree, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	brokenDependency := `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"https://example.test/b.schema.json","type":"object"}` + "\n"
	if err := os.WriteFile(
		filepath.Join(worktree.Path, "internal/generated/b.schema.json"),
		[]byte(brokenDependency), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	result, err := manager.CreateResultCommit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	service := testValidationService(t, manager, t.TempDir(), []ValidatorDefinition{
		testValidatorDefinition(t, "unit-tests", "pass"),
	}, nil)
	bundle, err := service.Validate(t.Context(), request, result)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range bundle.Report.Checks {
		if check.CheckID == "schema-validation" && check.Status == "failed" {
			return
		}
	}
	t.Fatalf("broken unchanged schema dependent was accepted: %#v", bundle.Report.Checks)
}

func TestValidationServiceDetectsValidatorMutationAndPreservesCheckout(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	worktree, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	writeValidationFixture(t, worktree.Path, "value.txt", "safe\n")
	result, err := manager.CreateResultCommit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	service := testValidationService(t, manager, t.TempDir(), []ValidatorDefinition{
		testValidatorDefinition(t, "unit-tests", "mutate"),
	}, nil)
	bundle, err := service.Validate(t.Context(), request, result)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Report.Status != "failed" {
		t.Fatal("validator-mutated checkout passed validation")
	}
	if bundle.Report.CleanCheckout {
		t.Fatal("validator-mutated checkout reported clean checkout")
	}
	for _, check := range bundle.Report.Checks {
		if check.CheckID == "clean-checkout" && check.Status != "failed" {
			t.Fatalf("final clean-checkout result = %#v", check)
		}
	}
	quarantine := filepath.Join(
		root, ".forja-validation-quarantine", request.DeliveryID, request.AttemptID,
	)
	if content, err := os.ReadFile(filepath.Join(quarantine, "validator-mutated.txt")); err != nil || string(content) != "mutated\n" {
		t.Fatalf("mutated validation evidence was not preserved: %q err=%v", content, err)
	}
}

func TestValidationServiceRejectsMissingValidatorAndChangedEvidence(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	worktree, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	writeValidationFixture(t, worktree.Path, "value.txt", "safe\n")
	result, err := manager.CreateResultCommit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	emptyRegistry, err := NewValidatorRegistry(nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	missingService, err := NewValidationService(manager, emptyRegistry, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missingService.Validate(t.Context(), request, result); err == nil ||
		!strings.Contains(err.Error(), "not registered") {
		t.Fatalf("missing validator error = %v", err)
	}

	evidenceRoot := t.TempDir()
	service := testValidationService(t, manager, evidenceRoot, []ValidatorDefinition{
		testValidatorDefinition(t, "unit-tests", "pass"),
	}, nil)
	bundle, err := service.Validate(t.Context(), request, result)
	if err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(bundle.PhysicalRoot, "evidence", "validation.json")
	if err := os.WriteFile(reportPath, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Validate(t.Context(), request, result); err == nil ||
		!strings.Contains(err.Error(), "existing evidence changed") {
		t.Fatalf("changed evidence replay error = %v", err)
	}
}

func TestValidationServiceRejectsTamperedResultIdentity(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	worktree, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	writeValidationFixture(t, worktree.Path, "value.txt", "safe\n")
	result, err := manager.CreateResultCommit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	service := testValidationService(t, manager, t.TempDir(), []ValidatorDefinition{
		testValidatorDefinition(t, "unit-tests", "pass"),
	}, nil)
	tampered := result
	tampered.PatchSHA256 = strings.Repeat("0", 64)
	if _, err := service.Validate(t.Context(), request, tampered); err == nil ||
		!strings.Contains(err.Error(), "disagrees with Git") {
		t.Fatalf("tampered result identity error = %v", err)
	}
	tampered = result
	tampered.ResultCommit = "--all"
	if _, err := service.Validate(t.Context(), request, tampered); err == nil ||
		!strings.Contains(err.Error(), "40-character object ID") {
		t.Fatalf("noncanonical result commit error = %v", err)
	}
}

func TestValidationServicePreservesCheckoutWithChangedHead(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	worktree, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	writeValidationFixture(t, worktree.Path, "value.txt", "safe\n")
	result, err := manager.CreateResultCommit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	service := testValidationService(t, manager, t.TempDir(), []ValidatorDefinition{
		testValidatorDefinition(t, "unit-tests", "move-head"),
	}, nil)
	bundle, err := service.Validate(t.Context(), request, result)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Report.Status != "failed" {
		t.Fatal("validator-changed HEAD passed validation")
	}
	for _, namespace := range []string{
		".forja-mechanical-quarantine", ".forja-validation-quarantine",
	} {
		quarantine := filepath.Join(root, namespace, request.DeliveryID, request.AttemptID)
		if head := strings.TrimSpace(runGitTest(t, quarantine, "rev-parse", "HEAD")); head != base {
			t.Fatalf("%s preserved HEAD = %s, want %s", namespace, head, base)
		}
	}
}

func TestValidationServicePersistsBoundedOutputFailureFromBothLanes(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	worktree, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	writeValidationFixture(t, worktree.Path, "value.txt", "safe\n")
	result, err := manager.CreateResultCommit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	service := testValidationService(t, manager, t.TempDir(), []ValidatorDefinition{{
		ID:      "unit-tests",
		Argv:    []string{executable, "-test.run=TestValidatorHelperProcess", "--", "overflow"},
		Timeout: 5 * time.Second, MaxOutputBytes: 1024,
	}}, nil)
	bundle, err := service.Validate(t.Context(), request, result)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Report.Status != "failed" {
		t.Fatal("configured output overflow passed independent validation")
	}
	for _, lane := range []string{"mechanical", "independent"} {
		content, err := os.ReadFile(filepath.Join(
			bundle.PhysicalRoot, "evidence", lane, "checks", "unit-tests.stdout",
		))
		if err != nil || len(content) != 1024 {
			t.Fatalf("%s bounded output bytes=%d err=%v", lane, len(content), err)
		}
	}
}

func TestValidationServiceRejectsSymlinkedEvidenceNamespace(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	worktree, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	writeValidationFixture(t, worktree.Path, "value.txt", "safe\n")
	result, err := manager.CreateResultCommit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	evidenceRoot := t.TempDir()
	escape := t.TempDir()
	if err := os.Symlink(escape, filepath.Join(evidenceRoot, request.DeliveryID)); err != nil {
		t.Fatal(err)
	}
	service := testValidationService(t, manager, evidenceRoot, []ValidatorDefinition{
		testValidatorDefinition(t, "unit-tests", "pass"),
	}, nil)
	if _, err := service.Validate(t.Context(), request, result); err == nil {
		t.Fatalf("symlinked evidence namespace error = %v", err)
	}
	entries, err := os.ReadDir(escape)
	if err != nil || len(entries) != 0 {
		t.Fatalf("evidence escaped operator root: entries=%v err=%v", entries, err)
	}
}

func testValidationService(
	t *testing.T,
	manager *WorktreeManager,
	evidenceRoot string,
	definitions []ValidatorDefinition,
	bindings []SchemaBinding,
) *ValidationService {
	t.Helper()
	registry, err := NewValidatorRegistry(definitions, bindings, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("close validator registry: %v", err)
		}
	})
	service, err := NewValidationService(manager, registry, evidenceRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testValidatorDefinition(t *testing.T, id string, mode string) ValidatorDefinition {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return ValidatorDefinition{
		ID: id,
		Argv: []string{
			executable, "-test.run=TestValidatorHelperProcess", "--", mode,
		},
		Timeout: 5 * time.Second, MaxOutputBytes: 4096,
	}
}

func writeValidationFixture(t *testing.T, worktree string, name string, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(worktree, "internal/generated", name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
