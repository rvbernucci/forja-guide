package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/clock"
	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

const fixedRunID = identity.RunID("run_00010203-0405-4607-8809-0a0b0c0d0e0f")

const testBearerToken = "forja-test-bearer-token-00000001"

var testAuthority = control.Authority{
	TenantID:     control.LocalTenantID,
	RepositoryID: control.LocalRepositoryID,
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(
		runstate.NewStore(clock.Fixed{
			Time: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
		}),
		registry,
		newTestAuthenticator(t, testAuthority, control.AllPermissions...),
		testAuthority,
		func() (identity.RunID, error) { return fixedRunID, nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func newTestAuthenticator(
	t *testing.T,
	authority control.Authority,
	permissions ...control.Permission,
) Authenticator {
	t.Helper()
	principal, err := control.NewScopedPrincipal(
		"human",
		"daemon-test",
		authority.TenantID,
		authority.RepositoryID,
		permissions...,
	)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewStaticBearerAuthenticator(testBearerToken, principal)
	if err != nil {
		t.Fatal(err)
	}
	return authenticator
}

func TestHTTPMapsAuthenticationAndAuthorizationErrors(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "unauthenticated", err: fault.New(fault.CodeUnauthenticated, "test", "authenticate"), want: http.StatusUnauthorized},
		{name: "permission denied", err: fault.New(fault.CodePermissionDenied, "test", "authorize"), want: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			server.writeError(recorder, test.err)
			if recorder.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.want, recorder.Body.String())
			}
		})
	}
}

