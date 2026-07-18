// Package delivery owns isolated Git worktrees and publication-side authority.
package delivery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

const (
	maximumGitOutputBytes      = 16 << 20
	deliveryGitReadTimeout     = 2 * time.Second
	deliveryGitMutationTimeout = 30 * time.Second
	attemptLockTimeout         = 5 * time.Second
)

var (
	deliveryIDPattern = regexp.MustCompile(`^delivery_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	attemptIDPattern  = regexp.MustCompile(`^attempt_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

	// ErrWorktreeConflict identifies a path or Git identity already bound to
	// different authority.
	ErrWorktreeConflict = errors.New("delivery worktree conflict")
	// ErrWorktreeDirty identifies bytes that must be preserved rather than
	// removed or reused.
	ErrWorktreeDirty = errors.New("delivery worktree is dirty")
	// ErrGitReconciliationRequired identifies preserved bytes whose Git
	// administrative registration requires explicit operator reconciliation.
	ErrGitReconciliationRequired = errors.New("delivery Git reconciliation required")
)

// Worktree is the verified identity of one detached delivery checkout.
type Worktree struct {
	DeliveryID     string
	AttemptID      string
	RepositoryPath string
	Path           string
	CommonDir      string
	BaseCommit     string
}

// QuarantineResult records where contaminated or unverifiable bytes were
// preserved.
type QuarantineResult struct {
	SourcePath     string
	QuarantinePath string
	GitRegistered  bool
}

// WorktreeManager creates and quarantines only derived, detached worktrees.
type WorktreeManager struct {
	gitPath              string
	environ              []string
	readTimeout          time.Duration
	mutationTimeout      time.Duration
	afterAttemptLockTest func(string)
}

// NewWorktreeManager constructs a manager with a fixed Git executable and
// sanitized command environment.
func NewWorktreeManager(gitPath string, environ []string) (*WorktreeManager, error) {
	gitPath = strings.TrimSpace(gitPath)
	if gitPath == "" {
		gitPath = "git"
	}
	if strings.ContainsRune(gitPath, '\x00') {
		return nil, fmt.Errorf("git executable contains a NUL byte")
	}
	if environ == nil {
		environ = os.Environ()
	}
	return &WorktreeManager{
		gitPath:         gitPath,
		environ:         gitEnvironment(environ),
		readTimeout:     deliveryGitReadTimeout,
		mutationTimeout: deliveryGitMutationTimeout,
	}, nil
}

// Prepare creates or idempotently verifies one checkout at the approved base
// commit. Existing dirty or unrelated paths are never reused.
func (m *WorktreeManager) Prepare(
	ctx context.Context,
	request contracts.DeliveryRequest,
) (worktree Worktree, resultErr error) {
	resolved, err := m.resolveRequest(ctx, request)
	if err != nil {
		return Worktree{}, err
	}
	release, err := acquireAttemptLock(ctx, resolved)
	if err != nil {
		return Worktree{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, release()) }()
	if m.afterAttemptLockTest != nil {
		m.afterAttemptLockTest("prepare")
	}
	if err := ensureRequestAuthority(resolved.worktreeRoot, request, true); err != nil {
		return Worktree{}, err
	}
	if retired, err := quarantineDestinationExists(resolved); err != nil {
		return Worktree{}, err
	} else if retired {
		return Worktree{}, fmt.Errorf("%w: attempt identity is already quarantined", ErrWorktreeConflict)
	}
	if _, err := os.Lstat(resolved.worktreePath); err == nil {
		worktree, err := m.inspectResolved(ctx, resolved, true)
		if err != nil {
			return Worktree{}, fmt.Errorf("%w: existing attempt path: %v", ErrWorktreeConflict, err)
		}
		if err := prepareWritableRoots(worktree.Path, request); err != nil {
			return Worktree{}, err
		}
		return worktree, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Worktree{}, fmt.Errorf("inspect derived worktree path: %w", err)
	}

	root, err := os.OpenRoot(resolved.worktreeRoot)
	if err != nil {
		return Worktree{}, fmt.Errorf("open worktree root: %w", err)
	}
	defer root.Close()
	if err := root.MkdirAll(resolved.deliveryRelative, 0o700); err != nil {
		return Worktree{}, fmt.Errorf("create delivery worktree namespace: %w", err)
	}
	if err := validateScopePosition(
		resolved.worktreeRoot,
		resolved.deliveryRelative,
		resolved.deliveryRelative,
	); err != nil {
		return Worktree{}, fmt.Errorf("validate delivery worktree namespace: %w", err)
	}
	created := false
	defer func() {
		if resultErr == nil || !created {
			return
		}
		cleanupContext, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		if cleanupErr := m.removeFreshCleanResolved(cleanupContext, resolved); cleanupErr != nil {
			resultErr = errors.Join(resultErr, cleanupErr, retireFailedPrepare(resolved))
		}
	}()
	if _, err := m.gitMutation(
		ctx,
		resolved.repositoryPath,
		"worktree", "add", "--quiet", "--detach",
		resolved.worktreePath,
		resolved.baseCommit,
	); err != nil {
		return Worktree{}, errors.Join(
			fmt.Errorf("create detached worktree: %w", err),
			retireFailedPrepare(resolved),
		)
	}
	created = true

	worktree, err = m.inspectResolved(ctx, resolved, true)
	if err != nil {
		return Worktree{}, err
	}
	if err := prepareWritableRoots(worktree.Path, request); err != nil {
		return Worktree{}, err
	}
	worktree, err = m.inspectResolved(ctx, resolved, true)
	if err != nil {
		return Worktree{}, err
	}
	created = false
	return worktree, nil
}

// Inspect verifies repository binding, detached HEAD, exact base commit, clean
// status, ignored-file absence, and hidden-index flags.
func (m *WorktreeManager) Inspect(
	ctx context.Context,
	request contracts.DeliveryRequest,
) (worktree Worktree, resultErr error) {
	resolved, err := m.resolveRequest(ctx, request)
	if err != nil {
		return Worktree{}, err
	}
	release, err := acquireAttemptLock(ctx, resolved)
	if err != nil {
		return Worktree{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, release()) }()
	if m.afterAttemptLockTest != nil {
		m.afterAttemptLockTest("inspect")
	}
	if err := ensureRequestAuthority(resolved.worktreeRoot, request, false); err != nil {
		return Worktree{}, err
	}
	return m.inspectResolved(ctx, resolved, true)
}

// Quarantine moves a derived attempt path to a non-reusable namespace without
// deleting its bytes. Registered worktrees move through Git so metadata remains
// inspectable; an unregistered directory moves through the rooted filesystem.
func (m *WorktreeManager) Quarantine(
	ctx context.Context,
	request contracts.DeliveryRequest,
) (result QuarantineResult, resultErr error) {
	resolved, err := resolveQuarantineRequest(request)
	if err != nil {
		return QuarantineResult{}, err
	}
	release, err := acquireAttemptLock(ctx, resolved)
	if err != nil {
		return QuarantineResult{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, release()) }()
	if m.afterAttemptLockTest != nil {
		m.afterAttemptLockTest("quarantine")
	}
	if err := ensureRequestAuthority(resolved.worktreeRoot, request, false); err != nil {
		return QuarantineResult{}, err
	}
	authorizedCommon := ""
	if repositoryPhysical, repositoryErr := canonicalDirectory(request.RepositoryPath, "repository"); repositoryErr == nil {
		if top, common, identityErr := m.worktreeIdentity(ctx, request.RepositoryPath); identityErr == nil && top == repositoryPhysical {
			authorizedCommon = common
		}
	}
	info, err := os.Lstat(resolved.worktreePath)
	if err != nil {
		return QuarantineResult{}, fmt.Errorf("stat worktree for quarantine: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return QuarantineResult{}, fmt.Errorf("%w: attempt path is a symlink", ErrWorktreeConflict)
	}
	physicalSource, err := filepath.EvalSymlinks(resolved.worktreePath)
	if err != nil {
		return QuarantineResult{}, fmt.Errorf("resolve worktree for quarantine: %w", err)
	}
	if physicalSource != filepath.Join(resolved.rootPhysical, resolved.attemptRelative) {
		return QuarantineResult{}, fmt.Errorf("%w: attempt path traverses a symlink", ErrWorktreeConflict)
	}

	root, err := os.OpenRoot(resolved.worktreeRoot)
	if err != nil {
		return QuarantineResult{}, fmt.Errorf("open worktree root: %w", err)
	}
	defer root.Close()
	quarantineRelative := filepath.Join(
		"quarantine",
		request.DeliveryID,
		request.AttemptID,
	)
	quarantineParent := filepath.Dir(quarantineRelative)
	quarantinePath := filepath.Join(resolved.worktreeRoot, quarantineRelative)
	if _, err := root.Lstat(quarantineRelative); err == nil {
		return QuarantineResult{}, fmt.Errorf("%w: quarantine destination already exists", ErrWorktreeConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return QuarantineResult{}, fmt.Errorf("inspect quarantine destination: %w", err)
	}
	if err := root.MkdirAll(quarantineParent, 0o700); err != nil {
		return QuarantineResult{}, fmt.Errorf("create quarantine namespace: %w", err)
	}
	if err := validateScopePosition(
		resolved.worktreeRoot,
		quarantineParent,
		quarantineParent,
	); err != nil {
		return QuarantineResult{}, fmt.Errorf("validate quarantine namespace: %w", err)
	}

	registered := false
	if top, common, identityErr := m.worktreeIdentity(ctx, resolved.worktreePath); identityErr == nil {
		registered = true
		if top != physicalSource {
			return QuarantineResult{}, fmt.Errorf("%w: Git root disagrees with attempt path", ErrWorktreeConflict)
		}
		selfContained, err := physicalPathContains(physicalSource, common)
		if err != nil {
			return QuarantineResult{}, fmt.Errorf("inspect quarantined Git identity: %w", err)
		}
		if selfContained || authorizedCommon == "" || common != authorizedCommon {
			if err := root.Rename(resolved.attemptRelative, quarantineRelative); err != nil {
				return QuarantineResult{}, fmt.Errorf("move Git path through rooted quarantine: %w", err)
			}
		} else if _, err := m.gitMutation(
			ctx,
			resolved.worktreePath,
			"worktree", "move", resolved.worktreePath, quarantinePath,
		); err != nil {
			if reconciliationErr := reconcileQuarantineMutation(
				root,
				resolved,
				quarantineRelative,
			); reconciliationErr != nil {
				return QuarantineResult{}, errors.Join(
					fmt.Errorf("move registered worktree to quarantine: %w", err),
					reconciliationErr,
				)
			}
		}
	} else if err := root.Rename(resolved.attemptRelative, quarantineRelative); err != nil {
		return QuarantineResult{}, fmt.Errorf("move unverifiable path to quarantine: %w", err)
	}
	if _, err := os.Lstat(resolved.worktreePath); !errors.Is(err, os.ErrNotExist) {
		return QuarantineResult{}, fmt.Errorf("quarantine source still exists")
	}
	if _, err := os.Lstat(quarantinePath); err != nil {
		return QuarantineResult{}, fmt.Errorf("verify quarantined path: %w", err)
	}
	physicalQuarantine, err := filepath.EvalSymlinks(quarantinePath)
	if err != nil {
		return QuarantineResult{}, fmt.Errorf("resolve quarantined path: %w", err)
	}
	if physicalQuarantine != filepath.Join(resolved.rootPhysical, quarantineRelative) {
		return QuarantineResult{}, fmt.Errorf("quarantine path traverses a symlink")
	}
	return QuarantineResult{
		SourcePath:     resolved.worktreePath,
		QuarantinePath: quarantinePath,
		GitRegistered:  registered,
	}, nil
}

func resolveQuarantineRequest(request contracts.DeliveryRequest) (resolvedRequest, error) {
	if err := contracts.ValidateDeliveryRequest(request); err != nil {
		return resolvedRequest{}, fmt.Errorf("delivery request: %w", err)
	}
	if !deliveryIDPattern.MatchString(request.DeliveryID) ||
		!attemptIDPattern.MatchString(request.AttemptID) {
		return resolvedRequest{}, fmt.Errorf("delivery and attempt IDs must be canonical")
	}
	rootPhysical, err := canonicalDirectory(request.WorktreeRoot, "worktree root")
	if err != nil {
		return resolvedRequest{}, err
	}
	deliveryRelative := request.DeliveryID
	attemptRelative := filepath.Join(deliveryRelative, request.AttemptID)
	return resolvedRequest{
		request:          request,
		repositoryPath:   request.RepositoryPath,
		worktreeRoot:     request.WorktreeRoot,
		worktreePath:     filepath.Join(request.WorktreeRoot, attemptRelative),
		deliveryRelative: deliveryRelative,
		attemptRelative:  attemptRelative,
		baseCommit:       request.BaseCommit,
		rootPhysical:     rootPhysical,
	}, nil
}

type resolvedRequest struct {
	request            contracts.DeliveryRequest
	repositoryPath     string
	worktreeRoot       string
	worktreePath       string
	deliveryRelative   string
	attemptRelative    string
	baseCommit         string
	repositoryCommon   string
	repositoryPhysical string
	rootPhysical       string
}

func quarantineDestinationExists(resolved resolvedRequest) (bool, error) {
	root, err := os.OpenRoot(resolved.worktreeRoot)
	if err != nil {
		return false, fmt.Errorf("open worktree root for quarantine check: %w", err)
	}
	defer root.Close()
	name := filepath.Join("quarantine", resolved.request.DeliveryID, resolved.request.AttemptID)
	if _, err := root.Lstat(name); err == nil {
		return true, nil
	} else if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else {
		return false, fmt.Errorf("inspect quarantine destination: %w", err)
	}
}

func retireFailedPrepare(resolved resolvedRequest) error {
	root, err := os.OpenRoot(resolved.worktreeRoot)
	if err != nil {
		return errors.Join(
			ErrGitReconciliationRequired,
			fmt.Errorf("open worktree root for failed-prepare retirement: %w", err),
		)
	}
	defer root.Close()
	quarantineRelative := filepath.Join(
		"quarantine", resolved.request.DeliveryID, resolved.request.AttemptID,
	)
	if _, err := root.Lstat(quarantineRelative); err == nil {
		return ErrGitReconciliationRequired
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.Join(
			ErrGitReconciliationRequired,
			fmt.Errorf("inspect failed-prepare retirement: %w", err),
		)
	}
	parent := filepath.Dir(quarantineRelative)
	if err := root.MkdirAll(parent, 0o700); err != nil {
		return errors.Join(
			ErrGitReconciliationRequired,
			fmt.Errorf("create failed-prepare retirement namespace: %w", err),
		)
	}
	if err := validateScopePosition(resolved.worktreeRoot, parent, parent); err != nil {
		return errors.Join(ErrGitReconciliationRequired, err)
	}
	if _, err := root.Lstat(resolved.attemptRelative); err == nil {
		if err := root.Rename(resolved.attemptRelative, quarantineRelative); err != nil {
			return errors.Join(
				ErrGitReconciliationRequired,
				fmt.Errorf("preserve failed-prepare bytes: %w", err),
			)
		}
		return errors.Join(
			ErrGitReconciliationRequired,
			writeReconciliationMarker(root, quarantineRelative),
		)
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.Join(
			ErrGitReconciliationRequired,
			fmt.Errorf("inspect failed-prepare source: %w", err),
		)
	}
	if err := root.Mkdir(quarantineRelative, 0o700); err != nil {
		return errors.Join(
			ErrGitReconciliationRequired,
			fmt.Errorf("create failed-prepare tombstone: %w", err),
		)
	}
	return errors.Join(
		ErrGitReconciliationRequired,
		writeReconciliationMarker(root, quarantineRelative),
	)
}

func reconcileQuarantineMutation(
	root *os.Root,
	resolved resolvedRequest,
	quarantineRelative string,
) error {
	_, sourceErr := root.Lstat(resolved.attemptRelative)
	_, destinationErr := root.Lstat(quarantineRelative)
	sourceExists := sourceErr == nil
	destinationExists := destinationErr == nil
	if sourceErr != nil && !errors.Is(sourceErr, os.ErrNotExist) {
		return errors.Join(
			ErrGitReconciliationRequired,
			fmt.Errorf("inspect quarantine source after interrupted move: %w", sourceErr),
		)
	}
	if destinationErr != nil && !errors.Is(destinationErr, os.ErrNotExist) {
		return errors.Join(
			ErrGitReconciliationRequired,
			fmt.Errorf("inspect quarantine destination after interrupted move: %w", destinationErr),
		)
	}
	switch {
	case destinationExists && !sourceExists:
		if err := writeReconciliationMarker(root, quarantineRelative); err != nil {
			return errors.Join(ErrGitReconciliationRequired, err)
		}
	case sourceExists && !destinationExists:
		if err := root.Rename(resolved.attemptRelative, quarantineRelative); err != nil {
			return errors.Join(
				ErrGitReconciliationRequired,
				fmt.Errorf("preserve source after interrupted Git move: %w", err),
			)
		}
		if err := writeReconciliationMarker(root, quarantineRelative); err != nil {
			return errors.Join(ErrGitReconciliationRequired, err)
		}
	case sourceExists && destinationExists:
		if err := writeReconciliationMarker(root, quarantineRelative); err != nil {
			return errors.Join(ErrGitReconciliationRequired, err)
		}
		return errors.Join(
			ErrGitReconciliationRequired,
			fmt.Errorf("interrupted Git move left both source and quarantine destination"),
		)
	default:
		if err := root.Mkdir(quarantineRelative, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return errors.Join(
				ErrGitReconciliationRequired,
				fmt.Errorf("retire missing interrupted Git move: %w", err),
			)
		}
		if err := writeReconciliationMarker(root, quarantineRelative); err != nil {
			return errors.Join(ErrGitReconciliationRequired, err)
		}
		return errors.Join(
			ErrGitReconciliationRequired,
			fmt.Errorf("interrupted Git move left neither source nor preserved bytes"),
		)
	}
	return ErrGitReconciliationRequired
}

func writeReconciliationMarker(root *os.Root, quarantineRelative string) error {
	marker := filepath.Join(quarantineRelative, ".forja-reconciliation-required")
	file, err := root.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create Git reconciliation marker: %w", err)
	}
	if _, err := file.Write([]byte("manual Git administrative reconciliation required\n")); err != nil {
		_ = file.Close()
		return fmt.Errorf("write Git reconciliation marker: %w", err)
	}
	return file.Close()
}

func acquireAttemptLock(
	ctx context.Context,
	resolved resolvedRequest,
) (func() error, error) {
	root, err := os.OpenRoot(resolved.worktreeRoot)
	if err != nil {
		return nil, fmt.Errorf("open worktree root for attempt lock: %w", err)
	}
	parent := filepath.Join(".forja-locks", resolved.request.DeliveryID)
	name := filepath.Join(parent, resolved.request.AttemptID+".lock")
	if err := root.MkdirAll(parent, 0o700); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("create attempt lock namespace: %w", err)
	}
	if err := validateScopePosition(resolved.worktreeRoot, parent, parent); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("validate attempt lock namespace: %w", err)
	}
	lockContext, cancel := context.WithTimeout(ctx, attemptLockTimeout)
	defer cancel()
	for {
		if err := root.Mkdir(name, 0o700); err == nil {
			return func() error {
				removeErr := root.Remove(name)
				closeErr := root.Close()
				if removeErr != nil {
					removeErr = fmt.Errorf("release attempt lock: %w", removeErr)
				}
				return errors.Join(removeErr, closeErr)
			}, nil
		} else if !errors.Is(err, os.ErrExist) {
			_ = root.Close()
			return nil, fmt.Errorf("acquire attempt lock: %w", err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-lockContext.Done():
			timer.Stop()
			_ = root.Close()
			return nil, fmt.Errorf("acquire attempt lock: %w", lockContext.Err())
		case <-timer.C:
		}
	}
}

func (m *WorktreeManager) resolveRequest(
	ctx context.Context,
	request contracts.DeliveryRequest,
) (resolvedRequest, error) {
	if err := contracts.ValidateDeliveryRequest(request); err != nil {
		return resolvedRequest{}, fmt.Errorf("delivery request: %w", err)
	}
	if !deliveryIDPattern.MatchString(request.DeliveryID) ||
		!attemptIDPattern.MatchString(request.AttemptID) {
		return resolvedRequest{}, fmt.Errorf("delivery and attempt IDs must be canonical")
	}
	repositoryPhysical, err := canonicalDirectory(request.RepositoryPath, "repository")
	if err != nil {
		return resolvedRequest{}, err
	}
	rootPhysical, err := canonicalDirectory(request.WorktreeRoot, "worktree root")
	if err != nil {
		return resolvedRequest{}, err
	}
	repositoryContainsRoot, err := physicalPathContains(repositoryPhysical, rootPhysical)
	if err != nil {
		return resolvedRequest{}, fmt.Errorf("compare repository and worktree roots: %w", err)
	}
	rootContainsRepository, err := physicalPathContains(rootPhysical, repositoryPhysical)
	if err != nil {
		return resolvedRequest{}, fmt.Errorf("compare worktree and repository roots: %w", err)
	}
	if repositoryContainsRoot || rootContainsRepository {
		return resolvedRequest{}, fmt.Errorf("resolved repository and worktree roots must be disjoint")
	}
	repositoryTop, common, err := m.worktreeIdentity(ctx, request.RepositoryPath)
	if err != nil {
		return resolvedRequest{}, fmt.Errorf("inspect repository identity: %w", err)
	}
	if repositoryTop != repositoryPhysical {
		return resolvedRequest{}, fmt.Errorf("repository path must be its Git worktree root")
	}
	if err := m.validateRepositoryExecutionConfig(ctx, request.RepositoryPath); err != nil {
		return resolvedRequest{}, err
	}
	commit, err := m.git(
		ctx,
		request.RepositoryPath,
		"rev-parse", "--verify", "--end-of-options", request.BaseCommit+"^{commit}",
	)
	if err != nil {
		return resolvedRequest{}, fmt.Errorf("resolve approved base commit: %w", err)
	}
	commit = bytes.TrimSpace(commit)
	if string(commit) != request.BaseCommit {
		return resolvedRequest{}, fmt.Errorf("approved base does not identify an exact commit object")
	}
	deliveryRelative := request.DeliveryID
	attemptRelative := filepath.Join(deliveryRelative, request.AttemptID)
	return resolvedRequest{
		request:            request,
		repositoryPath:     request.RepositoryPath,
		worktreeRoot:       request.WorktreeRoot,
		worktreePath:       filepath.Join(request.WorktreeRoot, attemptRelative),
		deliveryRelative:   deliveryRelative,
		attemptRelative:    attemptRelative,
		baseCommit:         request.BaseCommit,
		repositoryCommon:   common,
		repositoryPhysical: repositoryPhysical,
		rootPhysical:       rootPhysical,
	}, nil
}

func (m *WorktreeManager) inspectResolved(
	ctx context.Context,
	resolved resolvedRequest,
	requireClean bool,
) (Worktree, error) {
	physical, err := canonicalDirectory(resolved.worktreePath, "attempt worktree")
	if err != nil {
		return Worktree{}, err
	}
	expectedPhysical := filepath.Join(resolved.rootPhysical, resolved.attemptRelative)
	if physical != expectedPhysical {
		return Worktree{}, fmt.Errorf("attempt worktree traverses a symlink")
	}
	top, common, err := m.worktreeIdentity(ctx, resolved.worktreePath)
	if err != nil {
		return Worktree{}, fmt.Errorf("inspect attempt worktree identity: %w", err)
	}
	if top != physical || common != resolved.repositoryCommon {
		return Worktree{}, fmt.Errorf("attempt worktree belongs to another Git identity")
	}
	head, err := m.git(ctx, resolved.worktreePath, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return Worktree{}, fmt.Errorf("inspect attempt HEAD: %w", err)
	}
	if string(bytes.TrimSpace(head)) != resolved.baseCommit {
		return Worktree{}, fmt.Errorf("%w: attempt HEAD differs from approved base", ErrWorktreeConflict)
	}
	branch, err := m.git(ctx, resolved.worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return Worktree{}, fmt.Errorf("inspect detached HEAD: %w", err)
	}
	if string(bytes.TrimSpace(branch)) != "HEAD" {
		return Worktree{}, fmt.Errorf("attempt worktree must use detached HEAD")
	}
	if err := m.validateIndexFlags(ctx, resolved.worktreePath); err != nil {
		return Worktree{}, err
	}
	if requireClean {
		if err := m.validateClean(ctx, resolved.worktreePath); err != nil {
			return Worktree{}, err
		}
	}
	return Worktree{
		DeliveryID:     resolved.request.DeliveryID,
		AttemptID:      resolved.request.AttemptID,
		RepositoryPath: resolved.repositoryPath,
		Path:           resolved.worktreePath,
		CommonDir:      common,
		BaseCommit:     resolved.baseCommit,
	}, nil
}

// removeFreshCleanResolved is used only to roll back Prepare before the path is
// returned to a worker. Post-exposure deletion requires a future live-lease and
// process-quiescence proof and deliberately has no public entry point here.
func (m *WorktreeManager) removeFreshCleanResolved(ctx context.Context, resolved resolvedRequest) error {
	if _, err := m.inspectResolved(ctx, resolved, true); err != nil {
		return err
	}
	if _, err := m.gitMutation(
		ctx,
		resolved.repositoryPath,
		"worktree", "remove", resolved.worktreePath,
	); err != nil {
		return fmt.Errorf("remove clean worktree: %w", err)
	}
	if _, err := os.Lstat(resolved.worktreePath); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removed worktree path still exists")
	}
	root, err := os.OpenRoot(resolved.worktreeRoot)
	if err == nil {
		defer root.Close()
		_ = root.Remove(resolved.deliveryRelative)
	}
	return nil
}

