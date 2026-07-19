// Package indexing extracts deterministic repository metadata from committed source.
package indexing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

const (
	MaximumRepositoryFiles       = 100000
	MaximumRepositorySourceBytes = int64(2 << 30)
	maximumGitMetadataBytes      = 64 << 20
	maximumGitErrorBytes         = 64 << 10
)

var (
	ErrUnsupportedGitEntry = errors.New("unsupported Git tree entry")
	ErrRepositoryLimit     = errors.New("repository indexing limit exceeded")
	ErrGitIntegrity        = errors.New("Git object integrity failure")
)

type GitRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type ExecGitRunner struct {
	RepositoryPath string
}

func (r ExecGitRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "git" {
		return nil, fmt.Errorf("unsupported executable %q", name)
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("Git arguments are required")
	}
	root, err := filepath.Abs(r.RepositoryPath)
	if err != nil {
		return nil, fmt.Errorf("resolve repository path: %w", err)
	}
	commandArgs := append([]string{"--no-optional-locks", "-C", root}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Env = []string{
		"HOME=/nonexistent",
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"LANG=C",
		"LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_TERMINAL_PROMPT=0",
	}
	stdout := &boundedBuffer{limit: gitOutputLimit(args)}
	stderr := &boundedBuffer{limit: maximumGitErrorBytes}
	command.Stdout, command.Stderr = stdout, stderr
	runErr := command.Run()
	if stdout.exceeded || stderr.exceeded {
		return nil, fmt.Errorf("%w: git %s exceeded output limit", ErrRepositoryLimit, args[0])
	}
	if runErr != nil {
		if errors.As(runErr, new(*exec.ExitError)) {
			message := strings.TrimSpace(stderr.String())
			if message == "" {
				message = runErr.Error()
			}
			return nil, fmt.Errorf("git %s failed: %s", args[0], message)
		}
		return nil, fmt.Errorf("run git %s: %w", args[0], runErr)
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

func gitOutputLimit(args []string) int {
	if len(args) == 0 {
		return 0
	}
	switch args[0] {
	case "rev-parse":
		return 1024
	case "show":
		return 4096
	case "cat-file":
		return int(contracts.MaximumIndexedFileBytes) + 1
	default:
		return maximumGitMetadataBytes
	}
}

type CommittedFile struct {
	Path      string
	Mode      string
	GitBlobID string
	SizeBytes int64
	Language  string
	Generated bool
}

type CommittedTree struct {
	CommitID    string
	TreeID      string
	CommittedAt time.Time
	Files       []CommittedFile
}

type GitChange struct {
	Kind     string
	Path     string
	FromPath *string
}

type GitChangeSet struct {
	BaseCommit   string
	TargetCommit string
	Changes      []GitChange
}

type GitSource struct {
	runner GitRunner
}

func NewGitSource(runner GitRunner) (*GitSource, error) {
	if runner == nil {
		return nil, fmt.Errorf("Git runner is required")
	}
	return &GitSource{runner: runner}, nil
}

func (s *GitSource) InspectCommit(ctx context.Context, revision string) (CommittedTree, error) {
	if strings.TrimSpace(revision) == "" || len(revision) > 256 || strings.HasPrefix(revision, "-") {
		return CommittedTree{}, fmt.Errorf("Git revision is invalid")
	}
	commitOutput, err := s.runner.Run(ctx, "git", "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return CommittedTree{}, err
	}
	commitID := strings.TrimSpace(string(commitOutput))
	if !validGitObjectID(commitID) {
		return CommittedTree{}, fmt.Errorf("resolved commit ID is invalid")
	}
	treeOutput, err := s.runner.Run(ctx, "git", "rev-parse", "--verify", commitID+"^{tree}")
	if err != nil {
		return CommittedTree{}, err
	}
	treeID := strings.TrimSpace(string(treeOutput))
	if !validGitObjectID(treeID) {
		return CommittedTree{}, fmt.Errorf("resolved tree ID is invalid")
	}
	committedAtOutput, err := s.runner.Run(ctx, "git", "show", "-s", "--format=%cI", commitID)
	if err != nil {
		return CommittedTree{}, err
	}
	committedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(string(committedAtOutput)))
	if err != nil {
		return CommittedTree{}, fmt.Errorf("committed timestamp is invalid: %w", err)
	}
	listing, err := s.runner.Run(ctx, "git", "ls-tree", "-rlz", "--full-tree", commitID)
	if err != nil {
		return CommittedTree{}, err
	}
	files, err := parseTreeListing(listing)
	if err != nil {
		return CommittedTree{}, err
	}
	return CommittedTree{CommitID: commitID, TreeID: treeID, CommittedAt: committedAt.UTC(), Files: files}, nil
}

func (s *GitSource) ReadFile(ctx context.Context, file CommittedFile) ([]byte, string, error) {
	if !validGitObjectID(file.GitBlobID) || file.SizeBytes < 0 || file.SizeBytes > contracts.MaximumIndexedFileBytes {
		return nil, "", fmt.Errorf("%w: invalid committed file descriptor", ErrGitIntegrity)
	}
	body, err := s.runner.Run(ctx, "git", "cat-file", "blob", file.GitBlobID)
	if err != nil {
		return nil, "", err
	}
	if int64(len(body)) != file.SizeBytes {
		return nil, "", fmt.Errorf("%w: blob size changed", ErrGitIntegrity)
	}
	if isIndexedTextLanguage(file.Language) && !utf8.Valid(body) {
		return nil, "", fmt.Errorf("%w: %s source is not UTF-8", ErrUnsupportedGitEntry, file.Language)
	}
	digest := sha256.Sum256(body)
	return body, "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (s *GitSource) ChangeSet(ctx context.Context, baseRevision, targetRevision string) (GitChangeSet, error) {
	base, err := s.resolveCommit(ctx, baseRevision)
	if err != nil {
		return GitChangeSet{}, err
	}
	target, err := s.resolveCommit(ctx, targetRevision)
	if err != nil {
		return GitChangeSet{}, err
	}
	output, err := s.runner.Run(ctx, "git", "diff", "--no-ext-diff", "--no-textconv", "--name-status", "-z", "--find-renames", base, target, "--")
	if err != nil {
		return GitChangeSet{}, err
	}
	changes, err := parseNameStatus(output)
	if err != nil {
		return GitChangeSet{}, err
	}
	return GitChangeSet{BaseCommit: base, TargetCommit: target, Changes: changes}, nil
}

func (s *GitSource) resolveCommit(ctx context.Context, revision string) (string, error) {
	if strings.TrimSpace(revision) == "" || len(revision) > 256 || strings.HasPrefix(revision, "-") {
		return "", fmt.Errorf("Git revision is invalid")
	}
	output, err := s.runner.Run(ctx, "git", "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(output))
	if !validGitObjectID(commit) {
		return "", fmt.Errorf("resolved commit ID is invalid")
	}
	return commit, nil
}

func parseTreeListing(value []byte) ([]CommittedFile, error) {
	entries := bytes.Split(value, []byte{0})
	files := make([]CommittedFile, 0, len(entries))
	seenPath := make(map[string]struct{}, len(entries))
	seenFold := make(map[string]string, len(entries))
	var totalSize int64
	for _, entry := range entries {
		if len(entry) == 0 {
			continue
		}
		header, rawPath, ok := bytes.Cut(entry, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("%w: malformed tree record", ErrGitIntegrity)
		}
		fields := strings.Fields(string(header))
		if len(fields) != 4 {
			return nil, fmt.Errorf("%w: malformed tree header", ErrGitIntegrity)
		}
		mode, objectType, objectID, sizeValue := fields[0], fields[1], fields[2], fields[3]
		if objectType != "blob" || mode == "120000" || mode == "160000" {
			return nil, fmt.Errorf("%w: mode=%s type=%s", ErrUnsupportedGitEntry, mode, objectType)
		}
		if mode != "100644" && mode != "100755" || !validGitObjectID(objectID) {
			return nil, fmt.Errorf("%w: invalid blob descriptor", ErrUnsupportedGitEntry)
		}
		size, err := strconv.ParseInt(sizeValue, 10, 64)
		if err != nil || size < 0 || size > contracts.MaximumIndexedFileBytes {
			return nil, fmt.Errorf("%w: file size %q", ErrRepositoryLimit, sizeValue)
		}
		if !utf8.Valid(rawPath) {
			return nil, fmt.Errorf("%w: path is not UTF-8", ErrUnsupportedGitEntry)
		}
		canonicalPath, err := contracts.NormalizeRepositoryPath(string(rawPath))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrGitIntegrity, err)
		}
		if _, exists := seenPath[canonicalPath]; exists {
			return nil, fmt.Errorf("%w: duplicate path", ErrGitIntegrity)
		}
		folded := strings.ToLower(canonicalPath)
		if previous, exists := seenFold[folded]; exists && previous != canonicalPath {
			return nil, fmt.Errorf("%w: case-colliding paths", ErrUnsupportedGitEntry)
		}
		seenPath[canonicalPath] = struct{}{}
		seenFold[folded] = canonicalPath
		totalSize += size
		if totalSize > MaximumRepositorySourceBytes || len(files) >= MaximumRepositoryFiles {
			return nil, ErrRepositoryLimit
		}
		files = append(files, CommittedFile{
			Path: canonicalPath, Mode: mode, GitBlobID: objectID, SizeBytes: size,
			Language: detectLanguage(canonicalPath), Generated: detectGeneratedPath(canonicalPath),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func parseNameStatus(value []byte) ([]GitChange, error) {
	parts := bytes.Split(value, []byte{0})
	changes := make([]GitChange, 0, len(parts)/2)
	for index := 0; index < len(parts); {
		if len(parts[index]) == 0 {
			index++
			continue
		}
		status := string(parts[index])
		index++
		if index >= len(parts) || len(parts[index]) == 0 {
			return nil, fmt.Errorf("%w: missing changed path", ErrGitIntegrity)
		}
		if !utf8.Valid(parts[index]) {
			return nil, fmt.Errorf("%w: changed path is not UTF-8", ErrUnsupportedGitEntry)
		}
		first, err := contracts.NormalizeRepositoryPath(string(parts[index]))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrGitIntegrity, err)
		}
		index++
		kind := ""
		change := GitChange{}
		switch status[0] {
		case 'A':
			kind, change.Path = "added", first
		case 'M', 'T':
			kind, change.Path = "modified", first
		case 'D':
			kind, change.Path = "deleted", first
		case 'R':
			if index >= len(parts) || len(parts[index]) == 0 {
				return nil, fmt.Errorf("%w: missing rename target", ErrGitIntegrity)
			}
			if !utf8.Valid(parts[index]) {
				return nil, fmt.Errorf("%w: rename target is not UTF-8", ErrUnsupportedGitEntry)
			}
			second, normalizeErr := contracts.NormalizeRepositoryPath(string(parts[index]))
			if normalizeErr != nil {
				return nil, fmt.Errorf("%w: %v", ErrGitIntegrity, normalizeErr)
			}
			index++
			kind, change.Path, change.FromPath = "renamed", second, &first
		default:
			return nil, fmt.Errorf("%w: unsupported change status %q", ErrUnsupportedGitEntry, status)
		}
		change.Kind = kind
		changes = append(changes, change)
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Path == changes[j].Path {
			return changes[i].Kind < changes[j].Kind
		}
		return changes[i].Path < changes[j].Path
	})
	return changes, nil
}

func isIndexedTextLanguage(language string) bool {
	switch language {
	case "go", "python", "typescript", "javascript":
		return true
	default:
		return false
	}
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func detectLanguage(path string) string {
	extension := strings.ToLower(filepath.Ext(path))
	switch extension {
	case ".go":
		return "go"
	case ".ts", ".tsx", ".mts", ".cts":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".py", ".pyi":
		return "python"
	case ".json", ".jsonc":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".md", ".mdx":
		return "markdown"
	default:
		return "other"
	}
}

func detectGeneratedPath(path string) bool {
	lower := strings.ToLower(path)
	base := strings.ToLower(filepath.Base(path))
	return strings.Contains(lower, "/generated/") || strings.HasPrefix(lower, "generated/") ||
		strings.HasSuffix(base, ".generated.go") || strings.HasSuffix(base, ".gen.go") ||
		strings.HasSuffix(base, ".generated.ts") || strings.HasSuffix(base, "_pb2.py") ||
		base == "package-lock.json" || base == "go.sum"
}
