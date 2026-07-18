package contracts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"time"
)

var mandatoryBuiltInCheckIDs = []string{
	"filesystem-safety",
	"generated-file-policy",
	"schema-validation",
	"scope-boundary",
	"secret-scan",
}

// ValidateDeliveryRequest enforces cross-field authority rules that JSON Schema
// cannot express.
func ValidateDeliveryRequest(request DeliveryRequest) error {
	if request.AuthorID == request.ValidatorID {
		return fmt.Errorf("delivery author and independent validator must differ")
	}
	if err := validateCanonicalAbsoluteRoot("repository path", request.RepositoryPath); err != nil {
		return err
	}
	if err := validateCanonicalAbsoluteRoot("worktree root", request.WorktreeRoot); err != nil {
		return err
	}
	if absolutePathContains(request.RepositoryPath, request.WorktreeRoot) ||
		absolutePathContains(request.WorktreeRoot, request.RepositoryPath) {
		return fmt.Errorf("repository path and worktree root must be disjoint")
	}
	if request.PublicationRef != "refs/forja/deliveries/"+request.DeliveryID {
		return fmt.Errorf("publication ref must be derived from the delivery ID")
	}
	if request.AttemptOrdinal > request.WorkerBudgets.MaxRetries+1 {
		return fmt.Errorf("attempt ordinal exceeds the approved retry budget")
	}
	minimumLease := request.WorkerBudgets.WallClockMS + request.WorkerBudgets.CancellationGraceMS
	if request.LeaseTTLMS <= minimumLease {
		return fmt.Errorf("lease TTL must outlive worker and cancellation budgets")
	}
	if err := validateCanonicalScopes("read scope", request.ReadScopes, true); err != nil {
		return err
	}
	if err := validateCanonicalScopes("write scope", request.WriteScopes, false); err != nil {
		return err
	}
	if err := validateCanonicalScopes("artifact scope", request.ArtifactScopes, false); err != nil {
		return err
	}
	writableScopes := append(append([]string(nil), request.WriteScopes...), request.ArtifactScopes...)
	slices.Sort(writableScopes)
	if err := validateCanonicalScopes("combined writable scope", writableScopes, false); err != nil {
		return err
	}
	if err := validateCanonicalScope("evidence scope", request.EvidenceScope, false); err != nil {
		return err
	}
	if !coveredByScope(request.EvidenceScope, request.ArtifactScopes) {
		return fmt.Errorf("evidence scope must be covered by an artifact scope")
	}
	if !sortedUnique(request.MechanicalValidatorIDs) {
		return fmt.Errorf("mechanical validator IDs must be unique and byte-sorted")
	}
	return nil
}

// ValidateValidationReport proves internal timing, identity, and result coupling.
func ValidateValidationReport(report ValidationReport) error {
	if report.AuthorID == report.ValidatorID {
		return fmt.Errorf("validation author and validator must differ")
	}
	if !report.CleanCheckout {
		return fmt.Errorf("independent validation requires a clean checkout")
	}
	if len(report.Checks) == 0 {
		return fmt.Errorf("independent validation requires at least one check")
	}
	checkIDs := make([]string, 0, len(report.Checks))
	allPassed := true
	hasIndependent := false
	latest := time.Time{}
	for _, check := range report.Checks {
		if check.FinishedAt.Before(check.StartedAt) {
			return fmt.Errorf("validation check %q finishes before it starts", check.CheckID)
		}
		if check.DurationMS != check.FinishedAt.Sub(check.StartedAt).Milliseconds() {
			return fmt.Errorf("validation check %q duration is inconsistent", check.CheckID)
		}
		if check.FinishedAt.After(latest) {
			latest = check.FinishedAt
		}
		allPassed = allPassed && check.Status == "passed"
		hasIndependent = hasIndependent || check.Kind == "independent"
		checkIDs = append(checkIDs, check.CheckID)
	}
	if !hasIndependent {
		return fmt.Errorf("validation report requires an independent check")
	}
	if !sortedUnique(checkIDs) {
		return fmt.Errorf("validation checks must be unique and byte-sorted")
	}
	if report.CreatedAt.Before(latest) {
		return fmt.Errorf("validation report predates a completed check")
	}
	if (report.Status == "passed") != allPassed {
		return fmt.Errorf("validation status disagrees with check results")
	}
	return nil
}

