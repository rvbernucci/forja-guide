// Package config loads daemon configuration with deterministic precedence.
package config

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	envListen          = "FORJA_LISTEN"
	envEnvironment     = "FORJA_ENVIRONMENT"
	envLogLevel        = "FORJA_LOG_LEVEL"
	envShutdownTimeout = "FORJA_SHUTDOWN_TIMEOUT"
)

// LookupEnv matches os.LookupEnv and enables deterministic tests.
type LookupEnv func(string) (string, bool)

// Config is the resolved daemon configuration.
type Config struct {
	Listen          string
	Environment     string
	LogLevel        string
	ShutdownTimeout time.Duration
	ConfigFile      string
}

type fileConfig struct {
	Listen          *string `json:"listen"`
	Environment     *string `json:"environment"`
	LogLevel        *string `json:"log_level"`
	ShutdownTimeout *string `json:"shutdown_timeout"`
}

// Defaults returns safe local development defaults.
func Defaults() Config {
	return Config{
		Listen:          "127.0.0.1:8080",
		Environment:     "development",
		LogLevel:        "info",
		ShutdownTimeout: 5 * time.Second,
	}
}

// Load resolves defaults, file, environment, and flags in that order.
func Load(args []string, lookup LookupEnv) (Config, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}

	cfg := Defaults()
	configPath, err := discoverConfigPath(args)
	if err != nil {
		return Config{}, err
	}
	if configPath != "" {
		if err := applyFile(&cfg, configPath); err != nil {
			return Config{}, err
		}
		cfg.ConfigFile = configPath
	}
	if err := applyEnvironment(&cfg, lookup); err != nil {
		return Config{}, err
	}
	if err := applyFlags(&cfg, args); err != nil {
		return Config{}, err
	}
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func discoverConfigPath(args []string) (string, error) {
	var path string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if strings.HasPrefix(arg, "--config=") {
			path = strings.TrimPrefix(arg, "--config=")
			if path == "" {
				return "", fmt.Errorf("--config requires a path")
			}
			continue
		}
		if arg == "--config" {
			if index+1 >= len(args) {
				return "", fmt.Errorf("--config requires a path")
			}
			path = args[index+1]
			index++
		}
	}
	return path, nil
}

func applyFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file %s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var file fileConfig
	if err := decoder.Decode(&file); err != nil {
		return fmt.Errorf("decode config file %s: %w", path, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("decode config file %s: %w", path, err)
	}
	if file.Listen != nil {
		cfg.Listen = *file.Listen
	}
	if file.Environment != nil {
		cfg.Environment = *file.Environment
	}
	if file.LogLevel != nil {
		cfg.LogLevel = *file.LogLevel
	}
	if file.ShutdownTimeout != nil {
		duration, err := time.ParseDuration(*file.ShutdownTimeout)
		if err != nil {
			return fmt.Errorf("parse shutdown_timeout: %w", err)
		}
		cfg.ShutdownTimeout = duration
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON documents")
		}
		return err
	}
	return nil
}

func applyEnvironment(cfg *Config, lookup LookupEnv) error {
	if value, ok := lookup(envListen); ok && value != "" {
		cfg.Listen = value
	}
	if value, ok := lookup(envEnvironment); ok && value != "" {
		cfg.Environment = value
	}
	if value, ok := lookup(envLogLevel); ok && value != "" {
		cfg.LogLevel = value
	}
	if value, ok := lookup(envShutdownTimeout); ok && value != "" {
		duration, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("parse %s: %w", envShutdownTimeout, err)
		}
		cfg.ShutdownTimeout = duration
	}
	return nil
}

func applyFlags(cfg *Config, args []string) error {
	set := flag.NewFlagSet("forjad", flag.ContinueOnError)
	set.SetOutput(new(bytes.Buffer))
	set.StringVar(&cfg.ConfigFile, "config", cfg.ConfigFile, "JSON configuration file")
	set.StringVar(&cfg.Listen, "listen", cfg.Listen, "HTTP listen address")
	set.StringVar(&cfg.Environment, "environment", cfg.Environment, "runtime environment")
	set.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "debug, info, warn, or error")
	set.DurationVar(
		&cfg.ShutdownTimeout,
		"shutdown-timeout",
		cfg.ShutdownTimeout,
		"graceful shutdown timeout",
	)
	if err := set.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if set.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(set.Args(), " "))
	}
	return nil
}

// Validate checks resolved configuration before the daemon starts.
func Validate(cfg Config) error {
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", cfg.Listen, err)
	}
	if host == "" {
		return fmt.Errorf("listen host must be explicit")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return fmt.Errorf("invalid listen port %q", port)
	}
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid log level %q", cfg.LogLevel)
	}
	if cfg.Environment == "" {
		return fmt.Errorf("environment must not be empty")
	}
	if cfg.ShutdownTimeout <= 0 || cfg.ShutdownTimeout > time.Minute {
		return fmt.Errorf("shutdown timeout must be greater than zero and at most one minute")
	}
	return nil
}
