package delivery

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

func TestWorktreeLifecyclePreparesIdempotentlyAndPreservesRetiredCheckout(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	installPostCheckoutHook(t, repository, marker)
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)

	prepared, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, request.DeliveryID, request.AttemptID)
	if prepared.Path != wantPath || prepared.BaseCommit != base {
		t.Fatalf("prepared worktree = %#v", prepared)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("repository post-checkout hook executed")
	}
	for _, scope := range []string{"internal/generated", "evidence"} {
		info, err := os.Lstat(filepath.Join(prepared.Path, scope))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("prepared scope %q info=%v err=%v", scope, info, err)
		}
	}
	replayed, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if replayed != prepared {
		t.Fatalf("idempotent prepare changed identity: %#v != %#v", replayed, prepared)
	}
	quarantined, err := manager.Quarantine(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quarantined worktree source still exists: %v", err)
	}
	if _, err := os.Stat(quarantined.QuarantinePath); err != nil {
		t.Fatalf("quarantined clean worktree is not preserved: %v", err)
	}
	if _, err := manager.Prepare(t.Context(), request); !errors.Is(err, ErrWorktreeConflict) {
		t.Fatalf("retired attempt replay error = %v", err)
	}
	if _, err := os.Lstat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired attempt replay recreated its worktree: %v", err)
	}
}

