package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/indexing"
	"github.com/rvbernucci/forja-guide/internal/postgres"
)

func TestIndexerCommandPublishesIncrementalSnapshot(t *testing.T) {
	databaseURL := os.Getenv("FORJA_TEST_INDEX_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("real index command drill is not configured")
	}
	endpoint, bucket := newIndexProtocolStore(t)

	repository := t.TempDir()
	runIndexGit(t, repository, "init", "-q")
	runIndexGit(t, repository, "config", "user.email", "index@example.invalid")
	runIndexGit(t, repository, "config", "user.name", "Index Fixture")
	writeIndexFixture(t, repository, "app.py", "value: int = 1\n")
	writeIndexFixture(t, repository, "go.mod", "module example.invalid/index-fixture\n\ngo 1.26\n")
	writeIndexFixture(t, repository, "main.go", "package fixture\n\nfunc Value() int { return 1 }\n")
	writeIndexFixture(t, repository, "src/main.ts", "export const value: number = 1;\n")
	runIndexGit(t, repository, "add", ".")
	commitIndexFixture(t, repository, "initial", "2026-07-19T10:00:00Z")

	toolRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORJA_DATABASE_URL", databaseURL)
	t.Setenv("FORJA_S3_BUCKET", bucket)
	t.Setenv("FORJA_S3_REGION", "us-east-1")
	t.Setenv("FORJA_S3_ENDPOINT", endpoint)
	t.Setenv("FORJA_S3_PATH_STYLE", "true")
	t.Setenv("FORJA_TENANT_ID", postgres.DefaultTenantID)
	t.Setenv("FORJA_REPOSITORY_ID", postgres.DefaultRepositoryID)
	t.Setenv("FORJA_INDEX_ACTOR_ID", "integration-indexer")
	t.Setenv("AWS_ACCESS_KEY_ID", "forja-index-drill")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "forja-index-drill-secret")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	arguments := []string{
		"--repository", repository,
		"--revision", "HEAD",
		"--tool-root", toolRoot,
		"--idempotency-key", "index-command-initial",
		"--timeout", "2m",
		"--python-version", "3.14",
	}
	if err := run(t.Context(), arguments); err != nil {
		t.Fatalf("publish initial snapshot: %v", err)
	}

	writeIndexFixture(t, repository, "app.py", "value: int = 2\n")
	runIndexGit(t, repository, "add", "app.py")
	commitIndexFixture(t, repository, "incremental", "2026-07-19T10:01:00Z")
	arguments[7] = "index-command-incremental"
	if err := run(t.Context(), arguments); err != nil {
		t.Fatalf("publish incremental snapshot: %v", err)
	}

	pool, err := postgres.Open(t.Context(), databaseURL, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store, err := postgres.NewStore(
		pool, nil, postgres.DefaultTenantID, postgres.DefaultRepositoryID,
	)
	if err != nil {
		t.Fatal(err)
	}
	bundle, found, err := store.GetActiveIndexBundle(t.Context())
	if err != nil || !found {
		t.Fatalf("active bundle found=%v error=%v", found, err)
	}
	targetCommit := runIndexGitOutput(t, repository, "rev-parse", "HEAD")
	if bundle.Snapshot.SourceCommit != targetCommit || len(bundle.Files) != 4 {
		t.Fatalf("active snapshot commit=%s files=%d", bundle.Snapshot.SourceCommit, len(bundle.Files))
	}
	adapterStatuses := func(snapshotID string) []string {
		rows, queryErr := pool.Query(t.Context(), `
			SELECT adapter_name, status
			FROM forja.index_adapter_runs
			WHERE tenant_id=$1 AND repository_id=$2 AND snapshot_id=$3
			ORDER BY adapter_name`,
			postgres.DefaultTenantID, postgres.DefaultRepositoryID, snapshotID,
		)
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		defer rows.Close()
		statuses := make([]string, 0, 3)
		for rows.Next() {
			var name, status string
			if scanErr := rows.Scan(&name, &status); scanErr != nil {
				t.Fatal(scanErr)
			}
			statuses = append(statuses, name+"="+status)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			t.Fatal(rowsErr)
		}
		return statuses
	}
	statuses := adapterStatuses(bundle.Snapshot.SnapshotID)
	want := []string{"go=reused", "python=passed", "typescript=reused"}
	if strings.Join(statuses, ",") != strings.Join(want, ",") {
		t.Fatalf("adapter statuses=%v want=%v", statuses, want)
	}
	previousGoQualified := goQualifiedNames(bundle)
	writeIndexFixture(t, repository, "go.mod", "module example.invalid/index-fixture-v2\n\ngo 1.26\n")
	runIndexGit(t, repository, "add", "go.mod")
	commitIndexFixture(t, repository, "Go module metadata", "2026-07-19T10:02:00Z")
	arguments[7] = "index-command-go-module"
	if err := run(t.Context(), arguments); err != nil {
		t.Fatalf("publish Go metadata snapshot: %v", err)
	}
	configurationBundle, found, err := store.GetActiveIndexBundle(t.Context())
	if err != nil || !found {
		t.Fatalf("Go metadata bundle found=%v error=%v", found, err)
	}
	statuses = adapterStatuses(configurationBundle.Snapshot.SnapshotID)
	want = []string{"go=passed", "python=reused", "typescript=reused"}
	if strings.Join(statuses, ",") != strings.Join(want, ",") {
		t.Fatalf("Go metadata adapter statuses=%v want=%v", statuses, want)
	}
	if current := goQualifiedNames(configurationBundle); strings.Join(current, ",") == strings.Join(previousGoQualified, ",") {
		t.Fatalf("go.mod change reused stale qualified names=%v", current)
	}
	bundle = configurationBundle
	otherRepositoryID := "00000000-0000-4000-8000-000000000009"
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.repositories (repository_id, tenant_id, canonical_name)
		VALUES ($1,$2,'index-command-other-repository')`,
		otherRepositoryID, postgres.DefaultTenantID,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORJA_REPOSITORY_ID", otherRepositoryID)
	arguments[7] = "index-command-other-repository"
	if err := run(t.Context(), arguments); err != nil {
		t.Fatalf("publish other repository snapshot: %v", err)
	}
	otherStore, err := postgres.NewStore(
		pool, nil, postgres.DefaultTenantID, otherRepositoryID,
	)
	if err != nil {
		t.Fatal(err)
	}
	otherBundle, found, err := otherStore.GetActiveIndexBundle(t.Context())
	if err != nil || !found || otherBundle.Snapshot.RepositoryID == bundle.Snapshot.RepositoryID {
		t.Fatalf("other repository bundle=%#v found=%v error=%v", otherBundle.Snapshot, found, err)
	}
	stillActive, found, err := store.GetActiveIndexBundle(t.Context())
	if err != nil || !found || stillActive.Snapshot.SnapshotID != bundle.Snapshot.SnapshotID {
		t.Fatalf("original repository authority changed: found=%v error=%v", found, err)
	}
	var snapshots, artifacts, receipts int
	if err := pool.QueryRow(t.Context(), `
		SELECT
			(SELECT count(*) FROM forja.index_snapshots),
			(SELECT count(*) FROM forja.artifacts WHERE kind='index_snapshot'),
			(SELECT count(*) FROM forja.idempotency_keys WHERE scope LIKE 'index_publish:%')`,
	).Scan(&snapshots, &artifacts, &receipts); err != nil {
		t.Fatal(err)
	}
	if snapshots != 4 || artifacts != 4 || receipts != 4 {
		t.Fatalf("snapshots=%d artifacts=%d receipts=%d", snapshots, artifacts, receipts)
	}
}

func goQualifiedNames(bundle indexing.IndexBundle) []string {
	result := make([]string, 0)
	for _, symbol := range bundle.Symbols {
		if symbol.Language == "go" {
			result = append(result, symbol.QualifiedName)
		}
	}
	sort.Strings(result)
	return result
}

func TestIndexerCommandRequiresOperatorBoundAuthority(t *testing.T) {
	for _, key := range []string{
		"FORJA_DATABASE_URL", "FORJA_S3_BUCKET", "FORJA_S3_REGION",
		"FORJA_TENANT_ID", "FORJA_REPOSITORY_ID", "FORJA_INDEX_ACTOR_ID",
	} {
		t.Setenv(key, "")
	}
	err := run(t.Context(), []string{"--idempotency-key", "missing-authority"})
	if err == nil || !strings.Contains(err.Error(), "FORJA_TENANT_ID") ||
		!strings.Contains(err.Error(), "FORJA_INDEX_ACTOR_ID") {
		t.Fatalf("authority error=%v", err)
	}
}

func TestReusableAdapterDescriptorsRejectsChangedResolutionMetadata(t *testing.T) {
	adapters := []indexing.Adapter{
		indexing.NewGoAdapter(),
		indexing.NewPythonAdapter(".", "3.14"),
		indexing.NewTypeScriptAdapter("."),
	}
	documents := []indexing.SourceDocument{
		{CommittedFile: indexing.CommittedFile{Path: "main.go", Language: "go"}},
		{CommittedFile: indexing.CommittedFile{Path: "app.py", Language: "python"}},
		{CommittedFile: indexing.CommittedFile{Path: "src/main.ts", Language: "typescript"}},
	}
	baseline := indexing.IndexBundle{
		Snapshot: contracts.RepositorySnapshot{Adapters: []contracts.AdapterDescriptor{
			adapters[0].Descriptor(), adapters[1].Descriptor(), adapters[2].Descriptor(),
		}},
		Files: []contracts.FileCard{
			{Path: "main.go", Language: "go"},
			{Path: "app.py", Language: "python"},
			{Path: "src/main.ts", Language: "typescript"},
		},
	}
	plan := indexing.InvalidationPlan{ReusableFiles: []indexing.ReuseCandidate{
		{Path: "main.go"}, {Path: "app.py"}, {Path: "src/main.ts"},
	}}
	tests := []struct {
		name        string
		change      indexing.GitChange
		invalidated string
	}{
		{name: "go module", change: indexing.GitChange{Kind: "modified", Path: "go.mod"}, invalidated: "go"},
		{name: "nested Go workspace", change: indexing.GitChange{Kind: "modified", Path: "tools/go.work"}, invalidated: "go"},
		{name: "TypeScript project", change: indexing.GitChange{Kind: "modified", Path: "web/tsconfig.build.json"}, invalidated: "typescript"},
		{name: "JavaScript package", change: indexing.GitChange{Kind: "modified", Path: "web/package.json"}, invalidated: "typescript"},
		{name: "Python project", change: indexing.GitChange{Kind: "modified", Path: "services/api/pyproject.toml"}, invalidated: "python"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reused := reusableAdapterDescriptors(
				adapters, baseline, plan,
				indexing.GitChangeSet{Changes: []indexing.GitChange{test.change}},
				documents, true,
			)
			if len(reused) != len(adapters)-1 {
				t.Fatalf("reused adapters=%v", reused)
			}
			for _, descriptor := range reused {
				if descriptor.Name == test.invalidated {
					t.Fatalf("configuration-changed adapter %s was reused", descriptor.Name)
				}
			}
		})
	}
}

func newIndexProtocolStore(t *testing.T) (string, string) {
	t.Helper()
	type object struct {
		body        []byte
		contentType string
		checksum    string
		metadata    map[string]string
		etag        string
		version     string
	}
	const bucket = "forja-sprint08-index"
	var mutex sync.Mutex
	objects := make(map[string]object)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		if !strings.HasPrefix(request.URL.Path, "/"+bucket+"/tenants/") ||
			request.Header.Get("Authorization") == "" {
			http.Error(writer, "invalid authority envelope", http.StatusBadRequest)
			return
		}
		switch request.Method {
		case http.MethodPut:
			if request.Header.Get("If-None-Match") != "*" ||
				request.Header.Get("X-Amz-Checksum-Sha256") == "" {
				http.Error(writer, "missing immutable integrity headers", http.StatusBadRequest)
				return
			}
			if _, exists := objects[request.URL.Path]; exists {
				writer.Header().Set("Content-Type", "application/xml")
				writer.WriteHeader(http.StatusPreconditionFailed)
				_, _ = writer.Write([]byte(`<Error><Code>PreconditionFailed</Code><Message>exists</Message></Error>`))
				return
			}
			body, err := io.ReadAll(request.Body)
			if err != nil {
				http.Error(writer, "read body", http.StatusBadRequest)
				return
			}
			sequence := len(objects) + 1
			value := object{
				body: body, contentType: request.Header.Get("Content-Type"),
				checksum: request.Header.Get("X-Amz-Checksum-Sha256"),
				metadata: map[string]string{
					"sha256":     request.Header.Get("X-Amz-Meta-Forja-Sha256"),
					"size":       request.Header.Get("X-Amz-Meta-Forja-Size"),
					"media-type": request.Header.Get("X-Amz-Meta-Forja-Media-Type"),
				},
				etag:    fmt.Sprintf("\"index-etag-%d\"", sequence),
				version: fmt.Sprintf("index-version-%d", sequence),
			}
			objects[request.URL.Path] = value
			writer.Header().Set("ETag", value.etag)
			writer.Header().Set("X-Amz-Version-Id", value.version)
			writer.WriteHeader(http.StatusOK)
		case http.MethodGet:
			value, exists := objects[request.URL.Path]
			if !exists {
				http.NotFound(writer, request)
				return
			}
			writer.Header().Set("Content-Type", value.contentType)
			writer.Header().Set("Content-Length", strconv.Itoa(len(value.body)))
			writer.Header().Set("ETag", value.etag)
			writer.Header().Set("X-Amz-Version-Id", value.version)
			writer.Header().Set("X-Amz-Checksum-Sha256", value.checksum)
			writer.Header().Set("X-Amz-Meta-Forja-Sha256", value.metadata["sha256"])
			writer.Header().Set("X-Amz-Meta-Forja-Size", value.metadata["size"])
			writer.Header().Set("X-Amz-Meta-Forja-Media-Type", value.metadata["media-type"])
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(value.body)
		default:
			http.Error(writer, "unsupported method", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL, bucket
}

func writeIndexFixture(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func commitIndexFixture(t *testing.T, root, message, timestamp string) {
	t.Helper()
	command := exec.Command("git", "-C", root, "commit", "-qm", message)
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE="+timestamp,
		"GIT_COMMITTER_DATE="+timestamp,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("commit fixture: %v: %s", err, output)
	}
}

func runIndexGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

func runIndexGitOutput(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", arguments, err)
	}
	return string(bytes.TrimSpace(output))
}
