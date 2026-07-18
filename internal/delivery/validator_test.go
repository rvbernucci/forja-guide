package delivery

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestValidatorRegistryPinsDefinitionsAndRejectsAuthorityExpansion(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	argv := []string{executable, "-test.run=TestValidatorHelperProcess", "--", "pass"}
	registry, err := NewValidatorRegistry([]ValidatorDefinition{{
		ID: "unit-tests", Argv: argv, Timeout: time.Second, MaxOutputBytes: 1024,
	}}, []SchemaBinding{{Path: "config/value.json", SchemaName: "run.schema.json"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("close validator registry: %v", err)
		}
	})
	argv[3] = "fail"
	resolved, err := registry.resolve([]string{"unit-tests"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved[0].argv[3] != "pass" || resolved[0].commandDigest == "" {
		t.Fatalf("registry definition was mutable: %#v", resolved[0])
	}
	if _, err := registry.resolve([]string{"missing"}); err == nil {
		t.Fatal("unregistered validator was accepted")
	}
	if _, err := registry.resolve([]string{"unit-tests", "unit-tests"}); err == nil {
		t.Fatal("duplicate requested validators were accepted")
	}
}

func TestValidatorRegistryRejectsReservedShellAndInvalidBudgets(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]ValidatorDefinition{
		"reserved ID": {
			ID: "secret-scan", Argv: []string{executable}, Timeout: time.Second, MaxOutputBytes: 1024,
		},
		"shell string": {
			ID: "test", Argv: []string{"echo value | cat"}, Timeout: time.Second, MaxOutputBytes: 1024,
		},
		"short timeout": {
			ID: "test", Argv: []string{executable}, Timeout: time.Millisecond, MaxOutputBytes: 1024,
		},
		"small output budget": {
			ID: "test", Argv: []string{executable}, Timeout: time.Second, MaxOutputBytes: 100,
		},
	}
	for name, definition := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewValidatorRegistry([]ValidatorDefinition{definition}, nil, nil); err == nil {
				t.Fatal("invalid validator definition was accepted")
			}
		})
	}
}

func TestValidatorRegistryRejectsWindowsAndAlternateStreamPaths(t *testing.T) {
	for _, value := range []string{"C:relative.json", "config/value.json:stream"} {
		if _, err := NewValidatorRegistry(nil, []SchemaBinding{{
			Path: value, SchemaName: "run.schema.json",
		}}, nil); err == nil {
			t.Fatalf("platform-specific path %q was accepted", value)
		}
	}
}

func TestRegisteredValidatorUsesPrivateCopyAfterSourceMutation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable script fixture requires a Unix host")
	}
	executable := filepath.Join(t.TempDir(), "validator")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	registry, err := NewValidatorRegistry([]ValidatorDefinition{{
		ID: "pinned", Argv: []string{executable}, Timeout: 5 * time.Second, MaxOutputBytes: 1024,
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("close validator registry: %v", err)
		}
	})
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	execution := runRegisteredValidator(
		t.Context(), registry.definitions["pinned"], t.TempDir(), t.TempDir(),
		registry.environ, time.Now, "independent",
	)
	if execution.check.Status != "passed" ||
		registry.definitions["pinned"].executablePath == executable {
		t.Fatalf("private executable result = %#v definition=%#v", execution.check, registry.definitions["pinned"])
	}
}

func TestRegisteredValidatorIsBoundedAndUsesSanitizedEnvironment(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	worktree := t.TempDir()
	definitions := []ValidatorDefinition{
		{ID: "combined-output", Argv: []string{executable, "-test.run=TestValidatorHelperProcess", "--", "both-output"}, Timeout: 5 * time.Second, MaxOutputBytes: 1024},
		{ID: "environment", Argv: []string{executable, "-test.run=TestValidatorHelperProcess", "--", "environment"}, Timeout: 5 * time.Second, MaxOutputBytes: 1024},
		{ID: "overflow", Argv: []string{executable, "-test.run=TestValidatorHelperProcess", "--", "overflow"}, Timeout: 5 * time.Second, MaxOutputBytes: 1024},
		{ID: "timeout", Argv: []string{executable, "-test.run=TestValidatorHelperProcess", "--", "sleep"}, Timeout: 100 * time.Millisecond, MaxOutputBytes: 1024},
	}
	registry, err := NewValidatorRegistry(definitions, nil, append(os.Environ(), "FORJA_CONTROL_TOKEN=secret"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("close validator registry: %v", err)
		}
	})
	now := func() time.Time { return time.Now().UTC() }

	environment := runRegisteredValidator(t.Context(), registry.definitions["environment"], worktree, home, registry.environ, now, "independent")
	if environment.check.Status != "passed" || !strings.HasPrefix(string(environment.stdout), home+"\n\n") ||
		strings.Contains(string(environment.stdout), "secret") {
		t.Fatalf("sanitized environment result = %#v stdout=%q", environment.check, environment.stdout)
	}
	overflow := runRegisteredValidator(t.Context(), registry.definitions["overflow"], worktree, home, registry.environ, now, "independent")
	if overflow.check.Status != "failed" || len(overflow.stdout) != 1024 ||
		overflow.check.Detail == nil || !strings.Contains(*overflow.check.Detail, "output") {
		t.Fatalf("overflow result = %#v bytes=%d", overflow.check, len(overflow.stdout))
	}
	combined := runRegisteredValidator(t.Context(), registry.definitions["combined-output"], worktree, home, registry.environ, now, "independent")
	if combined.check.Status != "failed" || len(combined.stdout)+len(combined.stderr) != 1024 {
		t.Fatalf(
			"combined output result = %#v stdout=%d stderr=%d",
			combined.check, len(combined.stdout), len(combined.stderr),
		)
	}
	started := time.Now()
	timeout := runRegisteredValidator(t.Context(), registry.definitions["timeout"], worktree, home, registry.environ, now, "independent")
	if timeout.check.Status != "failed" || timeout.check.Detail == nil ||
		!strings.Contains(*timeout.check.Detail, "timeout") || time.Since(started) > time.Second {
		t.Fatalf("timeout result = %#v elapsed=%s", timeout.check, time.Since(started))
	}
}

func TestValidatorHelperProcess(t *testing.T) {
	separator := slices.Index(os.Args, "--")
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	switch os.Args[separator+1] {
	case "pass":
		fmt.Println("passed")
	case "fail":
		os.Exit(7)
	case "environment":
		fmt.Printf("%s\n%s\n", os.Getenv("HOME"), os.Getenv("FORJA_CONTROL_TOKEN"))
	case "overflow":
		fmt.Print(strings.Repeat("x", 64<<10))
	case "both-output":
		fmt.Print(strings.Repeat("x", 800))
		fmt.Fprint(os.Stderr, strings.Repeat("y", 800))
	case "sleep":
		time.Sleep(10 * time.Second)
	case "mutate":
		if err := os.WriteFile("validator-mutated.txt", []byte("mutated\n"), 0o600); err != nil {
			os.Exit(10)
		}
	case "move-head":
		command := exec.Command("git", "checkout", "--quiet", "--detach", "HEAD^")
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Run(); err != nil {
			os.Exit(11)
		}
	default:
		os.Exit(9)
	}
}