// ValidateDeliveryPublication proves that one approved request, passing
// independent report, evidence manifest, and publication receipt describe the
// same fenced Git result.
func ValidateDeliveryPublication(
	request DeliveryRequest,
	report ValidationReport,
	evidenceManifest []byte,
	receipt DeliveryReceipt,
) error {
	if err := ValidateDeliveryRequest(request); err != nil {
		return fmt.Errorf("delivery request: %w", err)
	}
	if err := ValidateValidationReport(report); err != nil {
		return fmt.Errorf("validation report: %w", err)
	}
	if err := validateDeliveryReceiptStructure(receipt); err != nil {
		return err
	}
	if report.Status != "passed" {
		return fmt.Errorf("published delivery requires a passing validation report")
	}
	completedChecks := make(map[string]bool, len(report.Checks))
	completedBuiltIns := make(map[string]bool, len(report.Checks))
	for _, check := range report.Checks {
		if check.Kind == "configured" {
			completedChecks[check.CheckID] = check.Status == "passed"
		}
		if check.Kind == "built_in" {
			completedBuiltIns[check.CheckID] = check.Status == "passed"
		}
	}
	for _, validatorID := range request.MechanicalValidatorIDs {
		if !completedChecks[validatorID] {
			return fmt.Errorf("approved mechanical validator %q did not pass", validatorID)
		}
	}
	for _, checkID := range mandatoryBuiltInCheckIDs {
		if !completedBuiltIns[checkID] {
			return fmt.Errorf("mandatory built-in check %q did not pass", checkID)
		}
	}
	if report.DeliveryID != request.DeliveryID || receipt.DeliveryID != request.DeliveryID ||
		report.BaseCommit != request.BaseCommit || receipt.BaseCommit != request.BaseCommit ||
		receipt.ResultCommit != report.ResultCommit || receipt.PatchSHA256 != report.PatchSHA256 ||
		receipt.AuthorID != request.AuthorID || report.AuthorID != request.AuthorID ||
		receipt.ValidatorID != request.ValidatorID || report.ValidatorID != request.ValidatorID ||
		receipt.PublicationRef != request.PublicationRef {
		return fmt.Errorf("delivery request, validation report, and receipt identities disagree")
	}
	if !equalOptionalString(receipt.PublicationPreviousCommit, request.PublicationPreviousCommit) {
		return fmt.Errorf("publication previous commit disagrees with approved compare-and-swap state")
	}
	if receipt.CreatedAt.Before(report.CreatedAt) {
		return fmt.Errorf("delivery receipt predates independent validation")
	}
	for _, changedPath := range receipt.ChangedPaths {
		if !coveredByScope(changedPath, request.WriteScopes) {
			return fmt.Errorf("changed path %q is outside approved write scopes", changedPath)
		}
	}
	reportPath, reportDigest, _ := strings.Cut(receipt.ValidationReportRef, "#sha256=")
	manifestPath, manifestDigest, _ := strings.Cut(receipt.EvidenceManifestRef, "#sha256=")
	if !coveredByScope(reportPath, []string{request.EvidenceScope}) ||
		!coveredByScope(manifestPath, []string{request.EvidenceScope}) {
		return fmt.Errorf("publication evidence is outside the approved evidence scope")
	}
	canonicalReport, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshal canonical validation report: %w", err)
	}
	if reportDigest != sha256Hex(canonicalReport) {
		return fmt.Errorf("validation report reference digest disagrees with canonical report")
	}
	if manifestDigest != sha256Hex(evidenceManifest) {
		return fmt.Errorf("evidence manifest reference digest disagrees with manifest content")
	}
	manifest, err := decodeCanonicalEvidenceManifest(evidenceManifest)
	if err != nil {
		return err
	}
	if manifest.DeliveryID != request.DeliveryID {
		return fmt.Errorf("evidence manifest identifies another delivery")
	}
	entryPaths := make([]string, 0, len(manifest.Entries))
	reportEntryFound := false
	for _, entry := range manifest.Entries {
		if err := validateCanonicalScope("evidence entry path", entry.Path, false); err != nil {
			return err
		}
		if !coveredByScope(entry.Path, []string{request.EvidenceScope}) {
			return fmt.Errorf("evidence entry %q is outside the approved evidence scope", entry.Path)
		}
		if entry.Path == manifestPath {
			return fmt.Errorf("evidence manifest cannot contain itself")
		}
		if err := validateSHA256("evidence entry", entry.SHA256); err != nil {
			return err
		}
		if entry.SizeBytes < 0 || entry.MediaType == "" {
			return fmt.Errorf("evidence entry %q has invalid metadata", entry.Path)
		}
		if entry.Path == reportPath && entry.SHA256 == reportDigest && entry.SizeBytes == int64(len(canonicalReport)) {
			reportEntryFound = true
		}
		entryPaths = append(entryPaths, entry.Path)
	}
	if !sortedUnique(entryPaths) {
		return fmt.Errorf("evidence entries must be unique and byte-sorted by path")
	}
	if !reportEntryFound {
		return fmt.Errorf("evidence manifest does not contain the canonical validation report")
	}
	expectedFenceKeys := expectedDeliveryFenceKeys(request)
	actualFenceKeys := make([]string, 0, len(receipt.LeaseFences))
	for _, fence := range receipt.LeaseFences {
		actualFenceKeys = append(actualFenceKeys, fence.ResourceType+"\x00"+fence.ResourceID)
	}
	if !slices.Equal(actualFenceKeys, expectedFenceKeys) {
		return fmt.Errorf("delivery lease fences disagree with the approved scope set")
	}
	return nil
}

