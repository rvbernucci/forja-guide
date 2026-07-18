package delivery

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCreateResultCommitIsDeterministicAndDoesNotMutateAttemptGitState(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	worktree, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	changedPath := filepath.Join(worktree.Path, "internal/generated/result.txt")
	if err := os.WriteFile(changedPath, []byte("bounded result\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statusBefore := runGitTest(t, worktree.Path, "status", "--porcelain=v1", "--untracked-files=all")

	first, err := manager.CreateResultCommit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.CreateResultCommit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.ResultCommit != second.ResultCommit || first.ResultTree != second.ResultTree ||
		first.PatchSHA256 != second.PatchSHA256 || !slices.Equal(first.ChangedPaths, second.ChangedPaths) {
		t.Fatalf("deterministic result changed: first=%#v second=%#v", first, second)
	}
	if first.BaseCommit != base ||
		!slices.Equal(first.ChangedPaths, []string{"internal/generated/result.txt"}) {
		t.Fatalf("unexpected result identity: %#v", first)
	}
	if head := strings.TrimSpace(runGitTest(t, worktree.Path, "rev-parse", "HEAD")); head != base {
		t.Fatalf("attempt HEAD changed to %s", head)
	}
	if statusAfter := runGitTest(t, worktree.Path, "status", "--porcelain=v1", "--untracked-files=all"); statusAfter != statusBefore {
		t.Fatalf("attempt index or bytes changed: before=%q after=%q", statusBefore, statusAfter)
	}
	if author := strings.TrimSpace(runGitTest(t, repository, "show", "-s", "--format=%an <%ae>", first.ResultCommit)); author != "Forja Delivery Service <delivery@forja.invalid>" {
		t.Fatalf("supervisor author = %q", author)
	}
	if message := runGitTest(t, repository, "show", "-s", "--format=%B", first.ResultCommit); message != "Forja delivery "+request.DeliveryID+"\n\n" {
		t.Fatalf("result commit message = %q", message)
	}
	baseTime := strings.TrimSpace(runGitTest(t, repository, "show", "-s", "--format=%ct", base))
	resultTime := strings.TrimSpace(runGitTest(t, repository, "show", "-s", "--format=%ct", first.ResultCommit))
	if resultTime == baseTime {
		t.Fatal("result commit did not advance its deterministic timestamp")
	}
}

func TestCreateResultCommitRejectsOutOfScopeAndIgnoredBytes(t *testing.T) {
	t.Run("out of scope", func(t *testing.T) {
		repository, root, base := deliveryRepository(t)
		manager := testWorktreeManager(t)
		request := deliveryRequest(repository, root, base)
		worktree, err := manager.Prepare(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(worktree.Path, "README.md"), []byte("escaped\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.CreateResultCommit(t.Context(), request); err == nil ||
			!strings.Contains(err.Error(), "outside approved write scopes") {
			t.Fatalf("out-of-scope commit error = %v", err)
		}
		if head := strings.TrimSpace(runGitTest(t, worktree.Path, "rev-parse", "HEAD")); head != base {
			t.Fatalf("failed attempt HEAD changed to %s", head)
		}
	})

	t.Run("ignored", func(t *testing.T) {
		repository, root, base := deliveryRepository(t)
		manager := testWorktreeManager(t)
		request := deliveryRequest(repository, root, base)
		worktree, err := manager.Prepare(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(worktree.Path, ".gitignore"), []byte("*.secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(worktree.Path, "internal/generated/value.secret"), []byte("hidden\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.CreateResultCommit(t.Context(), request); !errors.Is(err, ErrWorktreeDirty) {
			t.Fatalf("ignored-byte commit error = %v", err)
		}
	})
}

func TestCreateResultCommitCapturesDeletionAndAdditionInByteOrder(t *testing.T) {
	repository, root, base := deliveryRepository(t)
	if err := os.MkdirAll(filepath.Join(repository, "internal/generated"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "internal/generated/old.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "internal/generated/old.txt")
	runGitTest(t, repository, "commit", "--quiet", "-m", "fixture")
	base = strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD"))
	manager := testWorktreeManager(t)
	request := deliveryRequest(repository, root, base)
	worktree, err := manager.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(worktree.Path, "internal/generated/old.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree.Path, "internal/generated/new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := manager.CreateResultCommit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"internal/generated/new.txt", "internal/generated/old.txt"}
	if !slices.Equal(result.ChangedPaths, want) {
		t.Fatalf("changed paths = %q, want %q", result.ChangedPaths, want)
	}
}