func (m *WorktreeManager) validateClean(ctx context.Context, worktree string) error {
	status, err := m.git(
		ctx,
		worktree,
		"status", "--porcelain=v1", "-z", "--untracked-files=all",
	)
	if err != nil {
		return fmt.Errorf("inspect worktree status: %w", err)
	}
	if len(status) != 0 {
		return ErrWorktreeDirty
	}
	ignored, err := m.git(
		ctx,
		worktree,
		"ls-files", "--others", "--ignored", "--exclude-standard", "-z",
	)
	if err != nil {
		return fmt.Errorf("inspect ignored worktree paths: %w", err)
	}
	if len(ignored) != 0 {
		return fmt.Errorf("%w: ignored paths are present", ErrWorktreeDirty)
	}
	return nil
}

func (m *WorktreeManager) validateIndexFlags(ctx context.Context, worktree string) error {
	output, err := m.git(ctx, worktree, "ls-files", "-v", "-z")
	if err != nil {
		return fmt.Errorf("inspect worktree index flags: %w", err)
	}
	for _, entry := range bytes.Split(output, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		if len(entry) < 3 || entry[0] != 'H' || entry[1] != ' ' {
			return fmt.Errorf("worktree index contains unsupported hidden path flags")
		}
	}
	return nil
}

func (m *WorktreeManager) validateRepositoryExecutionConfig(
	ctx context.Context,
	repository string,
) error {
	output, err := m.git(ctx, repository, "config", "--includes", "--null", "--list")
	if err != nil {
		return fmt.Errorf("inspect repository-local Git configuration: %w", err)
	}
	for _, entry := range bytes.Split(output, []byte{0}) {
		key, _, _ := bytes.Cut(entry, []byte{'\n'})
		name := strings.ToLower(string(key))
		if !strings.HasPrefix(name, "filter.") {
			continue
		}
		if strings.HasSuffix(name, ".clean") ||
			strings.HasSuffix(name, ".smudge") ||
			strings.HasSuffix(name, ".process") {
			return fmt.Errorf("repository-local Git content filters are not permitted")
		}
	}
	return nil
}