func validateDeliveryReceiptStructure(receipt DeliveryReceipt) error {
	if receipt.Status != "published" {
		return fmt.Errorf("delivery receipt must represent a published result")
	}
	if receipt.AuthorID == receipt.ValidatorID {
		return fmt.Errorf("delivery author and validator must differ")
	}
	if receipt.PublicationRef != "refs/forja/deliveries/"+receipt.DeliveryID {
		return fmt.Errorf("publication ref must be derived from the delivery ID")
	}
	if receipt.PublishedAt.Before(receipt.CreatedAt) {
		return fmt.Errorf("delivery publication predates receipt creation")
	}
	if err := validateCanonicalPaths("changed path", receipt.ChangedPaths, false); err != nil {
		return err
	}
	if err := validateContentReference("validation report", receipt.ValidationReportRef); err != nil {
		return err
	}
	if err := validateContentReference("evidence manifest", receipt.EvidenceManifestRef); err != nil {
		return err
	}
	if len(receipt.LeaseFences) < 3 {
		return fmt.Errorf("delivery receipt requires worktree, file, and artifact fences")
	}
	fenceKeys := make([]string, 0, len(receipt.LeaseFences))
	types := map[string]bool{}
	owner := receipt.LeaseFences[0].OwnerID
	for _, fence := range receipt.LeaseFences {
		if fence.OwnerID == "" || fence.FencingToken < 1 {
			return fmt.Errorf("delivery lease fence has invalid ownership proof")
		}
		if fence.OwnerID != owner {
			return fmt.Errorf("delivery lease fences must have one owner")
		}
		switch fence.ResourceType {
		case "worktree":
			if fence.ResourceID != receipt.DeliveryID {
				return fmt.Errorf("worktree lease fence must identify the delivery")
			}
		case "file", "artifact":
			if err := validateCanonicalScope(fence.ResourceType+" lease resource", fence.ResourceID, false); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported delivery lease resource type %q", fence.ResourceType)
		}
		types[fence.ResourceType] = true
		fenceKeys = append(fenceKeys, fence.ResourceType+"\x00"+fence.ResourceID)
	}
	if !types["worktree"] || !types["file"] || !types["artifact"] {
		return fmt.Errorf("delivery receipt lacks a required lease resource type")
	}
	if !sortedUnique(fenceKeys) {
		return fmt.Errorf("delivery lease fences must be unique and byte-sorted")
	}
	return nil
}

