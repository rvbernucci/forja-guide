package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/rvbernucci/forja-guide/internal/control"
)

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
