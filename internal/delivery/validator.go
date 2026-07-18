package delivery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	publicschemas "github.com/rvbernucci/forja-guide/schemas"
)

const (
	minimumValidatorTimeout     = 100 * time.Millisecond
	maximumValidatorTimeout     = time.Hour
	minimumValidatorOutputBytes = 1024
	maximumValidatorOutputBytes = 16 << 20
	maximumRegistryOutputBytes  = 128 << 20
	validatorStopGrace          = 250 * time.Millisecond
	validatorKillGrace          = 2 * time.Second
)

var validatorIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,119}$`)

var reservedValidationCheckIDs = map[string]struct{}{
	"clean-checkout":        {},
	"filesystem-safety":     {},
	"generated-file-policy": {},
	"mechanical-preflight":  {},
	"schema-validation":     {},
	"scope-boundary":        {},
	"secret-scan":           {},
}

// ValidatorDefinition is one trusted, shell-free runtime registry entry.
type ValidatorDefinition struct {
	ID             string
	Argv           []string
	Timeout        time.Duration
	MaxOutputBytes int
}

// SchemaBinding associates one canonical repository path with an embedded
// Forja JSON Schema. Requests cannot create or widen these bindings.
type SchemaBinding struct {
	Path       string
	SchemaName string
}

// ValidatorRegistry is immutable after construction and pins resolved
// executables, budgets, schema bindings, and a sanitized source environment.
type ValidatorRegistry struct {
	definitions map[string]registeredValidator
	bindings    map[string]string
	environ     []string
}

type registeredValidator struct {
	id             string
	argv           []string
	timeout        time.Duration
	maxOutputBytes int
	commandDigest  string
	executableHash string
}

// NewValidatorRegistry validates and pins every trusted registry entry.
func NewValidatorRegistry(
	definitions []ValidatorDefinition,
	bindings []SchemaBinding,
	environ []string,
) (*ValidatorRegistry, error) {
	if environ == nil {
		environ = os.Environ()
	}
	registry := &ValidatorRegistry{
		definitions: make(map[string]registeredValidator, len(definitions)),
		bindings:    make(map[string]string, len(bindings)),
		environ:     append([]string(nil), environ...),
	}
	totalOutputBudget := 0
	for _, definition := range definitions {
		if !validatorIDPattern.MatchString(definition.ID) {
			return nil, fmt.Errorf("validator ID %q is not canonical", definition.ID)
		}
		if _, reserved := reservedValidationCheckIDs[definition.ID]; reserved {
			return nil, fmt.Errorf("validator ID %q is reserved", definition.ID)
		}
		if _, duplicate := registry.definitions[definition.ID]; duplicate {
			return nil, fmt.Errorf("validator ID %q is duplicated", definition.ID)
		}
		if definition.Timeout < minimumValidatorTimeout || definition.Timeout > maximumValidatorTimeout {
			return nil, fmt.Errorf("validator %q timeout is outside policy", definition.ID)
		}
		if definition.MaxOutputBytes < minimumValidatorOutputBytes ||
			definition.MaxOutputBytes > maximumValidatorOutputBytes {
			return nil, fmt.Errorf("validator %q output budget is outside policy", definition.ID)
		}
		if totalOutputBudget > maximumRegistryOutputBytes-definition.MaxOutputBytes {
			return nil, fmt.Errorf("combined validator output budget exceeds policy")
		}
		totalOutputBudget += definition.MaxOutputBytes
		argv, err := pinValidatorArgv(definition.Argv)
		if err != nil {
			return nil, fmt.Errorf("validator %q: %w", definition.ID, err)
		}
		executableHash, err := hashRegularFile(argv[0])
		if err != nil {
			return nil, fmt.Errorf("validator %q executable: %w", definition.ID, err)
		}
		environmentDocument, err := json.Marshal(validatorEnvironment(environ, "/__forja_validator_home__"))
		if err != nil {
			return nil, fmt.Errorf("encode validator %q environment: %w", definition.ID, err)
		}
		environmentHash := sha256.Sum256(environmentDocument)
		digestDocument := struct {
			ID                string   `json:"id"`
			Argv              []string `json:"argv"`
			ExecutableSHA256  string   `json:"executable_sha256"`
			EnvironmentSHA256 string   `json:"environment_sha256"`
			TimeoutMS         int64    `json:"timeout_ms"`
			MaxOutputBytes    int      `json:"max_output_bytes"`
		}{
			definition.ID, argv, executableHash, fmt.Sprintf("%x", environmentHash),
			definition.Timeout.Milliseconds(), definition.MaxOutputBytes,
		}
		canonical, err := json.Marshal(digestDocument)
		if err != nil {
			return nil, fmt.Errorf("encode validator %q identity: %w", definition.ID, err)
		}
		digest := sha256.Sum256(canonical)
		registry.definitions[definition.ID] = registeredValidator{
			id: definition.ID, argv: argv, timeout: definition.Timeout,
			maxOutputBytes: definition.MaxOutputBytes,
			commandDigest:  fmt.Sprintf("%x", digest),
			executableHash: executableHash,
		}
	}
	for _, binding := range bindings {
		if err := validateRepositoryRelativePath(binding.Path); err != nil {
			return nil, fmt.Errorf("schema binding path: %w", err)
		}
		if filepath.Base(binding.SchemaName) != binding.SchemaName ||
			!strings.HasSuffix(binding.SchemaName, ".schema.json") {
			return nil, fmt.Errorf("schema binding %q has a noncanonical schema name", binding.Path)
		}
		if _, err := publicschemas.FS.ReadFile(binding.SchemaName); err != nil {
			return nil, fmt.Errorf("schema binding %q references an unknown schema: %w", binding.Path, err)
		}
		if _, duplicate := registry.bindings[binding.Path]; duplicate {
			return nil, fmt.Errorf("schema binding path %q is duplicated", binding.Path)
		}
		registry.bindings[binding.Path] = binding.SchemaName
	}
	return registry, nil
}

func (r *ValidatorRegistry) resolve(ids []string) ([]registeredValidator, error) {
	if r == nil {
		return nil, fmt.Errorf("validator registry is required")
	}
	if !byteSortedUnique(ids) {
		return nil, fmt.Errorf("requested validator IDs must be unique and byte-sorted")
	}
	resolved := make([]registeredValidator, 0, len(ids))
	for _, id := range ids {
		definition, ok := r.definitions[id]
		if !ok {
			return nil, fmt.Errorf("approved validator %q is not registered", id)
		}
		definition.argv = append([]string(nil), definition.argv...)
		resolved = append(resolved, definition)
	}
	return resolved, nil
}

func pinValidatorArgv(argv []string) ([]string, error) {
	if len(argv) == 0 || len(argv) > 128 {
		return nil, fmt.Errorf("argv must contain between 1 and 128 values")
	}
	values := append([]string(nil), argv...)
	for _, value := range values {
		if value == "" || strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("argv contains an empty value or NUL byte")
		}
	}
	executable := values[0]
	if !filepath.IsAbs(executable) {
		resolved, err := exec.LookPath(executable)
		if err != nil {
			return nil, fmt.Errorf("resolve executable: %w", err)
		}
		executable, err = filepath.Abs(resolved)
		if err != nil {
			return nil, fmt.Errorf("make executable absolute: %w", err)
		}
	}
	if filepath.Clean(executable) != executable {
		return nil, fmt.Errorf("executable path is not canonical")
	}
	physical, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, fmt.Errorf("resolve executable symlinks: %w", err)
	}
	info, err := os.Stat(physical)
	if err != nil {
		return nil, fmt.Errorf("stat executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("executable is not an executable regular file")
	}
	values[0] = physical
	return values, nil
}

type validationExecution struct {
	check  contracts.ValidationCheck
	stdout []byte
	stderr []byte
	lane   string
}

func runRegisteredValidator(
	ctx context.Context,
	definition registeredValidator,
	directory string,
	home string,
	environ []string,
	now func() time.Time,
	lane string,
) validationExecution {
	startedAt := now().UTC()
	command := exec.Command(definition.argv[0], definition.argv[1:]...)
	command.Dir = directory
	command.Env = validatorEnvironment(environ, home)
	configureValidatorProcess(command)
	overflow := make(chan struct{})
	var overflowOnce sync.Once
	quota := &commandOutputQuota{
		remaining: definition.maxOutputBytes, overflow: overflow, once: &overflowOnce,
	}
	stdout := &commandOutputBuffer{quota: quota}
	stderr := &commandOutputBuffer{quota: quota}
	command.Stdout = stdout
	command.Stderr = stderr
	status := "passed"
	detail := ""
	var exitCode *int
	actualExecutableHash, executableErr := hashRegularFile(definition.argv[0])
	if executableErr != nil || actualExecutableHash != definition.executableHash {
		status = "failed"
		detail = "validator executable identity changed after registration"
	} else if err := command.Start(); err != nil {
		status = "failed"
		detail = "validator process could not start"
	} else {
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		timer := time.NewTimer(definition.timeout)
		defer timer.Stop()
		var runErr error
		select {
		case runErr = <-done:
		case <-overflow:
			status = "failed"
			detail = "validator output exceeded its configured budget"
			runErr = stopValidatorProcess(command, done)
		case <-timer.C:
			status = "failed"
			detail = "validator exceeded its configured timeout"
			runErr = stopValidatorProcess(command, done)
		case <-ctx.Done():
			status = "failed"
			detail = "validation context was cancelled"
			runErr = stopValidatorProcess(command, done)
		}
		if command.ProcessState != nil {
			value := command.ProcessState.ExitCode()
			exitCode = &value
		}
		if runErr != nil && status == "passed" {
			status = "failed"
			detail = "validator exited unsuccessfully"
		}
	}
	if actualHash, err := hashRegularFile(definition.argv[0]); err != nil || actualHash != definition.executableHash {
		status = "failed"
		detail = "validator executable identity changed during execution"
	}
	finishedAt := now().UTC()
	if finishedAt.Before(startedAt) {
		finishedAt = startedAt
	}
	stdoutBytes := stdout.Bytes()
	stderrBytes := stderr.Bytes()
	stdoutDigest := sha256.Sum256(stdoutBytes)
	stderrDigest := sha256.Sum256(stderrBytes)
	commandDigest := definition.commandDigest
	var detailPointer *string
	if detail != "" {
		detailPointer = &detail
	}
	return validationExecution{
		check: contracts.ValidationCheck{
			CheckID: definition.id, Kind: "configured", Status: status,
			StartedAt: startedAt, FinishedAt: finishedAt,
			DurationMS: finishedAt.Sub(startedAt).Milliseconds(), ExitCode: exitCode,
			CommandDigest: &commandDigest,
			StdoutSHA256:  fmt.Sprintf("%x", stdoutDigest),
			StderrSHA256:  fmt.Sprintf("%x", stderrDigest),
			Detail:        detailPointer,
		},
		stdout: stdoutBytes, stderr: stderrBytes, lane: lane,
	}
}

func stopValidatorProcess(command *exec.Cmd, done <-chan error) error {
	_ = signalValidatorProcessTree(command.Process, syscall.SIGTERM)
	timer := time.NewTimer(validatorStopGrace)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		_ = signalValidatorProcessTree(command.Process, syscall.SIGKILL)
		killTimer := time.NewTimer(validatorKillGrace)
		defer killTimer.Stop()
		select {
		case err := <-done:
			return err
		case <-killTimer.C:
			return fmt.Errorf("validator process tree did not terminate after SIGKILL")
		}
	}
}

type commandOutputBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	quota  *commandOutputQuota
}

func (b *commandOutputBuffer) Write(value []byte) (int, error) {
	original := len(value)
	accepted := b.quota.take(original)
	b.mu.Lock()
	defer b.mu.Unlock()
	if accepted > 0 {
		_, _ = b.buffer.Write(value[:accepted])
	}
	return original, nil
}

func (b *commandOutputBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buffer.Bytes())
}

type commandOutputQuota struct {
	mu        sync.Mutex
	remaining int
	overflow  chan struct{}
	once      *sync.Once
}

func (q *commandOutputQuota) take(requested int) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	accepted := requested
	if accepted > q.remaining {
		accepted = q.remaining
		q.once.Do(func() { close(q.overflow) })
	}
	q.remaining -= accepted
	return accepted
}

func validatorEnvironment(source []string, home string) []string {
	allowed := map[string]struct{}{
		"LANG": {}, "LC_ALL": {}, "PATH": {}, "SSL_CERT_DIR": {},
		"SSL_CERT_FILE": {}, "TMPDIR": {},
	}
	values := map[string]string{
		"HOME":                home,
		"GIT_CONFIG_GLOBAL":   "/dev/null",
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_TERMINAL_PROMPT": "0",
		"LC_ALL":              "C",
	}
	for _, entry := range source {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, accepted := allowed[key]; accepted && value != "" {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func validateRepositoryRelativePath(value string) error {
	if value == "" || strings.ContainsAny(value, "\\\x00\r\n") ||
		filepath.IsAbs(value) || filepath.ToSlash(filepath.Clean(value)) != value ||
		value == ".." || strings.HasPrefix(value, "../") {
		return fmt.Errorf("path %q is not canonical and repository-relative", value)
	}
	return nil
}

func hashRegularFile(name string) (string, error) {
	info, err := os.Stat(name)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("path is not a regular file")
	}
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}
