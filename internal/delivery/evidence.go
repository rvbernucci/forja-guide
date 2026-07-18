package delivery

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

const maximumValidationEvidenceBytes = 256 << 20

type evidenceContent struct {
	logicalPath string
	content     []byte
	mediaType   string
}

func (s *ValidationService) persistEvidence(
	request contracts.DeliveryRequest,
	report contracts.ValidationReport,
	independentExecutions []validationExecution,
	mechanicalExecutions []validationExecution,
) (ValidationBundle, error) {
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return ValidationBundle{}, fmt.Errorf("marshal canonical validation report: %w", err)
	}
	if err := s.schemaRegistry.ValidateJSON("validation-report.schema.json", reportJSON); err != nil {
		return ValidationBundle{}, fmt.Errorf("validate canonical validation report: %w", err)
	}
	reportPath := path.Join(request.EvidenceScope, "validation.json")
	manifestPath := path.Join(request.EvidenceScope, "manifest.json")
	mechanicalChecks := make([]contracts.ValidationCheck, 0, len(mechanicalExecutions))
	mechanicalStatus := "passed"
	for _, execution := range mechanicalExecutions {
		mechanicalChecks = append(mechanicalChecks, execution.check)
		if execution.check.Status != "passed" {
			mechanicalStatus = "failed"
		}
	}
	mechanicalDocument := struct {
		SchemaVersion string                      `json:"schema_version"`
		Status        string                      `json:"status"`
		Checks        []contracts.ValidationCheck `json:"checks"`
	}{"1.0", mechanicalStatus, mechanicalChecks}
	mechanicalJSON, err := json.Marshal(mechanicalDocument)
	if err != nil {
		return ValidationBundle{}, fmt.Errorf("marshal mechanical validation evidence: %w", err)
	}
	files := []evidenceContent{
		{reportPath, reportJSON, "application/json"},
		{path.Join(request.EvidenceScope, "mechanical-validation.json"), mechanicalJSON, "application/json"},
	}
	allExecutions := append([]validationExecution(nil), mechanicalExecutions...)
	allExecutions = append(allExecutions, independentExecutions...)
	for _, execution := range allExecutions {
		files = append(files,
			evidenceContent{
				path.Join(request.EvidenceScope, execution.lane, "checks", execution.check.CheckID+".stdout"),
				execution.stdout, "text/plain; charset=utf-8",
			},
			evidenceContent{
				path.Join(request.EvidenceScope, execution.lane, "checks", execution.check.CheckID+".stderr"),
				execution.stderr, "text/plain; charset=utf-8",
			},
		)
	}
	slices.SortFunc(files, func(left evidenceContent, right evidenceContent) int {
		return strings.Compare(left.logicalPath, right.logicalPath)
	})
	entries := make([]contracts.EvidenceEntry, 0, len(files))
	totalBytes := 0
	for index, file := range files {
		if index > 0 && files[index-1].logicalPath == file.logicalPath {
			return ValidationBundle{}, fmt.Errorf("duplicate evidence path %q", file.logicalPath)
		}
		if err := validateRepositoryRelativePath(file.logicalPath); err != nil {
			return ValidationBundle{}, fmt.Errorf("evidence path: %w", err)
		}
		if !pathCoveredByScopes(file.logicalPath, []string{request.EvidenceScope}) {
			return ValidationBundle{}, fmt.Errorf("evidence path %q is outside its approved scope", file.logicalPath)
		}
		if totalBytes > maximumValidationEvidenceBytes-len(file.content) {
			return ValidationBundle{}, fmt.Errorf("validation evidence exceeds its aggregate budget")
		}
		totalBytes += len(file.content)
		digest := sha256.Sum256(file.content)
		entries = append(entries, contracts.EvidenceEntry{
			Path: file.logicalPath, SHA256: fmt.Sprintf("%x", digest),
			SizeBytes: int64(len(file.content)), MediaType: file.mediaType,
		})
	}
	manifest := contracts.EvidenceManifest{
		DeliveryID: request.DeliveryID, SchemaVersion: "1.0", Entries: entries,
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return ValidationBundle{}, fmt.Errorf("marshal canonical evidence manifest: %w", err)
	}
	if err := s.schemaRegistry.ValidateJSON("evidence-manifest.schema.json", manifestJSON); err != nil {
		return ValidationBundle{}, fmt.Errorf("validate canonical evidence manifest: %w", err)
	}
	files = append(files, evidenceContent{manifestPath, manifestJSON, "application/json"})
	if totalBytes > maximumValidationEvidenceBytes-len(manifestJSON) {
		return ValidationBundle{}, fmt.Errorf("validation evidence exceeds its aggregate budget")
	}

	physicalRoot, err := s.writeEvidenceAtomically(request, files)
	if err != nil {
		return ValidationBundle{}, err
	}
	reportDigest := sha256.Sum256(reportJSON)
	manifestDigest := sha256.Sum256(manifestJSON)
	return ValidationBundle{
		Report: report, ReportJSON: reportJSON,
		Manifest: manifest, ManifestJSON: manifestJSON,
		ReportRef:    reportPath + "#sha256=" + fmt.Sprintf("%x", reportDigest),
		ManifestRef:  manifestPath + "#sha256=" + fmt.Sprintf("%x", manifestDigest),
		PhysicalRoot: physicalRoot,
	}, nil
}

