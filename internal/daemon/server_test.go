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
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

const fixedRunID = identity.RunID("run_00010203-0405-4607-8809-0a0b0c0d0e0f")

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
		func() (identity.RunID, error) { return fixedRunID, nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	return server
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
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	daemon.Handler().ServeHTTP(response, request)
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
	request, err := http.NewRequest(method, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
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
