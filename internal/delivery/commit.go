package delivery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

// CommitResult is the immutable Git identity produced from one worker attempt.
type CommitResult struct {
	BaseCommit   string
	ResultCommit string
	ResultTree   string
	PatchSHA256  string
	ChangedPaths []string
}

// CreateResultCommit snapshots worker bytes through a temporary index and
// creates a deterministic supervisor-owned commit without changing the
// attempt's HEAD or index.
func (m *WorktreeManager) CreateResultCommit(
	ctx context.Context,
	request contracts.DeliveryRequest,
) (result CommitResult, resultErr error) {
	resolved, err := m.resolveRequest(ctx, request)
	if err != nil {
		return CommitResult{}, err
	}
	release, err := acquireAttemptLock(ctx, resolved)
	if err != nil {
		return CommitResult{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, release()) }()
	if err := ensureRequestAuthority(resolved.worktreeRoot, request, false); err != nil {
		return CommitResult{}, err
	}
	if _, err := m.inspectResolved(ctx, resolved, false); err != nil {
		return CommitResult{}, err
	}
	if err := m.validateIgnoredAbsent(ctx, resolved.worktreePath); err != nil {
		return CommitResult{}, err
	}

	workspace, err := m.snapshotWorkspace(ctx, resolved)
	if err != nil {
		return CommitResult{}, err
	}
	allowedScopes := append([]string(nil), request.WriteScopes...)
	allowedScopes = append(allowedScopes, request.ArtifactScopes...)
	for _, changedPath := range workspace.changedPaths {
		if !pathCoveredByScopes(changedPath, allowedScopes) {
			return CommitResult{}, fmt.Errorf("changed path %q is outside approved write and artifact scopes", changedPath)
		}
	}
	tree, _, err := m.snapshotTree(ctx, resolved, request.WriteScopes)
	if err != nil {
		return CommitResult{}, err
	}
	baseTree, err := m.git(
		ctx, resolved.repositoryPath,
		"rev-parse", "--verify", resolved.baseCommit+"^{tree}",
	)
	if err != nil {
		return CommitResult{}, fmt.Errorf("resolve base tree: %w", err)
	}
	baseTree = bytes.TrimSpace(baseTree)
	if tree == string(baseTree) {
		return CommitResult{}, fmt.Errorf("delivery attempt contains no changes")
	}

	commitTime, err := m.deterministicCommitTime(ctx, resolved)
	if err != nil {
		return CommitResult{}, err
	}
	message := []byte("Forja delivery " + request.DeliveryID + "\n")
	timestamp := strconv.FormatInt(commitTime.Unix(), 10) + " +0000"
	commit, err := m.gitWithInputAndEnvironment(
		ctx,
		m.mutationTimeout,
		resolved.repositoryPath,
		message,
		map[string]string{
			"GIT_AUTHOR_NAME":     "Forja Delivery Service",
			"GIT_AUTHOR_EMAIL":    "delivery@forja.invalid",
			"GIT_AUTHOR_DATE":     timestamp,
			"GIT_COMMITTER_NAME":  "Forja Delivery Service",
			"GIT_COMMITTER_EMAIL": "delivery@forja.invalid",
			"GIT_COMMITTER_DATE":  timestamp,
		},
		"commit-tree", tree, "-p", resolved.baseCommit,
	)
	if err != nil {
		return CommitResult{}, fmt.Errorf("create supervisor result commit: %w", err)
	}
	resultCommit := strings.TrimSpace(string(commit))
	if !fullObjectIDPattern.MatchString(resultCommit) {
		return CommitResult{}, fmt.Errorf("Git returned a noncanonical result commit")
	}

	secondWorkspace, err := m.snapshotWorkspace(ctx, resolved)
	if err != nil {
		return CommitResult{}, err
	}
	secondTree, _, err := m.snapshotTree(ctx, resolved, request.WriteScopes)
	if err != nil {
		return CommitResult{}, err
	}
	if secondTree != tree || secondWorkspace.digest != workspace.digest ||
		!slices.Equal(secondWorkspace.changedPaths, workspace.changedPaths) {
		return CommitResult{}, fmt.Errorf("%w: worker bytes changed while creating the result commit", ErrWorktreeDirty)
	}
	result, err = m.inspectCommitResult(ctx, resolved, resultCommit)
	if err != nil {
		return CommitResult{}, err
	}
	if result.ResultTree != tree {
		return CommitResult{}, fmt.Errorf("result commit tree disagrees with the staged snapshot")
	}
	for _, changedPath := range result.ChangedPaths {
		if !pathCoveredByScopes(changedPath, request.WriteScopes) {
			return CommitResult{}, fmt.Errorf("changed path %q is outside approved write scopes", changedPath)
		}
	}
	return result, nil
}

type workspaceSnapshot struct {
	changedPaths []string
	digest       string
}

