package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/fault"
)

const bootstrapTestToken = "forja-bootstrap-test-bearer-000001"

func TestHTTPTrustBoundaryFailsClosed(t *testing.T) {
	t.Parallel()
	for _, environment := range []map[string]string{
		{},
		{"FORJA_HTTP_ACTOR_ID": "operator"},
		{"FORJA_HTTP_ACTOR_ID": "operator", "FORJA_HTTP_BEARER_TOKEN": "short"},
		{"FORJA_HTTP_ACTOR_ID": "operator", "FORJA_HTTP_ACTOR_TYPE": "administrator", "FORJA_HTTP_BEARER_TOKEN": bootstrapTestToken},
	} {
		lookup := func(key string) string { return environment[key] }
		if _, _, err := httpTrustBoundary(lookup); err == nil {
			t.Fatalf("unsafe environment succeeded: %#v", environment)
		}
	}
}

func TestHTTPTrustBoundaryBuildsAuthenticatedOperator(t *testing.T) {
	t.Parallel()
	environment := map[string]string{
		"FORJA_HTTP_ACTOR_ID":     "local-operator",
		"FORJA_HTTP_BEARER_TOKEN": bootstrapTestToken,
	}
	authenticator, authority, err := httpTrustBoundary(
		func(key string) string { return environment[key] },
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/runs/example", nil)
	request.Header.Set("Authorization", "Bearer "+bootstrapTestToken)
	principal, err := authenticator.Authenticate(request)
	if err != nil {
		t.Fatal(err)
	}
	if principal.ActorType != "human" || principal.ActorID != "local-operator" {
		t.Fatalf("principal=%#v", principal)
	}
	if principal.TenantID != authority.TenantID ||
		principal.RepositoryID != authority.RepositoryID {
		t.Fatalf("principal authority=%#v server authority=%#v", principal, authority)
	}
	if _, ok := principal.Permissions[control.PermissionLegacyRunWrite]; !ok {
		t.Fatal("operator lacks legacy HTTP write permission")
	}

	badRequest := httptest.NewRequest(http.MethodGet, "/v1/runs/example", nil)
	badRequest.Header.Set("Authorization", "Bearer invalid-invalid-invalid-invalid")
	if _, err := authenticator.Authenticate(badRequest); !fault.IsCode(err, fault.CodeUnauthenticated) {
		t.Fatalf("invalid credential error=%v", err)
	}
}
