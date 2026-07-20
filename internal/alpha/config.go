package alpha

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

const defaultAddress = "127.0.0.1:8787"

type Config struct {
	Address          string
	ModelBaseURL     *url.URL
	EmbeddingBaseURL *url.URL
	Accelerator      string
	SoftwareStack    string
}

func LoadConfig() (Config, error) {
	return loadConfig(os.Getenv)
}

func loadConfig(getenv func(string) string) (Config, error) {
	config := Config{
		Address:       strings.TrimSpace(getenv("FORJA_ALPHA_ADDRESS")),
		Accelerator:   valueOrDefault(getenv("FORJA_ALPHA_ACCELERATOR"), "Not detected"),
		SoftwareStack: valueOrDefault(getenv("FORJA_ALPHA_SOFTWARE_STACK"), "Not detected"),
	}
	if config.Address == "" {
		config.Address = defaultAddress
	}
	if _, _, err := net.SplitHostPort(config.Address); err != nil {
		return Config{}, fmt.Errorf("FORJA_ALPHA_ADDRESS: %w", err)
	}

	var err error
	config.ModelBaseURL, err = localEndpoint(getenv("FORJA_ALPHA_MODEL_BASE_URL"))
	if err != nil {
		return Config{}, fmt.Errorf("FORJA_ALPHA_MODEL_BASE_URL: %w", err)
	}
	config.EmbeddingBaseURL, err = localEndpoint(getenv("FORJA_ALPHA_EMBEDDING_BASE_URL"))
	if err != nil {
		return Config{}, fmt.Errorf("FORJA_ALPHA_EMBEDDING_BASE_URL: %w", err)
	}
	return config, nil
}

func localEndpoint(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("scheme must be http or https")
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return parsed, nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("endpoint must resolve through an explicit loopback address")
	}
	return parsed, nil
}

func valueOrDefault(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}