func (m *WorktreeManager) snapshotWorkspace(
	ctx context.Context,
	resolved resolvedRequest,
) (workspaceSnapshot, error) {
	tracked, err := m.git(
		ctx, resolved.worktreePath,
		"diff", "--no-ext-diff", "--no-renames", "--name-only", "-z",
		resolved.baseCommit, "--",
	)
	if err != nil {
		return workspaceSnapshot{}, fmt.Errorf("enumerate tracked worker changes: %w", err)
	}
	trackedPaths, err := parseChangedPaths(tracked)
	if err != nil {
		return workspaceSnapshot{}, err
	}
	untracked, err := m.git(
		ctx, resolved.worktreePath,
		"ls-files", "--others", "--exclude-standard", "-z",
	)
	if err != nil {
		return workspaceSnapshot{}, fmt.Errorf("enumerate untracked worker changes: %w", err)
	}
	untrackedPaths, err := parseChangedPaths(untracked)
	if err != nil {
		return workspaceSnapshot{}, err
	}
	changedPaths := append(trackedPaths, untrackedPaths...)
	slices.Sort(changedPaths)
	changedPaths = slices.Compact(changedPaths)
	root, err := os.OpenRoot(resolved.worktreePath)
	if err != nil {
		return workspaceSnapshot{}, fmt.Errorf("open worktree for stable snapshot: %w", err)
	}
	defer root.Close()
	digest := sha256.New()
	for _, changedPath := range changedPaths {
		if err := hashWorkspacePath(root, digest, changedPath); err != nil {
			return workspaceSnapshot{}, err
		}
	}
	return workspaceSnapshot{
		changedPaths: changedPaths,
		digest:       fmt.Sprintf("%x", digest.Sum(nil)),
	}, nil
}

func hashWorkspacePath(root *os.Root, digest io.Writer, repositoryPath string) error {
	if err := validateRepositoryRelativePath(repositoryPath); err != nil {
		return err
	}
	if err := writeDigestField(digest, []byte(repositoryPath)); err != nil {
		return err
	}
	name := filepath.FromSlash(repositoryPath)
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return writeDigestField(digest, []byte("deleted"))
	}
	if err != nil {
		return fmt.Errorf("inspect changed path %q: %w", repositoryPath, err)
	}
	mode := []byte(info.Mode().String())
	if err := writeDigestField(digest, mode); err != nil {
		return err
	}
	switch {
	case info.Mode().IsRegular():
		file, err := root.Open(name)
		if err != nil {
			return fmt.Errorf("open changed file %q: %w", repositoryPath, err)
		}
		contentDigest := sha256.New()
		_, copyErr := io.Copy(contentDigest, file)
		after, statErr := file.Stat()
		closeErr := file.Close()
		if copyErr != nil || statErr != nil || closeErr != nil {
			return errors.Join(
				fmt.Errorf("hash changed file %q", repositoryPath), copyErr, statErr, closeErr,
			)
		}
		if info.Size() != after.Size() || info.Mode() != after.Mode() ||
			!info.ModTime().Equal(after.ModTime()) {
			return fmt.Errorf("%w: changed file %q mutated while hashing", ErrWorktreeDirty, repositoryPath)
		}
		return writeDigestField(digest, contentDigest.Sum(nil))
	case info.Mode()&os.ModeSymlink != 0:
		target, err := root.Readlink(name)
		if err != nil {
			return fmt.Errorf("read changed symlink %q: %w", repositoryPath, err)
		}
		return writeDigestField(digest, []byte(target))
	case info.IsDir():
		return writeDigestField(digest, []byte("directory"))
	default:
		return writeDigestField(digest, []byte("special"))
	}
}

func writeDigestField(destination io.Writer, value []byte) error {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	if _, err := destination.Write(length[:]); err != nil {
		return err
	}
	_, err := destination.Write(value)
	return err
}

func (m *WorktreeManager) inspectCommitResult(
	ctx context.Context,
	resolved resolvedRequest,
	resultCommit string,
) (CommitResult, error) {
	if !fullObjectIDPattern.MatchString(resultCommit) {
		return CommitResult{}, fmt.Errorf("result commit must be an exact 40-character object ID")
	}
	parents, err := m.git(
		ctx, resolved.repositoryPath,
		"rev-list", "--parents", "-n", "1", resultCommit,
	)
	if err != nil {
		return CommitResult{}, fmt.Errorf("inspect result commit parent: %w", err)
	}
	fields := strings.Fields(string(parents))
	if len(fields) != 2 || fields[0] != resultCommit || fields[1] != resolved.baseCommit {
		return CommitResult{}, fmt.Errorf("result commit must have only the approved base as parent")
	}
	tree, err := m.git(
		ctx, resolved.repositoryPath,
		"rev-parse", "--verify", resultCommit+"^{tree}",
	)
	if err != nil {
		return CommitResult{}, fmt.Errorf("resolve result tree: %w", err)
	}
	tree = bytes.TrimSpace(tree)
	if !fullObjectIDPattern.Match(tree) {
		return CommitResult{}, fmt.Errorf("Git returned a noncanonical result tree")
	}
	changed, err := m.git(
		ctx, resolved.repositoryPath,
		"diff-tree", "--no-commit-id", "--name-only", "--no-renames", "-r", "-z",
		resolved.baseCommit, resultCommit, "--",
	)
	if err != nil {
		return CommitResult{}, fmt.Errorf("capture changed paths: %w", err)
	}
	changedPaths, err := parseChangedPaths(changed)
	if err != nil {
		return CommitResult{}, err
	}
	if len(changedPaths) == 0 {
		return CommitResult{}, fmt.Errorf("result commit contains no changed paths")
	}
	patch, err := m.git(
		ctx, resolved.repositoryPath,
		"diff", "--binary", "--full-index", "--no-ext-diff", "--no-color", "--no-renames",
		resolved.baseCommit, resultCommit, "--",
	)
	if err != nil {
		return CommitResult{}, fmt.Errorf("capture canonical binary patch: %w", err)
	}
	digest := sha256.Sum256(patch)
	return CommitResult{
		BaseCommit:   resolved.baseCommit,
		ResultCommit: resultCommit,
		ResultTree:   string(tree),
		PatchSHA256:  fmt.Sprintf("%x", digest),
		ChangedPaths: changedPaths,
	}, nil
}

