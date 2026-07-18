package delivery

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

// ValidationBundle contains canonical, content-addressed evidence produced by
// an independent clean checkout.
type ValidationBundle struct {
	Report       contracts.ValidationReport
	ReportJSON   []byte
	Manifest     contracts.EvidenceManifest
	ManifestJSON []byte
	ReportRef    string
	ManifestRef  string
	PhysicalRoot string
}

// ValidationService owns independent checkouts and evidence production.
type ValidationService struct {
	manager        *WorktreeManager
	registry       *ValidatorRegistry
	evidenceRoot   string
	schemaRegistry *contracts.Registry
	now            func() time.Time
}

// NewValidationService creates a service over an existing operator-owned,
// non-symlink evidence root.
func NewValidationService(
	manager *WorktreeManager,
	registry *ValidatorRegistry,
	evidenceRoot string,
	now func() time.Time,
) (*ValidationService, error) {
	if manager == nil || registry == nil {
		return nil, fmt.Errorf("worktree manager and validator registry are required")
	}
	physical, err := canonicalDirectory(evidenceRoot, "evidence root")
	if err != nil {
		return nil, err
	}
	schemas, err := contracts.NewRegistry()
	if err != nil {
		return nil, fmt.Errorf("compile canonical schemas: %w", err)
	}
	if now == nil {
		now = time.Now
	}
	return &ValidationService{
		manager: manager, registry: registry, evidenceRoot: physical,
		schemaRegistry: schemas, now: now,
	}, nil
}

// Validate independently reconstructs the approved Git identity, runs every
// mandatory check, and atomically persists canonical evidence. Expected check
// failures are represented in the report; authority or persistence failures
// are returned as errors.
func (s *ValidationService) Validate(
	ctx context.Context,
	request contracts.DeliveryRequest,
	result CommitResult,
) (bundle ValidationBundle, resultErr error) {
	resolved, err := s.manager.resolveRequest(ctx, request)
	if err != nil {
		return ValidationBundle{}, err
	}
	evidenceContainsRepository, err := physicalPathContains(s.evidenceRoot, resolved.repositoryPhysical)
	if err != nil {
		return ValidationBundle{}, fmt.Errorf("compare evidence and repository roots: %w", err)
	}
	repositoryContainsEvidence, err := physicalPathContains(resolved.repositoryPhysical, s.evidenceRoot)
	if err != nil {
		return ValidationBundle{}, fmt.Errorf("compare repository and evidence roots: %w", err)
	}
	evidenceContainsWorktrees, err := physicalPathContains(s.evidenceRoot, resolved.rootPhysical)
	if err != nil {
		return ValidationBundle{}, fmt.Errorf("compare evidence and worktree roots: %w", err)
	}
	worktreesContainEvidence, err := physicalPathContains(resolved.rootPhysical, s.evidenceRoot)
	if err != nil {
		return ValidationBundle{}, fmt.Errorf("compare worktree and evidence roots: %w", err)
	}
	if evidenceContainsRepository || repositoryContainsEvidence ||
		evidenceContainsWorktrees || worktreesContainEvidence {
		return ValidationBundle{}, fmt.Errorf("evidence, repository, and worktree roots must be disjoint")
	}
	validators, err := s.registry.resolve(request.MechanicalValidatorIDs)
	if err != nil {
		return ValidationBundle{}, err
	}
	release, err := acquireAttemptLock(ctx, resolved)
	if err != nil {
		return ValidationBundle{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, release()) }()
	if err := ensureRequestAuthority(resolved.worktreeRoot, request, false); err != nil {
		return ValidationBundle{}, err
	}
	recomputed, err := s.manager.inspectCommitResult(ctx, resolved, result.ResultCommit)
	if err != nil {
		return ValidationBundle{}, err
	}
	if !sameCommitResult(result, recomputed) {
		return ValidationBundle{}, fmt.Errorf("supplied result identity disagrees with Git")
	}

	mechanicalExecutions, mechanicalPassed, err := s.runValidationLane(
		ctx, resolved, request, result, validators,
		"mechanical", ".forja-mechanical", ".forja-mechanical-quarantine",
	)
	if err != nil {
		return ValidationBundle{}, err
	}
	independentExecutions, _, err := s.runValidationLane(
		ctx, resolved, request, result, validators,
		"independent", ".forja-validation", ".forja-validation-quarantine",
	)
	if err != nil {
		return ValidationBundle{}, err
	}
	var mechanicalErr error
	if !mechanicalPassed {
		mechanicalErr = fmt.Errorf("mechanical preflight did not pass")
	}
	independentExecutions = append(
		independentExecutions,
		s.builtInExecution("mechanical-preflight", "built_in", mechanicalErr, "independent"),
	)
	slices.SortFunc(independentExecutions, func(left validationExecution, right validationExecution) int {
		return strings.Compare(left.check.CheckID, right.check.CheckID)
	})
	checks := make([]contracts.ValidationCheck, 0, len(independentExecutions))
	status := "passed"
	cleanCheckout := false
	latest := time.Time{}
	for _, execution := range independentExecutions {
		checks = append(checks, execution.check)
		if execution.check.CheckID == "clean-checkout" {
			cleanCheckout = execution.check.Status == "passed"
		}
		if execution.check.Status != "passed" {
			status = "failed"
		}
		if execution.check.FinishedAt.After(latest) {
			latest = execution.check.FinishedAt
		}
	}
	createdAt := s.now().UTC()
	if createdAt.Before(latest) {
		createdAt = latest
	}
	report := contracts.ValidationReport{
		ValidationID:  deterministicValidationID(request, result),
		DeliveryID:    request.DeliveryID,
		TenantID:      request.TenantID,
		RepositoryID:  request.RepositoryID,
		SchemaVersion: contracts.DeliverySchemaVersion,
		Status:        status,
		BaseCommit:    result.BaseCommit,
		ResultCommit:  result.ResultCommit,
		PatchSHA256:   result.PatchSHA256,
		AuthorID:      request.AuthorID,
		ValidatorID:   request.ValidatorID,
		CleanCheckout: cleanCheckout,
		Checks:        checks,
		CreatedAt:     createdAt,
	}
	if err := contracts.ValidateValidationReport(report); err != nil {
		return ValidationBundle{}, fmt.Errorf("validate generated report: %w", err)
	}
	bundle, err = s.persistEvidence(request, report, independentExecutions, mechanicalExecutions)
	if err != nil {
		return ValidationBundle{}, err
	}
	return bundle, nil
}