func (s *ValidationService) writeEvidenceAtomically(
	request contracts.DeliveryRequest,
	files []evidenceContent,
) (string, error) {
	stagingRoot := filepath.Join(s.evidenceRoot, ".staging")
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return "", fmt.Errorf("create evidence staging root: %w", err)
	}
	if physical, err := canonicalDirectory(stagingRoot, "evidence staging root"); err != nil {
		return "", err
	} else if physical != stagingRoot {
		return "", fmt.Errorf("evidence staging root traverses a symlink")
	}
	stage, err := os.MkdirTemp(stagingRoot, request.AttemptID+"-")
	if err != nil {
		return "", fmt.Errorf("create evidence stage: %w", err)
	}
	defer os.RemoveAll(stage)
	for _, file := range files {
		physical := filepath.Join(stage, filepath.FromSlash(file.logicalPath))
		if err := os.MkdirAll(filepath.Dir(physical), 0o700); err != nil {
			return "", fmt.Errorf("create evidence parent: %w", err)
		}
		handle, err := os.OpenFile(physical, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return "", fmt.Errorf("create evidence file %q: %w", file.logicalPath, err)
		}
		if _, err := handle.Write(file.content); err != nil {
			_ = handle.Close()
			return "", fmt.Errorf("write evidence file %q: %w", file.logicalPath, err)
		}
		if err := handle.Sync(); err != nil {
			_ = handle.Close()
			return "", fmt.Errorf("sync evidence file %q: %w", file.logicalPath, err)
		}
		if err := handle.Close(); err != nil {
			return "", fmt.Errorf("close evidence file %q: %w", file.logicalPath, err)
		}
	}
	final := filepath.Join(s.evidenceRoot, request.DeliveryID, request.AttemptID)
	if _, err := os.Lstat(final); err == nil {
		if err := verifyEvidenceDirectory(final, files); err != nil {
			return "", fmt.Errorf("existing evidence changed: %w", err)
		}
		return final, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect evidence destination: %w", err)
	}
	parent := filepath.Dir(final)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("create evidence delivery namespace: %w", err)
	}
	if physical, err := canonicalDirectory(parent, "evidence delivery namespace"); err != nil {
		return "", err
	} else if physical != parent {
		return "", fmt.Errorf("evidence delivery namespace traverses a symlink")
	}
	if err := os.Rename(stage, final); err != nil {
		if _, statErr := os.Lstat(final); statErr == nil {
			if verifyErr := verifyEvidenceDirectory(final, files); verifyErr == nil {
				return final, nil
			}
		}
		return "", fmt.Errorf("publish evidence bundle: %w", err)
	}
	if err := verifyEvidenceDirectory(final, files); err != nil {
		return "", fmt.Errorf("verify published evidence bundle: %w", err)
	}
	return final, nil
}

func verifyEvidenceDirectory(root string, files []evidenceContent) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("evidence bundle root is not a real directory")
	}
	physical, err := filepath.EvalSymlinks(root)
	if err != nil || physical != root {
		return fmt.Errorf("evidence bundle root traverses a symlink")
	}
	expected := make(map[string][]byte, len(files))
	for _, file := range files {
		if _, duplicate := expected[file.logicalPath]; duplicate {
			return fmt.Errorf("duplicate expected evidence path %q", file.logicalPath)
		}
		expected[file.logicalPath] = file.content
	}
	seen := make(map[string]struct{}, len(files))
	err = filepath.WalkDir(root, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == root {
			return nil
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		logical := filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("evidence path %q is a symlink", logical)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("evidence path %q is not a regular file", logical)
		}
		want, ok := expected[logical]
		if !ok {
			return fmt.Errorf("unexpected evidence file %q", logical)
		}
		actual, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if !bytes.Equal(actual, want) {
			return fmt.Errorf("evidence file %q has different content", logical)
		}
		seen[logical] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("evidence bundle is missing files")
	}
	return nil
}
