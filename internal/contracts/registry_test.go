package contracts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryValidatesContractFixtures(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile(filepath.Join("testdata", "run.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := DecodeStrict[Run](registry, "run.schema.json", valid)
	if err != nil {
		t.Fatal(err)
	}
	if run.RunID != "run_00010203-0405-4607-8809-0a0b0c0d0e0f" {
		t.Fatalf("unexpected run ID: %s", run.RunID)
	}

	invalid, err := os.ReadFile(filepath.Join("testdata", "run.invalid.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeStrict[Run](registry, "run.schema.json", invalid); err == nil {
		t.Fatal("expected invalid fixture to fail")
	}
}

func TestRegistryRejectsUnknownSchemaAndTrailingDocument(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateJSON("missing.schema.json", []byte(`{}`)); err == nil {
		t.Fatal("expected unknown schema to fail")
	}
	data := []byte(`{
		"run_id": "run_00010203-0405-4607-8809-0a0b0c0d0e0f",
		"schema_version": "1.0",
		"objective": "Synthetic run",
		"state": "draft",
		"version": 1,
		"created_at": "2026-07-16T12:00:00Z",
		"updated_at": "2026-07-16T12:00:00Z"
	} {}`)
	if _, err := DecodeStrict[Run](registry, "run.schema.json", data); err == nil {
		t.Fatal("expected trailing document to fail")
	}
}

func FuzzRunContract(f *testing.F) {
	valid, err := os.ReadFile(filepath.Join("testdata", "run.valid.json"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte(`{}`))
	registry, err := NewRegistry()
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeStrict[Run](registry, "run.schema.json", data)
	})
}