func (s *ValidationService) runValidationLane(
	ctx context.Context,
	resolved resolvedRequest,
	request contracts.DeliveryRequest,
	result CommitResult,
	validators []registeredValidator,
	lane string,
	checkoutNamespace string,
	quarantineNamespace string,
) ([]validationExecution, bool, error) {
	checkout, cleanup, err := s.prepareIndependentCheckout(
		ctx, resolved, result.ResultCommit, checkoutNamespace, quarantineNamespace,
	)
	if err != nil {
		return nil, false, err
	}
	home, err := os.MkdirTemp(s.evidenceRoot, ".validator-home-*")
	if err != nil {
		return nil, false, errors.Join(
			fmt.Errorf("create isolated validator home: %w", err), cleanup(),
		)
	}
	defer os.RemoveAll(home)

	executions := make([]validationExecution, 0, 6+len(validators))
	initialCleanErr := s.verifyIndependentCheckout(ctx, resolved, checkout, result.ResultCommit)
	executions = append(executions, s.builtInExecution("clean-checkout", "independent", initialCleanErr, lane))
	builtIns := []struct {
		id  string
		run func() error
	}{
		{"scope-boundary", func() error { return validateChangedPathScopes(result.ChangedPaths, request.WriteScopes) }},
		{"filesystem-safety", func() error { return s.validateFilesystemSafety(ctx, resolved, result) }},
		{"secret-scan", func() error { return s.validateSecrets(ctx, resolved, result) }},
		{"schema-validation", func() error { return s.validateJSONAndSchemas(ctx, resolved, result) }},
		{"generated-file-policy", func() error { return s.validateGeneratedFiles(ctx, resolved, result) }},
	}
	builtInsPassed := initialCleanErr == nil
	for _, builtIn := range builtIns {
		execution := s.builtInExecution(builtIn.id, "built_in", builtIn.run(), lane)
		builtInsPassed = builtInsPassed && execution.check.Status == "passed"
		executions = append(executions, execution)
	}
	if builtInsPassed {
		for _, validator := range validators {
			executions = append(executions, runRegisteredValidator(
				ctx, validator, checkout, home, s.registry.environ, s.now, lane,
			))
		}
	}
	finalCleanErr := s.verifyIndependentCheckout(ctx, resolved, checkout, result.ResultCommit)
	if initialCleanErr == nil && finalCleanErr != nil {
		for index := range executions {
			if executions[index].check.CheckID == "clean-checkout" {
				executions[index] = s.builtInExecution("clean-checkout", "independent", finalCleanErr, lane)
				break
			}
		}
	}
	slices.SortFunc(executions, func(left validationExecution, right validationExecution) int {
		return strings.Compare(left.check.CheckID, right.check.CheckID)
	})
	passed := true
	for _, execution := range executions {
		if execution.check.Status != "passed" {
			passed = false
		}
	}
	if cleanupErr := cleanup(); cleanupErr != nil {
		return nil, false, cleanupErr
	}
	return executions, passed, nil
}

