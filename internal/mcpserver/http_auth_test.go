package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvbernucci/forja-guide/internal/control"
)

type testVerifier struct{ principal control.Principal }

func (v testVerifier) VerifyBearer(_ context.Context, token string) (control.Principal, error) {
	if token != "valid-token" {
		return control.Principal{}, context.Canceled
	}
	return v.principal, nil
}

func TestAuthenticatedHTTPBoundaryFailsClosed(t *testing.T) {
	t.Parallel()
	principal, err := control.NewPrincipal("agent", "remote-co-architect", control.AllPermissions...)
	if err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		resolved, resolveErr := (ContextPrincipalResolver{}).Resolve(request.Context())
		if resolveErr != nil || resolved.ActorID != principal.ActorID {
			t.Fatalf("principal was not propagated: %#v %v", resolved, resolveErr)
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	handler, err := AuthenticatedHTTPBoundary(next, testVerifier{principal: principal})
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	authorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("Authorization", "Bearer valid-token")
	handler.ServeHTTP(authorized, request)
	if authorized.Code != http.StatusNoContent {
		t.Fatalf("authorized status = %d", authorized.Code)
	}
	if _, err := AuthenticatedHTTPBoundary(next, nil); err == nil {
		t.Fatal("nil verifier created a permissive HTTP boundary")
	}
}