func expectedDeliveryFenceKeys(request DeliveryRequest) []string {
	keys := []string{"worktree\x00" + request.DeliveryID}
	for _, scope := range request.WriteScopes {
		for _, resourceID := range scopeAndAncestors(scope) {
			keys = append(keys, "file\x00"+resourceID)
		}
	}
	for _, scope := range request.ArtifactScopes {
		for _, resourceID := range scopeAndAncestors(scope) {
			keys = append(keys, "artifact\x00"+resourceID)
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

func scopeAndAncestors(scope string) []string {
	parts := strings.Split(scope, "/")
	values := make([]string, 0, len(parts))
	for index := range parts {
		values = append(values, strings.Join(parts[:index+1], "/"))
	}
	return values
}

func validateCanonicalScopes(label string, values []string, allowRoot bool) error {
	if err := validateCanonicalPaths(label, values, allowRoot); err != nil {
		return err
	}
	for index := 1; index < len(values); index++ {
		if coveredByScope(values[index], values[:index]) {
			return fmt.Errorf("%s values must not overlap", label)
		}
	}
	return nil
}

func validateCanonicalPaths(label string, values []string, allowRoot bool) error {
	if len(values) == 0 || !sortedUnique(values) {
		return fmt.Errorf("%s values must be non-empty, unique, and byte-sorted", label)
	}
	for _, value := range values {
		if err := validateCanonicalScope(label, value, allowRoot); err != nil {
			return err
		}
	}
	if allowRoot && slices.Contains(values, ".") && len(values) != 1 {
		return fmt.Errorf("root read scope must be the only read scope")
	}
	return nil
}

func validateCanonicalScope(label string, value string, allowRoot bool) error {
	if value == "" || strings.Contains(value, "\\") || strings.ContainsRune(value, '\x00') ||
		path.IsAbs(value) || path.Clean(value) != value || strings.HasPrefix(value, "../") ||
		value == ".." || (!allowRoot && value == ".") {
		return fmt.Errorf("%s %q is not a canonical repository-relative path", label, value)
	}
	return nil
}

func validateCanonicalAbsoluteRoot(label string, value string) error {
	if value == "" || value == "/" || strings.Contains(value, "\\") ||
		strings.ContainsRune(value, '\x00') || !path.IsAbs(value) || path.Clean(value) != value {
		return fmt.Errorf("%s %q is not a canonical non-root absolute path", label, value)
	}
	return nil
}

func absolutePathContains(parent string, child string) bool {
	return child == parent || strings.HasPrefix(child, parent+"/")
}

func coveredByScope(value string, scopes []string) bool {
	for _, scope := range scopes {
		if value == scope || strings.HasPrefix(value, scope+"/") {
			return true
		}
	}
	return false
}

func sortedUnique(values []string) bool {
	return slices.IsSorted(values) && len(slices.Compact(append([]string(nil), values...))) == len(values)
}

func validateContentReference(label string, reference string) error {
	relative, digest, ok := strings.Cut(reference, "#sha256=")
	if !ok || strings.Contains(digest, "#") || len(digest) != 64 {
		return fmt.Errorf("%s reference is not content-addressed", label)
	}
	if err := validateCanonicalScope(label+" path", relative, false); err != nil {
		return err
	}
	if err := validateSHA256(label+" reference", digest); err != nil {
		return err
	}
	return nil
}

func validateSHA256(label string, digest string) error {
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != 32 || digest != strings.ToLower(digest) {
		return fmt.Errorf("%s has an invalid SHA-256 digest", label)
	}
	return nil
}

func decodeCanonicalEvidenceManifest(content []byte) (EvidenceManifest, error) {
	var manifest EvidenceManifest
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return EvidenceManifest{}, fmt.Errorf("decode evidence manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return EvidenceManifest{}, fmt.Errorf("evidence manifest contains trailing JSON")
	}
	if manifest.SchemaVersion != "1.0" || len(manifest.Entries) == 0 {
		return EvidenceManifest{}, fmt.Errorf("evidence manifest has an unsupported version or no entries")
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return EvidenceManifest{}, fmt.Errorf("marshal canonical evidence manifest: %w", err)
	}
	if !bytes.Equal(content, canonical) {
		return EvidenceManifest{}, fmt.Errorf("evidence manifest is not canonical JSON")
	}
	return manifest, nil
}

func sha256Hex(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func equalOptionalString(left *string, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