func (m *WorktreeManager) worktreeIdentity(
	ctx context.Context,
	directory string,
) (string, string, error) {
	output, err := m.git(
		ctx,
		directory,
		"rev-parse", "--show-toplevel", "--git-common-dir",
	)
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 2 || lines[0] == "" || lines[1] == "" {
		return "", "", fmt.Errorf("git returned an invalid worktree identity")
	}
	top, err := canonicalGitPath(directory, lines[0])
	if err != nil {
		return "", "", err
	}
	common, err := canonicalGitPath(directory, lines[1])
	if err != nil {
		return "", "", err
	}
	return top, common, nil
}

func (m *WorktreeManager) git(
	ctx context.Context,
	directory string,
	arguments ...string,
) ([]byte, error) {
	return m.gitWithTimeout(ctx, m.readTimeout, directory, arguments...)
}

func (m *WorktreeManager) gitMutation(
	ctx context.Context,
	directory string,
	arguments ...string,
) ([]byte, error) {
	return m.gitWithTimeout(ctx, m.mutationTimeout, directory, arguments...)
}

func (m *WorktreeManager) gitWithTimeout(
	ctx context.Context,
	timeout time.Duration,
	directory string,
	arguments ...string,
) ([]byte, error) {
	args := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "advice.detachedHead=false",
		"-C", directory,
	}
	args = append(args, arguments...)
	gitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(gitContext, m.gitPath, args...)
	command.Env = m.environ
	stdout := &boundedBuffer{limit: maximumGitOutputBytes}
	stderr := &boundedBuffer{limit: maximumGitOutputBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if stdout.exceeded || stderr.exceeded {
		return nil, fmt.Errorf("git output exceeded %d bytes", maximumGitOutputBytes)
	}
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(arguments, " "), detail)
	}
	return stdout.Bytes(), nil
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.limit - b.buffer.Len()
	if remaining < len(value) {
		b.exceeded = true
		if remaining < 0 {
			remaining = 0
		}
		value = value[:remaining]
	}
	_, _ = b.buffer.Write(value)
	return original, nil
}

