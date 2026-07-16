package contracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	publicschemas "github.com/rvbernucci/forja-guide/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Registry compiles and validates the canonical embedded JSON Schemas.
type Registry struct {
	compiled map[string]*jsonschema.Schema
}

// NewRegistry compiles every embedded schema and enables format assertions.
func NewRegistry() (*Registry, error) {
	entries, err := publicschemas.FS.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("read embedded schemas: %w", err)
	}

	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".schema.json") {
			continue
		}
		data, err := publicschemas.FS.ReadFile(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read schema %s: %w", entry.Name(), err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("decode schema %s: %w", entry.Name(), err)
		}
		if err := compiler.AddResource(entry.Name(), document); err != nil {
			return nil, fmt.Errorf("register schema %s: %w", entry.Name(), err)
		}
		names = append(names, entry.Name())
	}

	compiled := make(map[string]*jsonschema.Schema, len(names))
	for _, name := range names {
		schema, err := compiler.Compile(name)
		if err != nil {
			return nil, fmt.Errorf("compile schema %s: %w", name, err)
		}
		compiled[name] = schema
	}
	return &Registry{compiled: compiled}, nil
}

// ValidateJSON validates a JSON document against a canonical schema.
func (r *Registry) ValidateJSON(schemaName string, data []byte) error {
	name := filepath.Base(schemaName)
	schema, ok := r.compiled[name]
	if !ok {
		return fmt.Errorf("unknown schema %q", schemaName)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("decode instance for %s: %w", name, err)
	}
	if err := schema.Validate(instance); err != nil {
		return fmt.Errorf("validate instance against %s: %w", name, err)
	}
	return nil
}

// DecodeStrict validates JSON and decodes it without accepting unknown fields.
func DecodeStrict[T any](r *Registry, schemaName string, data []byte) (T, error) {
	var value T
	if err := r.ValidateJSON(schemaName, data); err != nil {
		return value, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("strict decode %s: %w", schemaName, err)
	}
	if err := ensureEOF(decoder); err != nil {
		return value, fmt.Errorf("strict decode %s: %w", schemaName, err)
	}
	return value, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON documents")
		}
		return err
	}
	return nil
}
