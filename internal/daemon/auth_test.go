package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/fault"
)

func TestStaticBearerAuthenticatorFailsClosed(t *testing.T) {
	t.Parallel()
	principal, err := control.NewPrincipal("human", "auth-test", control.PermissionRead)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewStaticBearerAuthenticator(testBearerToken, principal)
	if err != nil {
		t.Fatal(err)
	}
	for _, headers := range [][]string{
		nil,
		{"Basic credentials"},
		{"Bearer wrong-token-wrong-token-wrong"},
		{"Bearer " + testBearerToken, "Bearer " + testBearerToken},
	} {
		request := httptest.NewRequest(http.MethodGet, "/v1/runs/example", nil)
		for _, header := range headers {
			request.Header.Add("Authorization", header)
		}
		if _, err := authenticator.Authenticate(request); !fault.IsCode(err, fault.CodeUnauthenticated) {
			t.Fatalf("headers=%v error=%v", headers, err)
		}
	}
}

func TestStaticBearerAuthenticatorReturnsConfiguredPrincipal(t *testing.T) {
	t.Parallel()
	principal, err := control.NewPrincipal("human", "auth-test", control.PermissionRead)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewStaticBearerAuthenticator(testBearerToken, principal)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/runs/example", nil)
	request.Header.Set("Authorization", "bEaReR "+testBearerToken)
	got, err := authenticator.Authenticate(request)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActorID != principal.ActorID || got.ActorType != principal.ActorType {
		t.Fatalf("principal=%#v want=%#v", got, principal)
	}
}

func TestStaticBearerAuthenticatorRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()
	principal, err := control.NewPrincipal("human", "auth-test", control.PermissionRead)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "short", "forja token with whitespace 000000000000"} {
		if _, err := NewStaticBearerAuthenticator(token, principal); err == nil {
			t.Fatalf("token configuration %q succeeded", token)
		}
	}
}
