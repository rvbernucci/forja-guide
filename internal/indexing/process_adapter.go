package indexing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

const (
	MaximumAdapterInputBytes  = 16 << 20
	MaximumAdapterOutputBytes = 64 << 20
	MaximumAdapterRuntime     = 2 * time.Minute
)

type ProcessAdapter struct {
	descriptor               contracts.AdapterDescriptor
	expectedToolchainVersion string
	setupErr                 error
	toolRoot                 string
	command                  string
	args                     []string
	languages                map[string]struct{}
}

func NewTypeScriptAdapter(toolRoot string) *ProcessAdapter {
	configuration, setupErr := processAdapterConfigurationHash(
		toolRoot,
		"ts:ES2024;module=ESNext;resolution=Bundler;allowJs;checkJs;noEmit;root-guard;node>=24",
		"adapters/typescript-indexer.mjs", "package.json", "package-lock.json",
	)
	return &ProcessAdapter{
		descriptor: contracts.AdapterDescriptor{
			Name: "typescript", Version: "wrapper-6.0.2/compiler-6.0.3",
			ConfigurationHash: configuration,
			CapabilityHash:    hashText("declarations;imports;references;calls;extends;implements;tests;routes;schemas;diagnostics"),
		},
		expectedToolchainVersion: "6.0.3",
		setupErr:                 setupErr,
		toolRoot:                 toolRoot, command: "node", args: []string{"adapters/typescript-indexer.mjs"},
		languages: map[string]struct{}{"typescript": {}, "javascript": {}},
	}
}

func NewPythonAdapter(toolRoot, version string) *ProcessAdapter {
	configuration, setupErr := processAdapterConfigurationHash(
		toolRoot, "python:stdlib-ast;type-comments;no-imports;root-guard;syntax="+version,
		"adapters/python-indexer.py",
	)
	return &ProcessAdapter{
		descriptor: contracts.AdapterDescriptor{
			Name: "python", Version: version,
			ConfigurationHash: configuration,
			CapabilityHash:    hashText("declarations;imports;references;calls;extends;tests;routes;schemas;diagnostics"),
		},
		expectedToolchainVersion: version,
		setupErr:                 setupErr,
		toolRoot:                 toolRoot, command: "python3", args: []string{"-I", "adapters/python-indexer.py"},
		languages: map[string]struct{}{"python": {}},
	}
}

func (a *ProcessAdapter) Descriptor() contracts.AdapterDescriptor { return a.descriptor }
func (a *ProcessAdapter) Languages() []string {
	result := make([]string, 0, len(a.languages))
	for language := range a.languages {
		result = append(result, language)
	}
	sort.Strings(result)
	return result
}

func (a *ProcessAdapter) Extract(ctx context.Context, root string, documents []SourceDocument) (RawAdapterResult, error) {
	if a.setupErr != nil {
		return RawAdapterResult{}, a.setupErr
	}
	files := make([]string, 0)
	for _, document := range documents {
		if _, accepted := a.languages[document.Language]; accepted {
			files = append(files, document.Path)
		}
	}
	result := RawAdapterResult{Descriptor: a.descriptor, Symbols: []RawSymbol{}, Relations: []RawRelation{}, Diagnostics: []RawDiagnostic{}}
	if len(files) == 0 {
		return result, nil
	}
	request, err := json.Marshal(struct {
		Root             string   `json:"root"`
		Files            []string `json:"files"`
		ToolchainVersion string   `json:"toolchain_version"`
	}{Root: root, Files: files, ToolchainVersion: a.expectedToolchainVersion})
	if err != nil {
		return result, err
	}
	if len(request) > MaximumAdapterInputBytes {
		return result, fmt.Errorf("%s adapter request exceeded input limit", a.descriptor.Name)
	}
	toolRoot, err := filepath.Abs(a.toolRoot)
	if err != nil {
		return result, fmt.Errorf("resolve adapter tool root: %w", err)
	}
	adapterContext, cancel := context.WithTimeout(ctx, MaximumAdapterRuntime)
	defer cancel()
	command := exec.CommandContext(adapterContext, a.command, a.args...)
	command.Dir = toolRoot
	command.Env = restrictedProcessEnvironment(toolRoot)
	command.Stdin = bytes.NewReader(request)
	stdout := &boundedBuffer{limit: MaximumAdapterOutputBytes}
	stderr := &boundedBuffer{limit: 64 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		if adapterContext.Err() != nil {
			return result, fmt.Errorf("%s adapter deadline: %w", a.descriptor.Name, adapterContext.Err())
		}
		return result, fmt.Errorf("%s adapter failed: %w: %s", a.descriptor.Name, err, strings.TrimSpace(stderr.String()))
	}
	if stdout.exceeded {
		return result, fmt.Errorf("%s adapter exceeded output limit", a.descriptor.Name)
	}
	var payload struct {
		ToolchainVersion string          `json:"toolchain_version"`
		Symbols          []RawSymbol     `json:"symbols"`
		Relations        []RawRelation   `json:"relations"`
		Diagnostics      []RawDiagnostic `json:"diagnostics"`
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return result, fmt.Errorf("decode %s adapter output: %w", a.descriptor.Name, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return result, err
	}
	if payload.Symbols == nil || payload.Relations == nil || payload.Diagnostics == nil {
		return result, fmt.Errorf("%s adapter returned null collections", a.descriptor.Name)
	}
	if payload.ToolchainVersion != a.expectedToolchainVersion {
		return result, fmt.Errorf(
			"%s adapter toolchain=%q, want %q",
			a.descriptor.Name, payload.ToolchainVersion, a.expectedToolchainVersion,
		)
	}
	result.Symbols, result.Relations, result.Diagnostics = payload.Symbols, payload.Relations, payload.Diagnostics
	return result, nil
}

func processAdapterConfigurationHash(toolRoot, configuration string, paths ...string) (string, error) {
	root, err := filepath.Abs(toolRoot)
	if err != nil {
		return "", fmt.Errorf("resolve adapter tool root: %w", err)
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("forja-process-adapter-configuration-v1\x00" + configuration))
	for _, name := range paths {
		canonical, err := contracts.NormalizeRepositoryPath(name)
		if err != nil {
			return "", fmt.Errorf("adapter source path: %w", err)
		}
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(canonical)))
		if err != nil {
			return "", fmt.Errorf("read adapter source %s: %w", canonical, err)
		}
		if len(body) > 16<<20 {
			return "", fmt.Errorf("adapter source %s exceeds limit", canonical)
		}
		fileDigest := sha256.Sum256(body)
		_, _ = digest.Write([]byte("\x00" + canonical + "\x00" + hex.EncodeToString(fileDigest[:])))
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.exceeded = true
		return original, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		b.exceeded = true
	}
	_, _ = b.Buffer.Write(value)
	return original, nil
}

func restrictedProcessEnvironment(toolRoot string) []string {
	environment := []string{"LANG=C.UTF-8", "LC_ALL=C.UTF-8", "PYTHONNOUSERSITE=1", "PYTHONDONTWRITEBYTECODE=1", "NODE_NO_WARNINGS=1"}
	for _, key := range []string{"PATH", "TMPDIR", "SYSTEMROOT"} {
		if value := os.Getenv(key); value != "" {
			environment = append(environment, key+"="+value)
		}
	}
	return append(environment, "NODE_PATH="+filepath.Join(toolRoot, "node_modules"))
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("adapter returned multiple JSON documents")
		}
		return fmt.Errorf("decode adapter trailer: %w", err)
	}
	return nil
}
