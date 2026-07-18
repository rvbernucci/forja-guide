package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var secretPatterns = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"private key", regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`)},
	{"GitHub token", regexp.MustCompile(`\bgh[opsu]_[A-Za-z0-9]{20,}\b`)},
	{"Hugging Face token", regexp.MustCompile(`\bhf_[A-Za-z0-9]{20,}\b`)},
	{"credential URL", regexp.MustCompile(`(?i)\b(?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?)://[^/\s:@]+:[^@\s]+@`)},
	{"AWS access key", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
}

var generatedMarkers = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`),
	regexp.MustCompile(`(?im)^(?:#|//|/\*|<!--)\s*@generated\b`),
	regexp.MustCompile(`(?im)^(?:#|//|/\*|<!--)\s*(?:auto-generated|automatically generated)\b`),
}

func validateChangedPathScopes(changedPaths []string, scopes []string) error {
	if len(changedPaths) == 0 || !byteSortedUnique(changedPaths) {
		return fmt.Errorf("changed paths are empty, duplicated, or unsorted")
	}
	for _, changedPath := range changedPaths {
		if err := validateRepositoryRelativePath(changedPath); err != nil {
			return err
		}
		if !pathCoveredByScopes(changedPath, scopes) {
			return fmt.Errorf("changed path %q is outside approved write scopes", changedPath)
		}
	}
	return nil
}

func (s *ValidationService) validateFilesystemSafety(
	ctx context.Context,
	resolved resolvedRequest,
	result CommitResult,
) error {
	for _, changedPath := range result.ChangedPaths {
		for _, commit := range []string{result.BaseCommit, result.ResultCommit} {
			entry, exists, err := s.gitTreeEntry(ctx, resolved, commit, changedPath)
			if err != nil {
				return err
			}
			if !exists {
				continue
			}
			if entry.objectType != "blob" || (entry.mode != "100644" && entry.mode != "100755") {
				return fmt.Errorf("changed path %q has unsafe Git mode %s", changedPath, entry.mode)
			}
		}
	}
	return nil
}

type gitTreeEntry struct {
	mode       string
	objectType string
	objectID   string
	path       string
}

func (s *ValidationService) gitTreeEntry(
	ctx context.Context,
	resolved resolvedRequest,
	commit string,
	repositoryPath string,
) (gitTreeEntry, bool, error) {
	output, err := s.manager.git(
		ctx, resolved.repositoryPath,
		"ls-tree", "-z", commit, "--", repositoryPath,
	)
	if err != nil {
		return gitTreeEntry{}, false, fmt.Errorf("inspect Git mode for %q: %w", repositoryPath, err)
	}
	if len(output) == 0 {
		return gitTreeEntry{}, false, nil
	}
	entries := bytes.Split(output, []byte{0})
	if len(entries) != 2 || len(entries[0]) == 0 {
		return gitTreeEntry{}, false, fmt.Errorf("git returned ambiguous tree entries for %q", repositoryPath)
	}
	header, name, ok := bytes.Cut(entries[0], []byte{'\t'})
	if !ok || string(name) != repositoryPath {
		return gitTreeEntry{}, false, fmt.Errorf("git returned the wrong tree path for %q", repositoryPath)
	}
	fields := strings.Fields(string(header))
	if len(fields) != 3 || !fullObjectIDPattern.MatchString(fields[2]) {
		return gitTreeEntry{}, false, fmt.Errorf("git returned malformed tree metadata for %q", repositoryPath)
	}
	return gitTreeEntry{fields[0], fields[1], fields[2], string(name)}, true, nil
}

func (s *ValidationService) validateSecrets(
	ctx context.Context,
	resolved resolvedRequest,
	result CommitResult,
) error {
	for _, changedPath := range result.ChangedPaths {
		content, exists, err := s.resultBlob(ctx, resolved, result.ResultCommit, changedPath)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		for _, candidate := range secretPatterns {
			if candidate.pattern.Match(content) {
				return fmt.Errorf("changed path %q contains a %s pattern", changedPath, candidate.name)
			}
		}
	}
	return nil
}

