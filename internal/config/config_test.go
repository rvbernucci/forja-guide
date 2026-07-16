package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadPrecedence(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "forja.json")
	data := []byte(`{
		"listen": "127.0.0.1:7000",
		"environment": "file",
		"log_level": "warn",
		"shutdown_timeout": "7s"
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{
		envListen:          "127.0.0.1:7001",
		envEnvironment:     "environment",
		envLogLevel:        "error",
		envShutdownTimeout: "8s",
	}
	lookup := func(key string) (string, bool) {
		value, ok := environment[key]
		return value, ok
	}

	cfg, err := Load([]string{
		"--config", path,
		"--listen", "127.0.0.1:7002",
		"--environment", "flag",
		"--log-level", "debug",
		"--shutdown-timeout", "9s",
	}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:7002" ||
		cfg.Environment != "flag" ||
		cfg.LogLevel != "debug" ||
		cfg.ShutdownTimeout != 9*time.Second {
		t.Fatalf("unexpected resolved config: %#v", cfg)
	}
}

func TestLoadRejectsUnknownFileField(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "forja.json")
	if err := os.WriteFile(path, []byte(`{"unknown": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load([]string{"--config", path}, nil); err == nil {
		t.Fatal("expected unknown field to fail closed")
	}
}

func TestLoadRejectsMultipleDocuments(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "forja.json")
	if err := os.WriteFile(path, []byte(`{} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load([]string{"--config", path}, nil); err == nil {
		t.Fatal("expected multiple documents to fail closed")
	}
}

func TestValidateRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()
	cases := []Config{
		{Listen: ":8080", Environment: "test", LogLevel: "info", ShutdownTimeout: time.Second},
		{Listen: "127.0.0.1:99999", Environment: "test", LogLevel: "info", ShutdownTimeout: time.Second},
		{Listen: "127.0.0.1:8080", Environment: "", LogLevel: "info", ShutdownTimeout: time.Second},
		{Listen: "127.0.0.1:8080", Environment: "test", LogLevel: "trace", ShutdownTimeout: time.Second},
		{Listen: "127.0.0.1:8080", Environment: "test", LogLevel: "info", ShutdownTimeout: 0},
	}
	for _, cfg := range cases {
		if err := Validate(cfg); err == nil {
			t.Fatalf("expected invalid config to fail: %#v", cfg)
		}
	}
}
