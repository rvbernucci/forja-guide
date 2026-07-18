package delivery

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
		DeliveryID: request.DeliveryID, TenantID: request.TenantID,
		RepositoryID: request.RepositoryID, SchemaVersion: contracts.DeliverySchemaVersion, Entries: entries,
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

// verifyPersistedValidationBundle re-reads the immutable evidence inventory
// from its physical root. In-memory contracts alone cannot authorize a
// publication after the persisted evidence has changed or disappeared.
func verifyPersistedValidationBundle(
	request contracts.DeliveryRequest,
	bundle ValidationBundle,
) error {
	physicalRoot, err := canonicalDirectory(bundle.PhysicalRoot, "persisted validation evidence")
	if err != nil {
		return err
	}
	if physicalRoot != bundle.PhysicalRoot {
		return fmt.Errorf("persisted validation evidence root is not canonical")
	}
	canonicalReport, err := json.Marshal(bundle.Report)
	if err != nil || !bytes.Equal(canonicalReport, bundle.ReportJSON) {
		return fmt.Errorf("persisted validation report bytes are not canonical")
	}
	canonicalManifest, err := json.Marshal(bundle.Manifest)
	if err != nil || !bytes.Equal(canonicalManifest, bundle.ManifestJSON) {
		return fmt.Errorf("persisted evidence manifest bytes are not canonical")
	}
	reportPath, reportDigest, reportBound := strings.Cut(bundle.ReportRef, "#sha256=")
	manifestPath, manifestDigest, manifestBound := strings.Cut(bundle.ManifestRef, "#sha256=")
	reportSHA256 := sha256.Sum256(bundle.ReportJSON)
	manifestSHA256 := sha256.Sum256(bundle.ManifestJSON)
	if !reportBound || !manifestBound ||
		reportDigest != fmt.Sprintf("%x", reportSHA256) ||
		manifestDigest != fmt.Sprintf("%x", manifestSHA256) {
		return fmt.Errorf("persisted validation references disagree with canonical bytes")
	}
	if !pathCoveredByScopes(reportPath, []string{request.EvidenceScope}) ||
		!pathCoveredByScopes(manifestPath, []string{request.EvidenceScope}) {
		return fmt.Errorf("persisted validation references are outside the approved evidence scope")
	}

	expected := make(map[string]contracts.EvidenceEntry, len(bundle.Manifest.Entries))
	totalBytes := int64(len(bundle.ManifestJSON))
	for _, entry := range bundle.Manifest.Entries {
		if _, duplicate := expected[entry.Path]; duplicate || entry.Path == manifestPath {
			return fmt.Errorf("persisted evidence manifest contains a duplicate path %q", entry.Path)
		}
		if entry.SizeBytes < 0 || entry.SizeBytes > maximumValidationEvidenceBytes-totalBytes {
			return fmt.Errorf("persisted validation evidence exceeds its aggregate budget")
		}
		totalBytes += entry.SizeBytes
		expected[entry.Path] = entry
	}
	reportEntry, ok := expected[reportPath]
	if !ok || reportEntry.SizeBytes != int64(len(bundle.ReportJSON)) ||
		reportEntry.SHA256 != reportDigest {
		return fmt.Errorf("persisted validation report is not bound by the evidence manifest")
	}

	root, err := openPinnedEvidenceRoot(physicalRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	return verifyPersistedEvidenceRoot(root, bundle, reportPath, manifestPath, expected)
}

func openPinnedEvidenceRoot(physicalRoot string) (*os.Root, error) {
	expectedIdentity, err := os.Lstat(physicalRoot)
	if err != nil {
		return nil, fmt.Errorf("stat persisted validation evidence before open: %w", err)
	}
	if expectedIdentity.Mode()&os.ModeSymlink != 0 || !expectedIdentity.IsDir() {
		return nil, fmt.Errorf("persisted validation evidence root must remain a real directory")
	}
	root, err := os.OpenRoot(physicalRoot)
	if err != nil {
		return nil, fmt.Errorf("open persisted validation evidence: %w", err)
	}
	openedIdentity, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("stat opened persisted validation evidence: %w", err)
	}
	if err := requireSameEvidenceRoot(expectedIdentity, openedIdentity); err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

func requireSameEvidenceRoot(expected os.FileInfo, opened os.FileInfo) error {
	if !os.SameFile(expected, opened) {
		return fmt.Errorf("persisted validation evidence root changed while it was opened")
	}
	return nil
}

func verifyPersistedEvidenceRoot(
	root *os.Root,
	bundle ValidationBundle,
	reportPath string,
	manifestPath string,
	expected map[string]contracts.EvidenceEntry,
) error {
	seen := make(map[string]struct{}, len(expected)+1)
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		logical := path.Clean(name)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("persisted evidence path %q is a symlink", logical)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("persisted evidence path %q is not a regular file", logical)
		}
		if _, duplicate := seen[logical]; duplicate {
			return fmt.Errorf("persisted evidence path %q was observed twice", logical)
		}
		seen[logical] = struct{}{}
		if logical == manifestPath {
			actual, err := readEvidenceFile(root, logical, int64(len(bundle.ManifestJSON)))
			if err != nil {
				return err
			}
			if !bytes.Equal(actual, bundle.ManifestJSON) {
				return fmt.Errorf("persisted evidence manifest has different content")
			}
			return nil
		}
		expectedEntry, ok := expected[logical]
		if !ok {
			return fmt.Errorf("unexpected persisted evidence file %q", logical)
		}
		actual, err := readEvidenceFile(root, logical, expectedEntry.SizeBytes)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(actual)
		if fmt.Sprintf("%x", digest) != expectedEntry.SHA256 {
			return fmt.Errorf("persisted evidence file %q has a different digest", logical)
		}
		if logical == reportPath && !bytes.Equal(actual, bundle.ReportJSON) {
			return fmt.Errorf("persisted validation report has different content")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(expected)+1 {
		return fmt.Errorf("persisted validation evidence is missing files")
	}
	if _, ok := seen[manifestPath]; !ok {
		return fmt.Errorf("persisted validation evidence is missing its manifest")
	}
	return nil
}

func readEvidenceFile(root *os.Root, logicalPath string, expectedSize int64) ([]byte, error) {
	handle, err := root.Open(filepath.FromSlash(logicalPath))
	if err != nil {
		return nil, fmt.Errorf("open persisted evidence file %q: %w", logicalPath, err)
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat persisted evidence file %q: %w", logicalPath, err)
	}
	if !info.Mode().IsRegular() || info.Size() != expectedSize {
		return nil, fmt.Errorf("persisted evidence file %q has a different size or type", logicalPath)
	}
	content, err := io.ReadAll(io.LimitReader(handle, expectedSize+1))
	if err != nil {
		return nil, fmt.Errorf("read persisted evidence file %q: %w", logicalPath, err)
	}
	if int64(len(content)) != expectedSize {
		return nil, fmt.Errorf("persisted evidence file %q changed while it was read", logicalPath)
	}
	return content, nil
}
