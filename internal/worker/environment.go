package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var inheritedEnvironment = map[string]struct{}{
	"LANG": {}, "LC_ALL": {}, "PATH": {}, "SSL_CERT_DIR": {},
	"SSL_CERT_FILE": {}, "TMPDIR": {},
}

// SanitizedEnvironment creates a minimal environment for an untrusted worker.
// Model authentication is available only through the deployment-owned Codex
// home; control-plane and Git credentials are never inherited.
func SanitizedEnvironment(source []string, isolatedHome string) ([]string, error) {
	if !filepath.IsAbs(isolatedHome) {
		return nil, fmt.Errorf("isolated worker home must be absolute")
	}
	sourceValues := parseEnvironment(source)
	codexHome := strings.TrimSpace(sourceValues["CODEX_HOME"])
	if codexHome == "" {
		home := strings.TrimSpace(sourceValues["HOME"])
		if home != "" {
			codexHome = filepath.Join(home, ".codex")
		}
	}
	if codexHome != "" && !filepath.IsAbs(codexHome) {
		return nil, fmt.Errorf("CODEX_HOME must be absolute")
	}

	result := make(map[string]string)
	for key := range inheritedEnvironment {
		if value, ok := sourceValues[key]; ok && value != "" {
			result[key] = value
		}
	}
	result["HOME"] = isolatedHome
	result["XDG_CACHE_HOME"] = filepath.Join(isolatedHome, ".cache")
	result["XDG_CONFIG_HOME"] = filepath.Join(isolatedHome, ".config")
	result["XDG_DATA_HOME"] = filepath.Join(isolatedHome, ".local", "share")
	result["GIT_CONFIG_GLOBAL"] = "/dev/null"
	result["GIT_CONFIG_NOSYSTEM"] = "1"
	result["GIT_TERMINAL_PROMPT"] = "0"
	result["GCM_INTERACTIVE"] = "Never"
	result["SSH_ASKPASS"] = "/bin/false"
	if codexHome != "" {
		result["CODEX_HOME"] = codexHome
	}

	keys := make([]string, 0, len(result))
	for key := range result {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key+"="+result[key])
	}
	return values, nil
}

func parseEnvironment(source []string) map[string]string {
	result := make(map[string]string, len(source))
	for _, entry := range source {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key != "" {
			result[key] = value
		}
	}
	return result
}

func prepareWorkerHome(path string) error {
	for _, directory := range []string{
		path,
		filepath.Join(path, ".cache"),
		filepath.Join(path, ".config"),
		filepath.Join(path, ".local", "share"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create isolated worker home: %w", err)
		}
	}
	return nil
}