func TestHTTPRoutesRequireAuthenticationBeforeParsing(t *testing.T) {
	t.Parallel()
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	store := runstate.NewStore(nil)
	server, err := New(
		store,
		registry,
		newTestAuthenticator(t, testAuthority, control.AllPermissions...),
		testAuthority,
		func() (identity.RunID, error) { return fixedRunID, nil },
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/runs",
		bytes.NewBufferString(`{"objective":"must not persist"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing bearer challenge")
	}
	if _, err := store.Get(fixedRunID); !fault.IsCode(err, fault.CodeNotFound) {
		t.Fatalf("unauthenticated request changed persistence: %v", err)
	}
}

func TestHTTPAuthenticatesEntireV1NamespaceBeforeRouting(t *testing.T) {
	t.Parallel()
	handler := newTestServer(t).Handler()
	for _, test := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/v1/runs"},
		{method: http.MethodGet, path: "/v1/unknown"},
		{method: http.MethodGet, path: "/v1"},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d body=%s", test.method, test.path, response.Code, response.Body.String())
		}
	}

	authenticated := httptest.NewRequest(http.MethodGet, "/v1/unknown", nil)
	authenticated.Header.Set("Authorization", "Bearer "+testBearerToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticated)
	if response.Code != http.StatusNotFound {
		t.Fatalf("authenticated unknown route status=%d body=%s", response.Code, response.Body.String())
	}
}

type recordingRepository struct {
	runstate.Repository
	metadata runstate.CommandMetadata
}

func (r *recordingRepository) CreateRun(
	ctx context.Context,
	id identity.RunID,
	objective string,
	metadata runstate.CommandMetadata,
) (contracts.Run, error) {
	r.metadata = metadata
	return r.Repository.CreateRun(ctx, id, objective, metadata)
}

func TestHTTPAuditIdentityComesOnlyFromAuthenticatedPrincipal(t *testing.T) {
	t.Parallel()
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	store := &recordingRepository{Repository: runstate.NewStore(nil)}
	server, err := New(
		store,
		registry,
		newTestAuthenticator(t, testAuthority, control.AllPermissions...),
		testAuthority,
		func() (identity.RunID, error) { return fixedRunID, nil },
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/runs",
		bytes.NewBufferString(`{"objective":"Preserve authenticated audit identity"}`),
	)
	request.Header.Set("Authorization", "Bearer "+testBearerToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Forja-Actor-Type", "system")
	request.Header.Set("Forja-Actor-ID", "spoofed-administrator")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if store.metadata.ActorType != "human" || store.metadata.ActorID != "daemon-test" {
		t.Fatalf("persisted spoofed metadata: %#v", store.metadata)
	}
}

func TestNewRequiresHTTPTrustBoundary(t *testing.T) {
	t.Parallel()
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(runstate.NewStore(nil), registry, nil, testAuthority, nil, nil); err == nil {
		t.Fatal("daemon accepted a missing HTTP authenticator")
	}
	if _, err := New(
		runstate.NewStore(nil),
		registry,
		newTestAuthenticator(t, testAuthority, control.AllPermissions...),
		control.Authority{},
		nil,
		nil,
	); err == nil {
		t.Fatal("daemon accepted a missing repository authority")
	}
}

func TestHTTPRejectsMissingPermissionAndWrongScope(t *testing.T) {
	t.Parallel()
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		authority   control.Authority
		permissions []control.Permission
	}{
		{name: "missing write permission", authority: testAuthority, permissions: []control.Permission{control.PermissionRead}},
		{name: "wrong repository scope", authority: control.Authority{TenantID: control.LocalTenantID, RepositoryID: "other-repository"}, permissions: control.AllPermissions},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, err := New(
				runstate.NewStore(nil),
				registry,
				newTestAuthenticator(t, test.authority, test.permissions...),
				testAuthority,
				nil,
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewBufferString(`{"objective":"must not persist"}`))
			request.Header.Set("Authorization", "Bearer "+testBearerToken)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestHTTPCreateInspectAndTransition(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(newTestServer(t).Handler())
	defer server.Close()

	create := requestJSON(t, server.Client(), http.MethodPost, server.URL+"/v1/runs", `{
		"objective": "Create and inspect a synthetic run"
	}`)
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.StatusCode, readBody(t, create))
	}
	var run contracts.Run
	decodeBody(t, create, &run)
	if run.RunID != fixedRunID.String() || run.State != "draft" {
		t.Fatalf("unexpected created run: %#v", run)
	}

	get := requestJSON(t, server.Client(), http.MethodGet, server.URL+"/v1/runs/"+run.RunID, "")
	if get.StatusCode != http.StatusOK {
		t.Fatalf("get status=%d body=%s", get.StatusCode, readBody(t, get))
	}
	decodeBody(t, get, &run)

	transition := requestJSON(
		t,
		server.Client(),
		http.MethodPost,
		server.URL+"/v1/runs/"+run.RunID+"/transitions",
		`{"expected_version": 1, "target_state": "awaiting_approval"}`,
	)
	if transition.StatusCode != http.StatusOK {
		t.Fatalf("transition status=%d body=%s", transition.StatusCode, readBody(t, transition))
	}
	decodeBody(t, transition, &run)
	if run.Version != 2 || run.State != "awaiting_approval" {
		t.Fatalf("unexpected transitioned run: %#v", run)
	}
}

func TestHTTPFailsClosedForMalformedCommands(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(newTestServer(t).Handler())
	defer server.Close()
	cases := []struct {
		path string
		body string
		want int
	}{
		{"/v1/runs", `{"objective":"valid","unknown":true}`, http.StatusBadRequest},
		{"/v1/runs", `{`, http.StatusBadRequest},
		{"/v1/runs", `{"objective":"valid"} {}`, http.StatusBadRequest},
		{"/v1/runs/not-an-id/transitions", `{}`, http.StatusBadRequest},
		{
			"/v1/runs/" + fixedRunID.String() + "/transitions",
			`{"expected_version":1,"target_state":"completed"}`,
			http.StatusNotFound,
		},
	}
	for _, test := range cases {
		response := requestJSON(
			t,
			server.Client(),
			http.MethodPost,
			server.URL+test.path,
			test.body,
		)
		if response.StatusCode != test.want {
			t.Fatalf(
				"path=%s status=%d want=%d body=%s",
				test.path,
				response.StatusCode,
				test.want,
				readBody(t, response),
			)
		}
		response.Body.Close()
	}
}

func TestHTTPUnicodeObjectiveMatchesSchemaSemantics(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(newTestServer(t).Handler())
	defer server.Close()

	tooShort := requestJSON(
		t,
		server.Client(),
		http.MethodPost,
		server.URL+"/v1/runs",
		`{"objective":"😀😀"}`,
	)
	if tooShort.StatusCode != http.StatusBadRequest {
		t.Fatalf(
			"short Unicode status=%d body=%s",
			tooShort.StatusCode,
			readBody(t, tooShort),
		)
	}
	tooShort.Body.Close()

	valid := requestJSON(
		t,
		server.Client(),
		http.MethodPost,
		server.URL+"/v1/runs",
		`{"objective":"😀😀😀"}`,
	)
	if valid.StatusCode != http.StatusCreated {
		t.Fatalf("valid Unicode status=%d body=%s", valid.StatusCode, readBody(t, valid))
	}
	valid.Body.Close()
}

func TestReadinessCanFailClosed(t *testing.T) {
	t.Parallel()
	daemon := newTestServer(t)
	daemon.SetReady(false)
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/readyz",
		nil,
	)
	response := httptest.NewRecorder()
	daemon.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

type unavailableRepository struct {
	*runstate.Store
}

func (unavailableRepository) Ready(context.Context) error {
	return context.DeadlineExceeded
}

func TestReadinessChecksDurableDependency(t *testing.T) {
	t.Parallel()
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(
		unavailableRepository{Store: runstate.NewStore(nil)},
		registry,
		newTestAuthenticator(t, testAuthority, control.AllPermissions...),
		testAuthority,
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/readyz",
		nil,
	)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestListenAndServeGracefulShutdown(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- ListenAndServe(
			ctx,
			listener,
			http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusNoContent)
			}),
			time.Second,
			nil,
		)
	}()
	client := &http.Client{Timeout: time.Second}
	waitForServer(t, client, "http://"+listener.Addr().String())
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down")
	}
}

func requestJSON(
	t *testing.T,
	client *http.Client,
	method string,
	url string,
	body string,
) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(
		t.Context(),
		method,
		url,
		bytes.NewBufferString(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+testBearerToken)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeBody(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func waitForServer(t *testing.T, client *http.Client, endpoint string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(endpoint)
		if err == nil {
			response.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not become reachable")
}