func (b *boundedBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *boundedBuffer) String() string { return b.buffer.String() }

func prepareWritableRoots(worktree string, request contracts.DeliveryRequest) error {
	root, err := os.OpenRoot(worktree)
	if err != nil {
		return fmt.Errorf("open attempt worktree root: %w", err)
	}
	defer root.Close()
	scopes := append([]string(nil), request.WriteScopes...)
	scopes = append(scopes, request.ArtifactScopes...)
	slices.Sort(scopes)
	scopes = slices.Compact(scopes)
	for _, scope := range scopes {
		name := filepath.FromSlash(scope)
		if err := root.MkdirAll(name, 0o700); err != nil {
			return fmt.Errorf("prepare writable scope %q: %w", scope, err)
		}
		if err := validateScopePosition(worktree, name, scope); err != nil {
			return err
		}
	}
	return nil
}

func validateScopePosition(worktree string, name string, scope string) error {
	logical := filepath.Join(worktree, name)
	info, err := os.Lstat(logical)
	if err != nil {
		return fmt.Errorf("stat writable scope %q: %w", scope, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("writable scope %q must be a real directory", scope)
	}
	physicalRoot, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return fmt.Errorf("resolve attempt worktree: %w", err)
	}
	physical, err := filepath.EvalSymlinks(logical)
	if err != nil {
		return fmt.Errorf("resolve writable scope %q: %w", scope, err)
	}
	expected := filepath.Join(physicalRoot, name)
	if physical != expected {
		return fmt.Errorf("writable scope %q traverses a symlink", scope)
	}
	return nil
}

