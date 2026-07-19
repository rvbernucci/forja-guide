package indexing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestParseTreeListingIsCanonicalAndBounded(t *testing.T) {
	t.Parallel()
	listing := bytes.Join([][]byte{
		[]byte("100644 blob " + repeatHex("a", 40) + " 12\tz.go"),
		[]byte("100755 blob " + repeatHex("b", 40) + " 10\tcmd/a.go"),
		{},
	}, []byte{0})
	files, err := parseTreeListing(listing)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != "cmd/a.go" || files[1].Path != "z.go" {
		t.Fatalf("files=%#v", files)
	}
	if files[0].Language != "go" || files[0].Mode != "100755" {
		t.Fatalf("file=%#v", files[0])
	}
}

func TestParseTreeListingRejectsSymlinkSubmoduleAndCaseCollision(t *testing.T) {
	t.Parallel()
	for name, listing := range map[string][]byte{
		"symlink":   []byte("120000 blob " + repeatHex("a", 40) + " 4\tlink\x00"),
		"submodule": []byte("160000 commit " + repeatHex("a", 40) + " -\tmodule\x00"),
		"collision": []byte("100644 blob " + repeatHex("a", 40) + " 1\tA.go\x00100644 blob " + repeatHex("b", 40) + " 1\ta.go\x00"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseTreeListing(listing); !errors.Is(err, ErrUnsupportedGitEntry) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestParseNameStatusPreservesRenameEvidence(t *testing.T) {
	t.Parallel()
	changes, err := parseNameStatus([]byte("M\x00b.go\x00R100\x00old.go\x00new.go\x00A\x00a.go\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 || changes[0].Path != "a.go" || changes[1].Path != "b.go" ||
		changes[2].Kind != "renamed" || changes[2].FromPath == nil || *changes[2].FromPath != "old.go" {
		t.Fatalf("changes=%#v", changes)
	}
}

func TestParseNameStatusRejectsNonUTF8Paths(t *testing.T) {
	t.Parallel()
	for name, value := range map[string][]byte{
		"changed path":  []byte{'M', 0, 0xff, 0},
		"rename target": []byte{'R', '1', '0', '0', 0, 'a', '.', 'g', 'o', 0, 0xff, 0},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseNameStatus(value); !errors.Is(err, ErrUnsupportedGitEntry) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestGitOutputLimitsAreCommandSpecific(t *testing.T) {
	t.Parallel()
	if gitOutputLimit([]string{"rev-parse"}) != 1024 ||
		gitOutputLimit([]string{"cat-file"}) != (16<<20)+1 ||
		gitOutputLimit([]string{"ls-tree"}) != maximumGitMetadataBytes {
		t.Fatal("Git output limits do not match their bounded command classes")
	}
}

func TestGitSourceRejectsNonUTF8IndexedSourceButAllowsOpaqueBinary(t *testing.T) {
	t.Parallel()
	body := []byte{0xff, 0xfe}
	source, err := NewGitSource(staticGitRunner{output: body})
	if err != nil {
		t.Fatal(err)
	}
	file := CommittedFile{
		Path: "main.py", GitBlobID: repeatHex("a", 40),
		SizeBytes: int64(len(body)), Language: "python",
	}
	if _, _, err := source.ReadFile(context.Background(), file); !errors.Is(err, ErrUnsupportedGitEntry) {
		t.Fatalf("indexed source error=%v", err)
	}
	file.Path, file.Language = "logo.png", "other"
	read, _, err := source.ReadFile(context.Background(), file)
	if err != nil || !bytes.Equal(read, body) {
		t.Fatalf("opaque binary=%v error=%v", read, err)
	}
}

func TestGitSourceReadsCommittedBytesNotWorktree(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "fixture@example.invalid")
	runGit(t, root, "config", "user.name", "Fixture")
	path := filepath.Join(root, "main.go")
	committed := []byte("package main\n\nfunc main() {}\n")
	if err := os.WriteFile(path, committed, 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "main.go")
	runGit(t, root, "commit", "-qm", "fixture")
	if err := os.WriteFile(path, []byte("uncommitted secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := NewGitSource(ExecGitRunner{RepositoryPath: root})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := source.InspectCommit(context.Background(), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Files) != 1 || tree.Files[0].Path != "main.go" {
		t.Fatalf("tree=%#v", tree)
	}
	if tree.CommittedAt.IsZero() || tree.CommittedAt.Location() != time.UTC {
		t.Fatalf("commit timestamp=%v", tree.CommittedAt)
	}
	body, digest, err := source.ReadFile(context.Background(), tree.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256(committed)
	if !bytes.Equal(body, committed) || digest != "sha256:"+hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("body=%q digest=%s", body, digest)
	}
}

func TestGitSourceProducesDeterministicChangeSet(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "fixture@example.invalid")
	runGit(t, root, "config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(root, "old.go"), []byte("package old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "old.go")
	runGit(t, root, "commit", "-qm", "base")
	base := runGitOutput(t, root, "rev-parse", "HEAD")
	runGit(t, root, "mv", "old.go", "new.go")
	if err := os.WriteFile(filepath.Join(root, "added.py"), []byte("value = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "added.py")
	runGit(t, root, "commit", "-qm", "target")
	target := runGitOutput(t, root, "rev-parse", "HEAD")

	source, _ := NewGitSource(ExecGitRunner{RepositoryPath: root})
	changeSet, err := source.ChangeSet(context.Background(), base, target)
	if err != nil {
		t.Fatal(err)
	}
	if changeSet.BaseCommit != base || changeSet.TargetCommit != target || len(changeSet.Changes) != 2 {
		t.Fatalf("change set=%#v", changeSet)
	}
	if changeSet.Changes[0].Kind != "added" || changeSet.Changes[0].Path != "added.py" ||
		changeSet.Changes[1].Kind != "renamed" || changeSet.Changes[1].Path != "new.go" ||
		changeSet.Changes[1].FromPath == nil || *changeSet.Changes[1].FromPath != "old.go" {
		t.Fatalf("changes=%#v", changeSet.Changes)
	}
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func runGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(bytes.TrimSpace(output))
}

func repeatHex(value string, count int) string {
	return string(bytes.Repeat([]byte(value), count))
}

type staticGitRunner struct{ output []byte }

func (r staticGitRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return append([]byte(nil), r.output...), nil
}
