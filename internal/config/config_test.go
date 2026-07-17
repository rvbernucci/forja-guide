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
		"shutdown_timeout": "7s",
		"database_url": "postgres://file",
		"database_max_connections": 5,
		"database_auto_migrate": false
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{
		envListen:          "127.0.0.1:7001",
		envEnvironment:     "environment",
		envLogLevel:        "error",
		envShutdownTimeout: "8s",
		envDatabaseURL:     "postgres://environment",
		envDatabaseMaxConn: "6",
		envDatabaseMigrate: "true",
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
		"--database-url", "postgres://flag",
		"--database-max-connections", "7",
		"--database-auto-migrate=false",
	}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:7002" ||
		cfg.Environment != "flag" ||
		cfg.LogLevel != "debug" ||
		cfg.ShutdownTimeout != 9*time.Second ||
		cfg.DatabaseURL != "postgres://flag" ||
		cfg.DatabaseMaxConn != 7 ||
		cfg.DatabaseMigrate {
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

func TestLoadUsesLastConfigFlagConsistently(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	first := filepath.Join(directory, "first.json")
	second := filepath.Join(directory, "second.json")
	if err := os.WriteFile(
		first,
		[]byte(`{"environment":"first"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		second,
		[]byte(`{"environment":"second"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(
		[]string{"--config", first, "--config=" + second},
		func(string) (string, bool) { return "", false },
	)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfigFile != second || cfg.Environment != "second" {
		t.Fatalf("unexpected config selection: %#v", cfg)
	}
}

func TestValidateRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()
	valid := Config{
		Listen:          "127.0.0.1:8080",
		Environment:     "test",
		LogLevel:        "info",
		ShutdownTimeout: time.Second,
		DatabaseMaxConn: 4,
	}
	cases := []Config{
		withConfig(valid, func(cfg *Config) { cfg.Listen = ":8080" }),
		withConfig(valid, func(cfg *Config) { cfg.Listen = "127.0.0.1:99999" }),
		withConfig(valid, func(cfg *Config) { cfg.Environment = "" }),
		withConfig(valid, func(cfg *Config) { cfg.LogLevel = "trace" }),
		withConfig(valid, func(cfg *Config) { cfg.ShutdownTimeout = 0 }),
		withConfig(valid, func(cfg *Config) { cfg.DatabaseMaxConn = 0 }),
		withConfig(valid, func(cfg *Config) { cfg.DatabaseMaxConn = 101 }),
	}
	for _, cfg := range cases {
		if err := Validate(cfg); err == nil {
			t.Fatalf("expected invalid config to fail: %#v", cfg)
		}
	}
}

func withConfig(base Config, mutate func(*Config)) Config {
	mutate(&base)
	return base
}