func TestWorktreeLifecycleQuarantinesDirtyAttemptAndRetriesFromBase(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	prepared, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	changedPath := filepath.Join(prepared.Path, "internal/generated/result.txt")
	if err := os.WriteFile(changedPath, []byte("contaminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Inspect(t.Context(), request); !errors.Is(err, ErrWorktreeDirty) {
		t.Fatalf("dirty inspection error = %v", err)
	}
	// Recovery must not depend on the repository remaining safe for a fresh
	// checkout after the attempt was already contaminated.
	runGitTest(t, repository, "config", "filter.late.smudge", "cat")
	quarantined, err := manager.Quarantine(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !quarantined.GitRegistered {
		t.Fatal("registered worktree was not quarantined through Git")
	}
	runGitTest(t, repository, "config", "--unset", "filter.late.smudge")
	content, err := os.ReadFile(filepath.Join(quarantined.QuarantinePath, "internal/generated/result.txt"))
	if err != nil || string(content) != "contaminated\n" {
		t.Fatalf("quarantined content=%q err=%v", content, err)
	}

	retry := request
	retry.AttemptID = "attempt_22222222-2222-4222-8222-222222222222"
	retry.AttemptOrdinal = 2
	retried, err := manager.Prepare(t.Context(), retry)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Path == prepared.Path || retried.BaseCommit != base {
		t.Fatalf("retry worktree = %#v", retried)
	}
	if _, err := os.Lstat(filepath.Join(retried.Path, "internal/generated/result.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry inherited contaminated bytes: %v", err)
	}
	if _, err := manager.Quarantine(t.Context(), retry); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeLifecycleSerializesQuarantineAgainstPrepareReplay(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	prepared, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	locked := make(chan struct{})
	releaseQuarantine := make(chan struct{})
	var signal sync.Once
	manager.afterAttemptLockTest = func(operation string) {
		if operation == "quarantine" {
			signal.Do(func() { close(locked) })
			<-releaseQuarantine
		}
	}
	quarantineDone := make(chan error, 1)
	go func() {
		_, err := manager.Quarantine(t.Context(), request)
		quarantineDone <- err
	}()
	<-locked
	prepareDone := make(chan error, 1)
	go func() {
		_, err := manager.Prepare(t.Context(), request)
		prepareDone <- err
	}()
	select {
	case err := <-prepareDone:
		t.Fatalf("prepare bypassed the quarantine lifecycle lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseQuarantine)
	if err := <-quarantineDone; err != nil {
		t.Fatal(err)
	}
	if err := <-prepareDone; !errors.Is(err, ErrWorktreeConflict) {
		t.Fatalf("concurrent retired replay error = %v", err)
	}
	if _, err := os.Lstat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("concurrent replay recreated the retired worktree: %v", err)
	}
}

func TestWorktreeLifecycleRejectsExistingUnrelatedPathAndPreservesIt(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	path := filepath.Join(root, request.DeliveryID, request.AttemptID)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "preserve.txt")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Prepare(t.Context(), request); !errors.Is(err, ErrWorktreeConflict) {
		t.Fatalf("unrelated path error = %v", err)
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "preserve" {
		t.Fatalf("unrelated path was not preserved: content=%q err=%v", content, err)
	}
	quarantined, err := manager.Quarantine(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if quarantined.GitRegistered {
		t.Fatal("unrelated directory was classified as a Git worktree")
	}
	if content, err := os.ReadFile(filepath.Join(quarantined.QuarantinePath, "preserve.txt")); err != nil || string(content) != "preserve" {
		t.Fatalf("unregistered quarantine content=%q err=%v", content, err)
	}
}

func TestWorktreeLifecycleRejectsMutatedAttemptReplay(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	prepared, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	mutated := request
	mutated.WriteScopes = []string{"another-root"}
	if _, err := manager.Prepare(t.Context(), mutated); !errors.Is(err, ErrWorktreeConflict) {
		t.Fatalf("mutated replay error = %v", err)
	}
	if _, err := manager.Quarantine(t.Context(), mutated); !errors.Is(err, ErrWorktreeConflict) {
		t.Fatalf("mutated quarantine error = %v", err)
	}
	if _, err := os.Stat(prepared.Path); err != nil {
		t.Fatalf("mutated quarantine moved the authorized worktree: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(prepared.Path, "another-root")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mutated replay expanded writable roots: %v", err)
	}
	if _, err := manager.Quarantine(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeLifecycleQuarantineDoesNotMutateForeignGitMetadata(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	prepared, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "worktree", "remove", prepared.Path)

	foreignRepository, _, foreignBase := deliveryRepository(t)
	runGitTest(
		t,
		foreignRepository,
		"worktree", "add", "--quiet", "--detach", prepared.Path, foreignBase,
	)
	quarantined, err := manager.Quarantine(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !quarantined.GitRegistered {
		t.Fatal("foreign linked checkout was not recognized as Git-registered")
	}
	after := runGitTest(t, foreignRepository, "worktree", "list", "--porcelain")
	originalTail := filepath.Join(request.DeliveryID, request.AttemptID)
	if !strings.Contains(after, originalTail+"\n") ||
		strings.Contains(after, quarantined.QuarantinePath) {
		t.Fatalf("quarantine redirected foreign Git metadata: %s", after)
	}
	if _, err := os.Stat(quarantined.QuarantinePath); err != nil {
		t.Fatalf("foreign checkout bytes were not preserved: %v", err)
	}
}

func TestInterruptedGitMoveRequiresAdministrativeReconciliation(t *testing.T) {
	for _, test := range []struct {
		name                string
		metadataDestination bool
	}{
		{name: "filesystem moved before Git metadata"},
		{name: "Git metadata moved before filesystem", metadataDestination: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, rootPath, base := deliveryRepository(t)
			manager := testWorktreeManager(t)
			request := deliveryRequest(repository, rootPath, base)
			prepared, err := manager.Prepare(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := manager.resolveRequest(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			quarantineRelative := filepath.Join("quarantine", request.DeliveryID, request.AttemptID)
			if err := root.MkdirAll(filepath.Dir(quarantineRelative), 0o700); err != nil {
				t.Fatal(err)
			}
			quarantinePath := filepath.Join(rootPath, quarantineRelative)
			if test.metadataDestination {
				runGitTest(
					t,
					prepared.Path,
					"worktree", "move", prepared.Path, quarantinePath,
				)
				if err := root.Rename(quarantineRelative, resolved.attemptRelative); err != nil {
					t.Fatal(err)
				}
			} else if err := root.Rename(resolved.attemptRelative, quarantineRelative); err != nil {
				t.Fatal(err)
			}
			if err := reconcileQuarantineMutation(root, resolved, quarantineRelative); !errors.Is(err, ErrGitReconciliationRequired) {
				t.Fatalf("interrupted move reconciliation error = %v", err)
			}
			if _, err := os.Stat(filepath.Join(quarantinePath, ".forja-reconciliation-required")); err != nil {
				t.Fatalf("interrupted move lacks reconciliation marker: %v", err)
			}
			if _, err := os.Lstat(prepared.Path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("interrupted move source was not retired: %v", err)
			}
			registration := runGitTest(t, repository, "worktree", "list", "--porcelain")
			if test.metadataDestination {
				if !strings.Contains(registration, filepath.Join("quarantine", request.DeliveryID, request.AttemptID)) {
					t.Fatalf("Git destination registration was not preserved: %s", registration)
				}
			} else if !strings.Contains(registration, filepath.Join(request.DeliveryID, request.AttemptID)) ||
				strings.Contains(registration, filepath.Join("quarantine", request.DeliveryID, request.AttemptID)) {
				t.Fatalf("Git source registration was not preserved for reconciliation: %s", registration)
			}
		})
	}
}

func TestInterruptedGitMoveFailureShapesRetainSentinel(t *testing.T) {
	for _, shape := range []string{"both", "neither", "rename-failure"} {
		t.Run(shape, func(t *testing.T) {
			if shape == "rename-failure" && runtime.GOOS == "windows" {
				t.Skip("permission fixture requires a Unix host")
			}
			repository, rootPath, base := deliveryRepository(t)
			manager := testWorktreeManager(t)
			request := deliveryRequest(repository, rootPath, base)
			resolved, err := manager.resolveRequest(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			quarantineRelative := filepath.Join("quarantine", request.DeliveryID, request.AttemptID)
			quarantineParent := filepath.Dir(quarantineRelative)
			if err := root.MkdirAll(quarantineParent, 0o700); err != nil {
				t.Fatal(err)
			}
			switch shape {
			case "both":
				if err := root.MkdirAll(resolved.attemptRelative, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := root.Mkdir(quarantineRelative, 0o700); err != nil {
					t.Fatal(err)
				}
			case "rename-failure":
				if err := root.MkdirAll(resolved.attemptRelative, 0o700); err != nil {
					t.Fatal(err)
				}
				parentPath := filepath.Join(rootPath, quarantineParent)
				if err := os.Chmod(parentPath, 0o500); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(parentPath, 0o700)
			}
			err = reconcileQuarantineMutation(root, resolved, quarantineRelative)
			if !errors.Is(err, ErrGitReconciliationRequired) {
				t.Fatalf("%s reconciliation error = %v", shape, err)
			}
			if shape == "rename-failure" {
				if _, sourceErr := root.Lstat(resolved.attemptRelative); sourceErr != nil {
					t.Fatalf("rename failure fixture did not retain its source: %v", sourceErr)
				}
			}
		})
	}
}

func TestWorktreeLifecycleQuarantineRejectsSymlinkedNamespace(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	request := deliveryRequest(repository, root, base)
	outside := t.TempDir()
	outsideAttempt := filepath.Join(outside, request.AttemptID)
	if err := os.Mkdir(outsideAttempt, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(outsideAttempt, "preserve.txt")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureRequestAuthority(root, request, true); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, request.DeliveryID)); err != nil {
		t.Fatal(err)
	}
	manager := testWorktreeManager(t)
	if _, err := manager.Quarantine(t.Context(), request); !errors.Is(err, ErrWorktreeConflict) {
		t.Fatalf("symlinked quarantine source error = %v", err)
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "preserve" {
		t.Fatalf("external source was changed: content=%q err=%v", content, err)
	}
}

func TestWorktreeLifecycleRejectsPhysicalRootOverlap(t *testing.T) {
	repository, _, base := deliveryRepository(t)
	actualRoot := filepath.Join(repository, "nested-worktrees")
	if err := os.Mkdir(actualRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	container := t.TempDir()
	link := filepath.Join(container, "repository-link")
	if err := os.Symlink(repository, link); err != nil {
		t.Fatal(err)
	}
	request := deliveryRequest(repository, filepath.Join(link, "nested-worktrees"), base)
	manager := testWorktreeManager(t)
	if _, err := manager.Prepare(t.Context(), request); err == nil ||
		!strings.Contains(err.Error(), "resolved repository and worktree roots must be disjoint") {
		t.Fatalf("physical overlap error = %v", err)
	}
}

func TestWorktreeLifecycleRejectsCaseAliasedRootOverlap(t *testing.T) {
	parent := t.TempDir()
	repository := filepath.Join(parent, "MixedCase")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "init", "--quiet")
	runGitTest(t, repository, "config", "user.name", "Forja Test")
	runGitTest(t, repository, "config", "user.email", "forja-test@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "README.md")
	runGitTest(t, repository, "commit", "--quiet", "-m", "initial")
	base := strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD"))
	caseAlias := filepath.Join(parent, "mixedcase")
	repositoryInfo, repositoryErr := os.Stat(repository)
	aliasInfo, aliasErr := os.Stat(caseAlias)
	if repositoryErr != nil || aliasErr != nil || !os.SameFile(repositoryInfo, aliasInfo) {
		t.Skip("fixture filesystem is case-sensitive")
	}
	request := deliveryRequest(repository, caseAlias, base)
	manager := testWorktreeManager(t)
	if _, err := manager.Prepare(t.Context(), request); err == nil ||
		!strings.Contains(err.Error(), "resolved repository and worktree roots must be disjoint") {
		t.Fatalf("case-aliased overlap error = %v", err)
	}
}

func TestWorktreeLifecycleRejectsScopeSymlinkEscapeAndCleansNewCheckout(t *testing.T) {
	repository, root, _ := deliveryRepository(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repository, "generated")); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "generated")
	runGitTest(t, repository, "commit", "-m", "add scope symlink")
	base := strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD"))
	request := deliveryRequest(repository, root, base)
	request.WriteScopes = []string{"generated"}
	manager := testWorktreeManager(t)
	if _, err := manager.Prepare(t.Context(), request); err == nil {
		t.Fatal("symlinked writable scope passed")
	}
	derived := filepath.Join(root, request.DeliveryID, request.AttemptID)
	if _, err := os.Lstat(derived); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed fresh checkout was not cleaned: %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("scope escape changed outside directory: entries=%v err=%v", entries, err)
	}
}

func TestWorktreeLifecycleRejectsRepositoryContentFilterBeforeCheckout(t *testing.T) {
	repository, root, _ := deliveryRepository(t)
	if err := os.WriteFile(
		filepath.Join(repository, ".gitattributes"),
		[]byte("*.payload filter=host-command\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "fixture.payload"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", ".gitattributes", "fixture.payload")
	runGitTest(t, repository, "commit", "-m", "add filter fixture")
	base := strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD"))
	marker := filepath.Join(t.TempDir(), "filter-ran")
	command := "sh -c " + shellSingleQuote("cat; printf filter > "+shellSingleQuote(marker))
	runGitTest(t, repository, "config", "extensions.worktreeConfig", "true")
	runGitTest(t, repository, "config", "--worktree", "filter.host-command.smudge", command)

	request := deliveryRequest(repository, root, base)
	manager := testWorktreeManager(t)
	if _, err := manager.Prepare(t.Context(), request); err == nil ||
		!strings.Contains(err.Error(), "content filters are not permitted") {
		t.Fatalf("content filter error = %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("repository content filter executed")
	}
	derived := filepath.Join(root, request.DeliveryID, request.AttemptID)
	if _, err := os.Lstat(derived); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("content-filter rejection created a worktree: %v", err)
	}
}

func TestWorktreeLifecycleGitCommandsHaveInternalDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("FIFO fixture requires a Unix host")
	}
	repository, root, base := deliveryRepository(t)
	fifo := filepath.Join(t.TempDir(), "blocked.gitconfig")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("FIFO unavailable: %v", err)
	}
	runGitTest(t, repository, "config", "include.path", fifo)
	manager := testWorktreeManager(t)
	started := time.Now()
	if _, err := manager.Prepare(t.Context(), deliveryRequest(repository, root, base)); err == nil {
		t.Fatal("blocking Git include bypassed the delivery deadline")
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("delivery Git deadline was not enforced: %v", elapsed)
	}
}

func TestWorktreeLifecycleRetiresInterruptedMutation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable Git wrapper fixture requires a Unix host")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := "#!/bin/sh\n" +
		"next=0\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$next\" = 1 ]; then\n" +
		"    mkdir -p \"$arg\"\n" +
		"    printf partial > \"$arg/partial.txt\"\n" +
		"    exec /bin/sleep 60\n" +
		"  fi\n" +
		"  if [ \"$arg\" = --detach ]; then next=1; fi\n" +
		"done\n" +
		"exec " + shellSingleQuote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	repository, root, base := deliveryRepository(t)
	manager, err := NewWorktreeManager(wrapper, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.readTimeout = 30 * time.Second
	manager.mutationTimeout = 100 * time.Millisecond
	request := deliveryRequest(repository, root, base)
	if _, err := manager.Prepare(t.Context(), request); !errors.Is(err, ErrGitReconciliationRequired) {
		t.Fatalf("interrupted worktree mutation error = %v", err)
	}
	quarantinePath := filepath.Join(root, "quarantine", request.DeliveryID, request.AttemptID)
	content, err := os.ReadFile(filepath.Join(quarantinePath, "partial.txt"))
	if err != nil || string(content) != "partial" {
		t.Fatalf("interrupted mutation bytes were not preserved: content=%q err=%v", content, err)
	}
	if _, err := manager.Prepare(t.Context(), request); !errors.Is(err, ErrWorktreeConflict) {
		t.Fatalf("interrupted mutation identity was reusable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(quarantinePath, ".forja-reconciliation-required")); err != nil {
		t.Fatalf("interrupted mutation lacks reconciliation marker: %v", err)
	}
}

func TestWorktreeLifecycleRejectsChangedHEADAndHiddenIndexState(t *testing.T) {
	t.Run("changed HEAD", func(t *testing.T) {
		repository, root, base := deliveryRepository(t)
		manager := testWorktreeManager(t)
		request := deliveryRequest(repository, root, base)
		prepared, err := manager.Prepare(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repository, "second.txt"), []byte("second\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, repository, "add", "second.txt")
		runGitTest(t, repository, "commit", "-m", "second")
		second := strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD"))
		runGitTest(t, prepared.Path, "checkout", "--detach", second)
		if _, err := manager.Inspect(t.Context(), request); !errors.Is(err, ErrWorktreeConflict) {
			t.Fatalf("changed HEAD error = %v", err)
		}
		if _, err := manager.Quarantine(t.Context(), request); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("hidden index", func(t *testing.T) {
		repository, root, base := deliveryRepository(t)
		manager := testWorktreeManager(t)
		request := deliveryRequest(repository, root, base)
		prepared, err := manager.Prepare(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		runGitTest(t, prepared.Path, "update-index", "--skip-worktree", "README.md")
		if _, err := manager.Inspect(t.Context(), request); err == nil ||
			!strings.Contains(err.Error(), "hidden path flags") {
			t.Fatalf("hidden index error = %v", err)
		}
		if _, err := manager.Quarantine(t.Context(), request); err != nil {
			t.Fatal(err)
		}
	})
}

func TestWorktreeLifecycleTreatsIgnoredFilesAsContamination(t *testing.T) {
	repository, root, _ := deliveryRepository(t)
	if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte("cache/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", ".gitignore")
	runGitTest(t, repository, "commit", "-m", "ignore cache")
	base := strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD"))
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	prepared, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(prepared.Path, "cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "state"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Inspect(t.Context(), request); !errors.Is(err, ErrWorktreeDirty) {
		t.Fatalf("ignored contamination error = %v", err)
	}
	if _, err := manager.Quarantine(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

func TestGitEnvironmentUsesMinimalAllowlist(t *testing.T) {
	environment := gitEnvironment([]string{
		"PATH=/usr/bin",
		"HOME=/safe/home",
		"TMPDIR=/safe/tmp",
		"LD_PRELOAD=/tmp/inject.so",
		"DYLD_INSERT_LIBRARIES=/tmp/inject.dylib",
		"GIT_CONFIG_GLOBAL=/tmp/hostile-config",
		"SSH_ASKPASS=/tmp/askpass",
		"DATABASE_URL=postgresql://secret",
	})
	joined := strings.Join(environment, "\n")
	for _, required := range []string{
		"PATH=/usr/bin",
		"HOME=/safe/home",
		"TMPDIR=/safe/tmp",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"LC_ALL=C",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("sanitized Git environment lacks %q: %s", required, joined)
		}
	}
	for _, forbidden := range []string{
		"LD_PRELOAD", "DYLD_", "hostile-config", "SSH_ASKPASS", "DATABASE_URL", "secret",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("sanitized Git environment contains %q: %s", forbidden, joined)
		}
	}
}

func deliveryRepository(t *testing.T) (string, string, string) {
	t.Helper()
	repository := t.TempDir()
	root := t.TempDir()
	runGitTest(t, repository, "init", "--quiet")
	runGitTest(t, repository, "config", "user.name", "Forja Test")
	runGitTest(t, repository, "config", "user.email", "forja-test@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("# fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repository, "internal"), 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "README.md", "internal")
	runGitTest(t, repository, "commit", "--quiet", "-m", "initial")
	base := strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD"))
	return repository, root, base
}

func deliveryRequest(repository string, root string, base string) contracts.DeliveryRequest {
	previous := base
	return contracts.DeliveryRequest{
		DeliveryID:                "delivery_11111111-1111-4111-8111-111111111111",
		TaskID:                    "task_11111111-1111-4111-8111-111111111111",
		AttemptID:                 "attempt_11111111-1111-4111-8111-111111111111",
		RunID:                     "run_11111111-1111-4111-8111-111111111111",
		SchemaVersion:             "1.0",
		RepositoryPath:            repository,
		WorktreeRoot:              root,
		BaseCommit:                base,
		PublicationRef:            "refs/forja/deliveries/delivery_11111111-1111-4111-8111-111111111111",
		PublicationPreviousCommit: &previous,
		AuthorID:                  "worker-author",
		ValidatorID:               "independent-validator",
		Role:                      "implementer",
		Objective:                 "Produce one bounded fixture change.",
		ReadScopes:                []string{"."},
		WriteScopes:               []string{"internal/generated"},
		ArtifactScopes:            []string{"evidence"},
		EvidenceScope:             "evidence",
		AttemptOrdinal:            1,
		WorkerBudgets: contracts.WorkerBudgets{
			WallClockMS:         1_000,
			InactivityMS:        500,
			MaxOutputBytes:      4_096,
			CancellationGraceMS: 100,
			MaxRetries:          2,
		},
		MechanicalValidatorIDs: []string{"unit-tests"},
		LeaseTTLMS:             2_000,
	}
}

func testWorktreeManager(t *testing.T) *WorktreeManager {
	t.Helper()
	manager, err := NewWorktreeManager("git", nil)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func runGitTest(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(
		t.Context(),
		"git",
		append([]string{"-C", directory}, arguments...)...,
	)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func installPostCheckoutHook(t *testing.T, repository string, marker string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("executable Git hook fixture requires a Unix host")
	}
	hook := filepath.Join(repository, ".git", "hooks", "post-checkout")
	content := "#!/bin/sh\nprintf hook > " + shellSingleQuote(marker) + "\n"
	if err := os.WriteFile(hook, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
