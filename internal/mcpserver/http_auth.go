package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/fault"
)

type principalContextKey struct{}

// BearerVerifier is the mandatory authentication boundary for future remote
// Streamable HTTP deployment. The repository ships no permissive verifier.
type BearerVerifier interface {
	VerifyBearer(context.Context, string) (control.Principal, error)
}

type ContextPrincipalResolver struct{}

func (ContextPrincipalResolver) Resolve(ctx context.Context) (control.Principal, error) {
	principal, ok := ctx.Value(principalContextKey{}).(control.Principal)
	if !ok || principal.ActorID == "" {
		return control.Principal{}, fault.New(fault.CodeUnauthenticated, "mcpserver.ContextPrincipalResolver", "HTTP principal is missing")
	}
	return principal, nil
}

// AuthenticatedHTTPBoundary rejects unauthenticated requests before they can
// enter the MCP transport or reach persistence.
func AuthenticatedHTTPBoundary(next http.Handler, verifier BearerVerifier) (http.Handler, error) {
	if next == nil {
		return nil, fault.New(fault.CodeInvalidArgument, "mcpserver.AuthenticatedHTTPBoundary", "next handler is required")
	}
	if verifier == nil {
		return nil, fault.New(fault.CodeInvalidArgument, "mcpserver.AuthenticatedHTTPBoundary", "bearer verifier is required")
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		scheme, token, ok := strings.Cut(strings.TrimSpace(request.Header.Get("Authorization")), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
			writeAuthError(writer, http.StatusUnauthorized, "bearer token is required")
			return
		}
		principal, err := verifier.VerifyBearer(request.Context(), strings.TrimSpace(token))
		if err != nil {
			writeAuthError(writer, http.StatusUnauthorized, "bearer token was rejected")
			return
		}
		ctx := context.WithValue(request.Context(), principalContextKey{}, principal)
		next.ServeHTTP(writer, request.WithContext(ctx))
	}), nil
}

func writeAuthError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("WWW-Authenticate", "Bearer")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"error": map[string]string{"code": string(fault.CodeUnauthenticated), "message": message},
	})
}
