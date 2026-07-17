package daemon

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"unicode"

	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/fault"
)

const (
	minBearerTokenBytes = 32
	maxBearerTokenBytes = 4096
)

// Authenticator resolves one HTTP request to an authenticated control-plane principal.
type Authenticator interface {
	Authenticate(*http.Request) (control.Principal, error)
}

// StaticBearerAuthenticator maps one environment-provided bearer secret to a
// server-configured principal. The caller cannot choose its own audit identity.
type StaticBearerAuthenticator struct {
	token     []byte
	principal control.Principal
}

// NewStaticBearerAuthenticator creates a fail-closed local HTTP authenticator.
func NewStaticBearerAuthenticator(
	token string,
	principal control.Principal,
) (*StaticBearerAuthenticator, error) {
	if len(token) < minBearerTokenBytes || len(token) > maxBearerTokenBytes {
		return nil, fault.New(
			fault.CodeInvalidArgument,
			"daemon.NewStaticBearerAuthenticator",
			"bearer token length must be between 32 and 4096 bytes",
		)
	}
	if strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return nil, fault.New(
			fault.CodeInvalidArgument,
			"daemon.NewStaticBearerAuthenticator",
			"bearer token must not contain whitespace",
		)
	}
	if principal.ActorType == "" || principal.ActorID == "" ||
		principal.TenantID == "" || principal.RepositoryID == "" {
		return nil, fault.New(
			fault.CodeUnauthenticated,
			"daemon.NewStaticBearerAuthenticator",
			"authenticated principal is incomplete",
		)
	}
	return &StaticBearerAuthenticator{
		token:     []byte(token),
		principal: principal,
	}, nil
}

// Authenticate requires exactly one RFC 6750-style Authorization header.
func (a *StaticBearerAuthenticator) Authenticate(
	request *http.Request,
) (control.Principal, error) {
	values := request.Header.Values("Authorization")
	if len(values) != 1 {
		return control.Principal{}, unauthenticated()
	}
	fields := strings.Fields(values[0])
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return control.Principal{}, unauthenticated()
	}
	provided := []byte(fields[1])
	if len(provided) != len(a.token) || subtle.ConstantTimeCompare(provided, a.token) != 1 {
		return control.Principal{}, unauthenticated()
	}
	return a.principal, nil
}

func unauthenticated() error {
	return fault.New(
		fault.CodeUnauthenticated,
		"daemon.authenticate",
		"a valid bearer token is required",
	)
}
