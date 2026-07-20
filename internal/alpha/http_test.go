package alpha

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func alphaTestHandler(t *testing.T) http.Handler {
	t.Helper()
	service := NewService(Config{Accelerator: "AMD Radeon", SoftwareStack: "ROCm"})
	service.newID = func() (string, error) { return "research_test", nil }
	handler, err := NewHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestHandlerServesEmbeddedApplication(t *testing.T) {
	recorder := httptest.NewRecorder()
	alphaTestHandler(t).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Forja Alpha") {
		t.Fatalf("application response = %d %q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Security-Policy") == "" || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("security headers missing: %#v", recorder.Header())
	}
}

func TestHandlerExposesHonestBootstrapState(t *testing.T) {
	recorder := httptest.NewRecorder()
	alphaTestHandler(t).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d", recorder.Code)
	}
	var response Bootstrap
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Runtime.CoreInferenceReady || !response.Runtime.LocalOnly || len(response.Universe) != 7 {
		t.Fatalf("unexpected bootstrap response: %#v", response)
	}
}

func TestHandlerCreatesAndReadsResearchPlan(t *testing.T) {
	handler := alphaTestHandler(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/research",
		strings.NewReader(`{"prompt":"Compare the latest filings and rate sensitivity."}`),
	))
	if recorder.Code != http.StatusCreated || recorder.Header().Get("Location") != "/api/v1/research/research_test" {
		t.Fatalf("create response = %d headers=%#v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/research/research_test", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Estimate factor sensitivity") {
		t.Fatalf("read response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestHandlerRejectsUnknownAndOversizedInput(t *testing.T) {
	handler := alphaTestHandler(t)
	for name, testCase := range map[string]struct {
		body   string
		status int
	}{
		"unknown":   {body: `{"prompt":"hello","answer":"forbidden"}`, status: http.StatusBadRequest},
		"oversized": {body: `{"prompt":"` + strings.Repeat("x", maxRequestBody) + `"}`, status: http.StatusRequestEntityTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/research", strings.NewReader(testCase.body)))
			if recorder.Code != testCase.status {
				t.Fatalf("status = %d, want %d", recorder.Code, testCase.status)
			}
		})
	}
}