func (s *ValidationService) builtInExecution(
	id string,
	kind string,
	runErr error,
	lane string,
) validationExecution {
	startedAt := s.now().UTC()
	finishedAt := s.now().UTC()
	if finishedAt.Before(startedAt) {
		finishedAt = startedAt
	}
	status := "passed"
	stderr := []byte{}
	var detail *string
	if runErr != nil {
		status = "failed"
		stderr = []byte(runErr.Error())
		if len(stderr) > 4000 {
			stderr = stderr[:4000]
		}
		message := "mandatory validation check failed"
		detail = &message
	}
	stdoutDigest := sha256.Sum256(nil)
	stderrDigest := sha256.Sum256(stderr)
	return validationExecution{
		check: contracts.ValidationCheck{
			CheckID: id, Kind: kind, Status: status,
			StartedAt: startedAt, FinishedAt: finishedAt,
			DurationMS:   finishedAt.Sub(startedAt).Milliseconds(),
			StdoutSHA256: fmt.Sprintf("%x", stdoutDigest),
			StderrSHA256: fmt.Sprintf("%x", stderrDigest), Detail: detail,
		},
		stdout: []byte{}, stderr: stderr, lane: lane,
	}
}

func (s *ValidationService) prepareIndependentCheckout(
	ctx context.Context,
	resolved resolvedRequest,
	resultCommit string,
	checkoutNamespace string,
	quarantineNamespace string,
) (string, func() error, error) {
	relative := filepath.Join(checkoutNamespace, resolved.request.DeliveryID, resolved.request.AttemptID)
	checkout := filepath.Join(resolved.worktreeRoot, relative)
	root, err := os.OpenRoot(resolved.worktreeRoot)
	if err != nil {
		return "", nil, fmt.Errorf("open worktree root for independent checkout: %w", err)
	}
	defer root.Close()
	parent := filepath.Dir(relative)
	if err := root.MkdirAll(parent, 0o700); err != nil {
		return "", nil, fmt.Errorf("create independent checkout namespace: %w", err)
	}
	if err := validateScopePosition(resolved.worktreeRoot, parent, parent); err != nil {
		return "", nil, err
	}
	if _, err := root.Lstat(relative); err == nil {
		return "", nil, fmt.Errorf("%w: independent checkout already exists", ErrWorktreeConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, fmt.Errorf("inspect independent checkout path: %w", err)
	}
	if _, err := s.manager.gitMutation(
		ctx, resolved.repositoryPath,
		"worktree", "add", "--quiet", "--detach", checkout, resultCommit,
	); err != nil {
		return "", nil, fmt.Errorf("create independent checkout: %w", err)
	}
	cleanup := func() error {
		return s.cleanupIndependentCheckout(
			resolved, checkout, resultCommit, quarantineNamespace,
		)
	}
	if err := s.verifyIndependentCheckout(ctx, resolved, checkout, resultCommit); err != nil {
		return "", nil, errors.Join(err, cleanup())
	}
	return checkout, cleanup, nil
}

func (s *ValidationService) verifyIndependentCheckout(
	ctx context.Context,
	resolved resolvedRequest,
	checkout string,
	resultCommit string,
) error {
	physical, err := canonicalDirectory(checkout, "independent checkout")
	if err != nil {
		return err
	}
	top, common, err := s.manager.worktreeIdentity(ctx, checkout)
	if err != nil || top != physical || common != resolved.repositoryCommon {
		return fmt.Errorf("independent checkout has an invalid Git identity")
	}
	head, err := s.manager.git(ctx, checkout, "rev-parse", "--verify", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) != resultCommit {
		return fmt.Errorf("independent checkout is not at the result commit")
	}
	branch, err := s.manager.git(ctx, checkout, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || strings.TrimSpace(string(branch)) != "HEAD" {
		return fmt.Errorf("independent checkout is not detached")
	}
	if err := s.manager.validateIndexFlags(ctx, checkout); err != nil {
		return err
	}
	if err := s.manager.validateClean(ctx, checkout); err != nil {
		return err
	}
	return s.manager.validateRepositoryExecutionConfig(ctx, checkout)
}

func (s *ValidationService) cleanupIndependentCheckout(
	resolved resolvedRequest,
	checkout string,
	resultCommit string,
	quarantineNamespace string,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	if err := s.verifyIndependentCheckout(ctx, resolved, checkout, resultCommit); err == nil {
		if _, err := s.manager.gitMutation(
			ctx, resolved.repositoryPath, "worktree", "remove", checkout,
		); err != nil {
			return fmt.Errorf("remove independent checkout: %w", err)
		}
		return nil
	}
	quarantineRelative := filepath.Join(
		quarantineNamespace, resolved.request.DeliveryID, resolved.request.AttemptID,
	)
	quarantine := filepath.Join(resolved.worktreeRoot, quarantineRelative)
	parent := filepath.Dir(quarantine)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create validation quarantine namespace: %w", err)
	}
	if physical, err := canonicalDirectory(parent, "validation quarantine namespace"); err != nil {
		return err
	} else if physical != filepath.Join(
		resolved.rootPhysical, quarantineNamespace,
		resolved.request.DeliveryID,
	) {
		return fmt.Errorf("validation quarantine namespace traverses a symlink")
	}
	if _, err := os.Lstat(quarantine); err == nil {
		return fmt.Errorf("%w: validation quarantine destination already exists", ErrWorktreeConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect validation quarantine destination: %w", err)
	}
	physical, physicalErr := canonicalDirectory(checkout, "independent checkout")
	top, common, identityErr := s.manager.worktreeIdentity(ctx, checkout)
	registered := physicalErr == nil && identityErr == nil &&
		top == physical && common == resolved.repositoryCommon
	if registered {
		if _, err := s.manager.gitMutation(
			ctx, resolved.repositoryPath, "worktree", "move", checkout, quarantine,
		); err != nil {
			return errors.Join(
				ErrGitReconciliationRequired,
				fmt.Errorf("preserve invalid independent checkout: %w", err),
			)
		}
		return nil
	}
	root, err := os.OpenRoot(resolved.worktreeRoot)
	if err != nil {
		return errors.Join(ErrGitReconciliationRequired, err)
	}
	defer root.Close()
	sourceRelative, err := filepath.Rel(resolved.worktreeRoot, checkout)
	if err != nil || sourceRelative == "." || strings.HasPrefix(sourceRelative, "..") {
		return errors.Join(
			ErrGitReconciliationRequired,
			fmt.Errorf("derive invalid checkout position"),
		)
	}
	if err := root.MkdirAll(quarantineRelative, 0o700); err != nil {
		return errors.Join(ErrGitReconciliationRequired, err)
	}
	preservedRelative := filepath.Join(quarantineRelative, "preserved-entry")
	if err := root.Rename(sourceRelative, preservedRelative); err != nil {
		return errors.Join(
			ErrGitReconciliationRequired,
			fmt.Errorf("preserve unverifiable independent checkout: %w", err),
		)
	}
	if err := writeReconciliationMarker(root, quarantineRelative); err != nil {
		return errors.Join(ErrGitReconciliationRequired, err)
	}
	return ErrGitReconciliationRequired
}

func sameCommitResult(left CommitResult, right CommitResult) bool {
	return left.BaseCommit == right.BaseCommit && left.ResultCommit == right.ResultCommit &&
		left.ResultTree == right.ResultTree && left.PatchSHA256 == right.PatchSHA256 &&
		slices.Equal(left.ChangedPaths, right.ChangedPaths)
}

func deterministicValidationID(request contracts.DeliveryRequest, result CommitResult) string {
	digest := sha256.Sum256([]byte(
		request.DeliveryID + "\x00" + request.AttemptID + "\x00" +
			request.ValidatorID + "\x00" + result.ResultCommit,
	))
	digest[6] = (digest[6] & 0x0f) | 0x40
	digest[8] = (digest[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"validation_%x-%x-%x-%x-%x",
		digest[0:4], digest[4:6], digest[6:8], digest[8:10], digest[10:16],
	)
}
