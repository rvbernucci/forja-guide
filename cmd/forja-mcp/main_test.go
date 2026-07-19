package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/rvbernucci/forja-guide/internal/control"
)

func TestMCPMetricsListenDefaultsToLoopbackAndRejectsRemoteBind(t *testing.T) {
	t.Parallel()
	listen, err := mcpMetricsListen(func(string) (string, bool) { return "", false })
	if err != nil || listen != defaultMCPMetricsListen {
		t.Fatalf("default metrics listen = %q, %v", listen, err)
	}
	if _, err := mcpMetricsListen(func(string) (string, bool) {
		return "0.0.0.0:9464", true
	}); err == nil {
		t.Fatal("remote MCP metrics bind was accepted")
	}
}

func TestMCPMetricsEndpointServesAndShutsDown(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	endpoint, err := startMCPMetricsEndpoint(
		"127.0.0.1:0",
		http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer, "forja_test_metric 1\n")
		}),
		logger,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { endpoint.shutdown(logger) })
	response, err := http.Get("http://" + endpoint.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "forja_test_metric 1") {
		t.Fatalf("unexpected metrics body %q", body)
	}
}

func TestStdioPermissionsSeparateProposalFromDecisionAuthority(t *testing.T) {
	tests := []struct {
		actorType string
		want      []control.Permission
	}{
		{
			actorType: "agent",
			want: []control.Permission{
				control.PermissionPlan,
				control.PermissionRead,
				control.PermissionSubmit,
				control.PermissionCancel,
			},
		},
		{actorType: "worker", want: []control.Permission{control.PermissionRead}},
		{actorType: "human", want: control.AllPermissions},
		{actorType: "system", want: control.AllPermissions},
	}
	for _, test := range tests {
		t.Run(test.actorType, func(t *testing.T) {
			got := stdioPermissions(test.actorType)
			if !slices.Equal(got, test.want) {
				t.Fatalf("stdioPermissions(%q) = %v, want %v", test.actorType, got, test.want)
			}
		})
	}
}

func TestStdioPermissionsNeverLetAgentAuthorizeItsOwnExecution(t *testing.T) {
	permissions := stdioPermissions("agent")
	if slices.Contains(permissions, control.PermissionDecide) {
		t.Fatal("agent received decision authority")
	}
	if slices.Contains(permissions, control.PermissionResume) {
		t.Fatal("agent received execution restart authority")
	}
}

func TestStdioPermissionsDoNotAliasAllPermissions(t *testing.T) {
	permissions := stdioPermissions("human")
	permissions[0] = control.PermissionRead
	if control.AllPermissions[0] != control.PermissionPlan {
		t.Fatal("stdio permission selection mutated the canonical permission list")
	}
}

func TestNormalizeServerErrorTreatsSignalCancellationAsCleanShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := normalizeServerError(ctx, context.Canceled); err != nil {
		t.Fatalf("normalize cancellation: %v", err)
	}
}

func TestNormalizeServerErrorPreservesUnexpectedFailure(t *testing.T) {
	want := errors.New("transport failed")
	if got := normalizeServerError(context.Background(), want); !errors.Is(got, want) {
		t.Fatalf("normalizeServerError() = %v, want %v", got, want)
	}
}