func (m *WorktreeManager) snapshotTree(
	ctx context.Context,
	resolved resolvedRequest,
	scopes []string,
) (string, []string, error) {
	index, err := os.CreateTemp(resolved.worktreeRoot, ".forja-index-*")
	if err != nil {
		return "", nil, fmt.Errorf("reserve temporary Git index: %w", err)
	}
	indexPath := index.Name()
	if err := index.Close(); err != nil {
		_ = os.Remove(indexPath)
		return "", nil, fmt.Errorf("close temporary Git index reservation: %w", err)
	}
	if err := os.Remove(indexPath); err != nil {
		return "", nil, fmt.Errorf("prepare temporary Git index: %w", err)
	}
	defer os.Remove(indexPath)
	overrides := map[string]string{"GIT_INDEX_FILE": indexPath}
	if _, err := m.gitWithInputAndEnvironment(
		ctx, m.mutationTimeout, resolved.worktreePath, nil, overrides,
		"read-tree", resolved.baseCommit,
	); err != nil {
		return "", nil, fmt.Errorf("seed temporary Git index: %w", err)
	}
	for _, scope := range scopes {
		if _, err := m.gitWithInputAndEnvironment(
			ctx, m.mutationTimeout, resolved.worktreePath, nil, overrides,
			"add", "--all", "--", scope,
		); err != nil {
			return "", nil, fmt.Errorf("snapshot worker bytes in %q: %w", scope, err)
		}
	}
	tree, err := m.gitWithInputAndEnvironment(
		ctx, m.mutationTimeout, resolved.worktreePath, nil, overrides,
		"write-tree",
	)
	if err != nil {
		return "", nil, fmt.Errorf("write result tree: %w", err)
	}
	value := strings.TrimSpace(string(tree))
	if !fullObjectIDPattern.MatchString(value) {
		return "", nil, fmt.Errorf("Git returned a noncanonical snapshot tree")
	}
	changed, err := m.gitWithInputAndEnvironment(
		ctx, m.readTimeout, resolved.worktreePath, nil, overrides,
		"diff-index", "--cached", "--name-only", "--no-renames", "-z",
		resolved.baseCommit, "--",
	)
	if err != nil {
		return "", nil, fmt.Errorf("capture snapshot paths: %w", err)
	}
	changedPaths, err := parseChangedPaths(changed)
	if err != nil {
		return "", nil, err
	}
	return value, changedPaths, nil
}

func (m *WorktreeManager) deterministicCommitTime(
	ctx context.Context,
	resolved resolvedRequest,
) (time.Time, error) {
	output, err := m.git(
		ctx, resolved.repositoryPath,
		"show", "-s", "--format=%ct", resolved.baseCommit,
	)
	if err != nil {
		return time.Time{}, fmt.Errorf("read base commit time: %w", err)
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil || seconds == math.MaxInt64 {
		return time.Time{}, fmt.Errorf("base commit has an invalid timestamp")
	}
	return time.Unix(seconds+1, 0).UTC(), nil
}

func (m *WorktreeManager) validateIgnoredAbsent(ctx context.Context, worktree string) error {
	ignored, err := m.git(
		ctx, worktree,
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

func parseChangedPaths(output []byte) ([]string, error) {
	entries := bytes.Split(output, []byte{0})
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if len(entry) == 0 {
			continue
		}
		value := string(entry)
		if value == "" || strings.ContainsAny(value, "\\\x00\r\n") ||
			path.IsAbs(value) || path.Clean(value) != value ||
			value == ".." || strings.HasPrefix(value, "../") {
			return nil, fmt.Errorf("Git returned noncanonical changed path %q", value)
		}
		paths = append(paths, value)
	}
	if !byteSortedUnique(paths) {
		return nil, fmt.Errorf("Git returned unsorted or duplicate changed paths")
	}
	return paths, nil
}

func pathCoveredByScopes(value string, scopes []string) bool {
	for _, scope := range scopes {
		if value == scope || strings.HasPrefix(value, scope+"/") {
			return true
		}
	}
	return false
}

func byteSortedUnique(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] >= values[index] {
			return false
		}
	}
	return true
}

var fullObjectIDPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