func canonicalDirectory(value string, label string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n") || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return "", fmt.Errorf("%s path must be absolute, clean, and single-line", label)
	}
	info, err := os.Lstat(value)
	if err != nil {
		return "", fmt.Errorf("stat %s path: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%s path must be a real directory", label)
	}
	physical, err := filepath.EvalSymlinks(value)
	if err != nil {
		return "", fmt.Errorf("resolve %s path: %w", label, err)
	}
	return physical, nil
}

func canonicalGitPath(base string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("Git returned an invalid path")
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}
	absolute, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func physicalPathContains(root string, candidate string) (bool, error) {
	rootInfo, err := os.Stat(root)
	if err != nil {
		return false, err
	}
	current := candidate
	for {
		currentInfo, err := os.Stat(current)
		if err != nil {
			return false, err
		}
		if os.SameFile(rootInfo, currentInfo) {
			return true, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false, nil
		}
		current = parent
	}
}

func ensureRequestAuthority(
	worktreeRoot string,
	request contracts.DeliveryRequest,
	create bool,
) error {
	canonical, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode delivery authority: %w", err)
	}
	digest := sha256.Sum256(canonical)
	expected := []byte(fmt.Sprintf("%x\n", digest))
	root, err := os.OpenRoot(worktreeRoot)
	if err != nil {
		return fmt.Errorf("open worktree root for authority binding: %w", err)
	}
	defer root.Close()
	parent := filepath.Join(".forja-authority", request.DeliveryID)
	name := filepath.Join(parent, request.AttemptID+".sha256")
	if create {
		if err := root.MkdirAll(parent, 0o700); err != nil {
			return fmt.Errorf("create delivery authority namespace: %w", err)
		}
	} else {
		info, err := root.Lstat(parent)
		if err != nil {
			return fmt.Errorf("inspect delivery authority namespace: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("delivery authority namespace is not a real directory")
		}
	}
	if err := validateScopePosition(worktreeRoot, parent, parent); err != nil {
		return fmt.Errorf("validate delivery authority namespace: %w", err)
	}
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return fmt.Errorf("delivery authority binding is missing")
		}
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("create delivery authority binding: %w", err)
		}
		if _, err := file.Write(expected); err != nil {
			_ = file.Close()
			return fmt.Errorf("write delivery authority binding: %w", err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return fmt.Errorf("sync delivery authority binding: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close delivery authority binding: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect delivery authority binding: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() != int64(len(expected)) {
		return fmt.Errorf("delivery authority binding is not a canonical regular file")
	}
	actual, err := root.ReadFile(name)
	if err != nil {
		return fmt.Errorf("read delivery authority binding: %w", err)
	}
	if !bytes.Equal(actual, expected) {
		return fmt.Errorf("%w: attempt ID is bound to another delivery request", ErrWorktreeConflict)
	}
	return nil
}

func gitEnvironment(source []string) []string {
	allowed := map[string]struct{}{
		"HOME":   {},
		"PATH":   {},
		"TEMP":   {},
		"TMP":    {},
		"TMPDIR": {},
	}
	values := make(map[string]string, len(allowed))
	for _, entry := range source {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, accepted := allowed[key]; accepted {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]string, 0, len(keys)+5)
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return append(
		result,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"LC_ALL=C",
	)
}
