package postgres

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/daemon"
	"github.com/rvbernucci/forja-guide/internal/identity"
)

const postgresHTTPTestBearerToken = "forja-postgres-http-test-token-0001"

func postgresHTTPTestAuthenticator(t *testing.T) daemon.Authenticator {
	t.Helper()
	principal, err := control.NewPrincipal("system", "postgres-http-test", control.AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := daemon.NewStaticBearerAuthenticator(postgresHTTPTestBearerToken, principal)
	if err != nil {
		t.Fatal(err)
	}
	return authenticator
}

func TestHTTPCreateRetryReplaysAcrossGeneratedIDs(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatalf("create contract registry: %v", err)
	}
	candidates := []identity.RunID{mustRunID(t), mustRunID(t)}
	next := 0
	server, err := daemon.New(
		store,
		registry,
		postgresHTTPTestAuthenticator(t),
		store.Authority(),
		func() (identity.RunID, error) {
			id := candidates[next]
			next++
			return id, nil
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("create daemon: %v", err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	first := postCreateRetry(t, httpServer.URL)
	second := postCreateRetry(t, httpServer.URL)
	if first != second {
		t.Fatalf("retry response differs: first=%#v second=%#v", first, second)
	}
	if first.RunID != candidates[0].String() {
		t.Fatalf("replayed run ID = %s, want %s", first.RunID, candidates[0])
	}
	var count int
	if err := pool.QueryRow(
		t.Context(),
		"SELECT count(*) FROM forja.runs",
	).Scan(&count); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if count != 1 {
		t.Fatalf("run count = %d, want 1", count)
	}
}

func TestHTTPReadinessFailsWhenPostgresPoolCloses(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatalf("create contract registry: %v", err)
	}
	server, err := daemon.New(
		store,
		registry,
		postgresHTTPTestAuthenticator(t),
		store.Authority(),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("create daemon: %v", err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	assertReadyStatus(t, httpServer.URL, http.StatusOK)
	if _, err := pool.Exec(
		t.Context(),
		"UPDATE forja.schema_migrations SET checksum='readiness-drift' WHERE version=1",
	); err != nil {
		t.Fatalf("tamper migration ledger: %v", err)
	}
	assertReadyStatus(t, httpServer.URL, http.StatusServiceUnavailable)
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if _, err := pool.Exec(
		t.Context(),
		"UPDATE forja.schema_migrations SET checksum=$1 WHERE version=1",
		migrations[0].checksum,
	); err != nil {
		t.Fatalf("restore migration ledger: %v", err)
	}
	assertReadyStatus(t, httpServer.URL, http.StatusOK)
	pool.Close()
	assertReadyStatus(t, httpServer.URL, http.StatusServiceUnavailable)
}

func assertReadyStatus(t *testing.T, endpoint string, want int) {
	t.Helper()
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		endpoint+"/readyz",
		nil,
	)
	if err != nil {
		t.Fatalf("create readiness request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("call readiness endpoint: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("readiness status = %d, want %d body=%s", response.StatusCode, want, data)
	}
}

func postCreateRetry(t *testing.T, endpoint string) contracts.Run {
	t.Helper()
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		endpoint+"/v1/runs",
		bytes.NewBufferString(`{"objective":"Replay an HTTP create command"}`),
	)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+postgresHTTPTestBearerToken)
	request.Header.Set("Idempotency-Key", "http-retry-0001")
	request.Header.Set("Forja-Correlation-ID", "http-retry-0001")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("create status = %d body=%s", response.StatusCode, data)
	}
	var run contracts.Run
	if err := json.NewDecoder(response.Body).Decode(&run); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return run
}