func (s *ValidationService) validateGeneratedFiles(
	ctx context.Context,
	resolved resolvedRequest,
	result CommitResult,
) error {
	for _, changedPath := range result.ChangedPaths {
		for _, commit := range []string{result.BaseCommit, result.ResultCommit} {
			content, exists, err := s.resultBlob(ctx, resolved, commit, changedPath)
			if err != nil {
				return err
			}
			if !exists {
				continue
			}
			if len(content) > 8192 {
				content = content[:8192]
			}
			for _, marker := range generatedMarkers {
				if marker.Match(content) {
					return fmt.Errorf("changed path %q is governed as generated content", changedPath)
				}
			}
		}
	}
	return nil
}

func (s *ValidationService) validateJSONAndSchemas(
	ctx context.Context,
	resolved resolvedRequest,
	result CommitResult,
) error {
	changedSchemas := make(map[string]struct{})
	for _, changedPath := range result.ChangedPaths {
		if !strings.HasSuffix(strings.ToLower(changedPath), ".json") {
			continue
		}
		if strings.HasSuffix(changedPath, ".schema.json") {
			changedSchemas[changedPath] = struct{}{}
		}
		content, exists, err := s.resultBlob(ctx, resolved, result.ResultCommit, changedPath)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if err := validateStrictJSON(content); err != nil {
			return fmt.Errorf("changed JSON %q is invalid: %w", changedPath, err)
		}
		if schemaName, registered := s.registry.bindings[changedPath]; registered {
			if err := s.schemaRegistry.ValidateJSON(schemaName, content); err != nil {
				return fmt.Errorf("changed JSON %q violates %s: %w", changedPath, schemaName, err)
			}
		}
	}
	if len(changedSchemas) == 0 {
		return nil
	}
	return s.compileRepositorySchemas(ctx, resolved, result.ResultCommit)
}

func (s *ValidationService) compileRepositorySchemas(
	ctx context.Context,
	resolved resolvedRequest,
	commit string,
) error {
	output, err := s.manager.git(
		ctx, resolved.repositoryPath,
		"ls-tree", "-r", "--name-only", "-z", commit, "--",
	)
	if err != nil {
		return fmt.Errorf("enumerate repository schemas: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	resources := make(map[string]string)
	for _, raw := range bytes.Split(output, []byte{0}) {
		name := string(raw)
		if !strings.HasSuffix(name, ".schema.json") {
			continue
		}
		content, exists, err := s.resultBlob(ctx, resolved, commit, name)
		if err != nil || !exists {
			if err == nil {
				err = fmt.Errorf("schema disappeared from result tree")
			}
			return fmt.Errorf("read schema %q: %w", name, err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
		if err != nil {
			return fmt.Errorf("decode schema %q: %w", name, err)
		}
		var header struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(content, &header); err != nil {
			return fmt.Errorf("decode schema identity %q: %w", name, err)
		}
		resourceID := strings.TrimSpace(header.ID)
		if resourceID == "" {
			resourceID = name
		}
		if err := compiler.AddResource(resourceID, document); err != nil {
			return fmt.Errorf("register schema %q: %w", name, err)
		}
		resources[name] = resourceID
	}
	names := make([]string, 0, len(resources))
	for name := range resources {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		resourceID := resources[name]
		if _, err := compiler.Compile(resourceID); err != nil {
			return fmt.Errorf("compile repository schema %q: %w", name, err)
		}
	}
	return nil
}

func (s *ValidationService) resultBlob(
	ctx context.Context,
	resolved resolvedRequest,
	commit string,
	repositoryPath string,
) ([]byte, bool, error) {
	entry, exists, err := s.gitTreeEntry(ctx, resolved, commit, repositoryPath)
	if err != nil || !exists {
		return nil, exists, err
	}
	if entry.objectType != "blob" {
		return nil, false, nil
	}
	content, err := s.manager.git(ctx, resolved.repositoryPath, "cat-file", "blob", entry.objectID)
	if err != nil {
		return nil, false, fmt.Errorf("read changed blob %q: %w", repositoryPath, err)
	}
	return content, true, nil
}

func validateStrictJSON(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON token %v", token)
		}
		return fmt.Errorf("read trailing JSON: %w", err)
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object key is not a string")
			}
			if _, duplicate := keys[key]; duplicate {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			keys[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return fmt.Errorf("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return fmt.Errorf("unterminated JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}
