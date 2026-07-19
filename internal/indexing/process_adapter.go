package indexing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

const MaximumAdapterOutputBytes = 64 << 20

type ProcessAdapter struct {
	descriptor contracts.AdapterDescriptor
	toolRoot   string
	command    string
	args       []string
	languages  map[string]struct{}
}

func NewTypeScriptAdapter(toolRoot string) *ProcessAdapter {
	return &ProcessAdapter{
		descriptor: contracts.AdapterDescriptor{
			Name: "typescript", Version: "6.0.2-api",
			ConfigurationHash: hashText("ts:ES2024;module=ESNext;resolution=Bundler;allowJs;checkJs;noEmit;root-guard"),
			CapabilityHash:    hashText("declarations;imports;references;calls;extends;implements;tests;routes;schemas;diagnostics"),
		},
		toolRoot: toolRoot, command: "node", args: []string{"adapters/typescript-indexer.mjs"},
		languages: map[string]struct{}{"typescript": {}, "javascript": {}},
	}
}

func NewPythonAdapter(toolRoot, version string) *ProcessAdapter {
	return &ProcessAdapter{
		descriptor: contracts.AdapterDescriptor{
			Name: "python", Version: version,
			ConfigurationHash: hashText("python:stdlib-ast;type-comments;no-imports;root-guard"),
			CapabilityHash:    hashText("declarations;imports;references;calls;extends;tests;routes;schemas;diagnostics"),
		},
		toolRoot: toolRoot, command: "python3", args: []string{"-I", "adapters/python-indexer.py"},
		languages: map[string]struct{}{"python": {}},
	}
}

func (a *ProcessAdapter) Descriptor() contracts.AdapterDescriptor { return a.descriptor }

func (a *ProcessAdapter) Extract(ctx context.Context, root string, documents []SourceDocument) (RawAdapterResult, error) {
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
		Root  string   `json:"root"`
		Files []string `json:"files"`
	}{Root: root, Files: files})
	if err != nil {
		return result, err
	}
	toolRoot, err := filepath.Abs(a.toolRoot)
	if err != nil {
		return result, fmt.Errorf("resolve adapter tool root: %w", err)
	}
	command := exec.CommandContext(ctx, a.command, a.args...)
	command.Dir = toolRoot
	command.Env = restrictedProcessEnvironment(toolRoot)
	command.Stdin = bytes.NewReader(request)
	stdout := &boundedBuffer{limit: MaximumAdapterOutputBytes}
	stderr := &boundedBuffer{limit: 64 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		return result, fmt.Errorf("%s adapter failed: %w: %s", a.descriptor.Name, err, strings.TrimSpace(stderr.String()))
	}
	if stdout.exceeded {
		return result, fmt.Errorf("%s adapter exceeded output limit", a.descriptor.Name)
	}
	var payload struct {
		Symbols     []RawSymbol     `json:"symbols"`
		Relations   []RawRelation   `json:"relations"`
		Diagnostics []RawDiagnostic `json:"diagnostics"`
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
	result.Symbols, result.Relations, result.Diagnostics = payload.Symbols, payload.Relations, payload.Diagnostics
	return result, nil
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
