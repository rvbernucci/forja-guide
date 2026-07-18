package delivery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

// ErrValidationEvidenceNotFound means validation had not durably published an
// evidence bundle before interruption.
var ErrValidationEvidenceNotFound = errors.New("validation evidence not found")

// Load reconstructs and verifies an exact persisted validation bundle. It
// never reruns validators or substitutes new timestamps for prepared intent.
func (s *ValidationService) Load(
	_ context.Context,
	request contracts.DeliveryRequest,
) (ValidationBundle, error) {
	if err := contracts.ValidateDeliveryRequest(request); err != nil {
		return ValidationBundle{}, err
	}
	physicalRoot := filepath.Join(s.evidenceRoot, request.DeliveryID, request.AttemptID)
	info, err := os.Lstat(physicalRoot)
	if errors.Is(err, os.ErrNotExist) {
		return ValidationBundle{}, ErrValidationEvidenceNotFound
	}
	if err != nil {
		return ValidationBundle{}, fmt.Errorf("inspect persisted validation evidence: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ValidationBundle{}, fmt.Errorf("persisted validation evidence is not a real directory")
	}
	canonical, err := filepath.EvalSymlinks(physicalRoot)
	if err != nil || canonical != physicalRoot {
		return ValidationBundle{}, fmt.Errorf("persisted validation evidence traverses a symlink")
	}
	reportPath := path.Join(request.EvidenceScope, "validation.json")
	manifestPath := path.Join(request.EvidenceScope, "manifest.json")
	root, err := os.OpenRoot(physicalRoot)
	if err != nil {
		return ValidationBundle{}, fmt.Errorf("open persisted validation evidence: %w", err)
	}
	defer root.Close()
	reportJSON, err := readBoundedEvidenceFile(root, reportPath)
	if err != nil {
		return ValidationBundle{}, err
	}
	manifestJSON, err := readBoundedEvidenceFile(root, manifestPath)
	if err != nil {
		return ValidationBundle{}, err
	}
	if err := s.schemaRegistry.ValidateJSON("validation-report.schema.json", reportJSON); err != nil {
		return ValidationBundle{}, fmt.Errorf("persisted validation report violates schema: %w", err)
	}
	if err := s.schemaRegistry.ValidateJSON("evidence-manifest.schema.json", manifestJSON); err != nil {
		return ValidationBundle{}, fmt.Errorf("persisted evidence manifest violates schema: %w", err)
	}
	var report contracts.ValidationReport
	if err := decodeExactEvidence(reportJSON, &report); err != nil {
		return ValidationBundle{}, fmt.Errorf("decode persisted validation report: %w", err)
	}
	if err := contracts.ValidateValidationReport(report); err != nil {
		return ValidationBundle{}, fmt.Errorf("validate persisted validation report: %w", err)
	}
	var manifest contracts.EvidenceManifest
	if err := decodeExactEvidence(manifestJSON, &manifest); err != nil {
		return ValidationBundle{}, fmt.Errorf("decode persisted evidence manifest: %w", err)
	}
	reportDigest := sha256.Sum256(reportJSON)
	manifestDigest := sha256.Sum256(manifestJSON)
	bundle := ValidationBundle{
		Report: report, ReportJSON: reportJSON,
		Manifest: manifest, ManifestJSON: manifestJSON,
		ReportRef:    reportPath + "#sha256=" + fmt.Sprintf("%x", reportDigest),
		ManifestRef:  manifestPath + "#sha256=" + fmt.Sprintf("%x", manifestDigest),
		PhysicalRoot: physicalRoot,
	}
	if err := verifyPersistedValidationBundle(request, bundle); err != nil {
		return ValidationBundle{}, fmt.Errorf("verify persisted validation bundle: %w", err)
	}
	return bundle, nil
}

func readBoundedEvidenceFile(root *os.Root, logicalPath string) ([]byte, error) {
	info, err := root.Lstat(filepath.FromSlash(logicalPath))
	if err != nil {
		return nil, fmt.Errorf("inspect persisted evidence file %q: %w", logicalPath, err)
	}
	if !info.Mode().IsRegular() || info.Size() < 2 || info.Size() > maximumValidationEvidenceBytes {
		return nil, fmt.Errorf("persisted evidence file %q has an invalid type or size", logicalPath)
	}
	return readEvidenceFile(root, logicalPath, info.Size())
}

func decodeExactEvidence(encoded []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("persisted evidence contains trailing JSON")
		}
		return err
	}
	return nil
}
