// Package control implements Forja's governed command application layer.
package control

import (
	"context"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

type SprintStatus string

const (
	SprintProposed         SprintStatus = "proposed"
	SprintAwaitingApproval SprintStatus = "awaiting_approval"
	SprintApproved         SprintStatus = "approved"
	SprintRejected         SprintStatus = "rejected"
	SprintCancelling       SprintStatus = "cancelling"
)

type DecisionStatus string

const (
	DecisionPending  DecisionStatus = "pending"
	DecisionApproved DecisionStatus = "approved"
	DecisionRejected DecisionStatus = "rejected"
)

const DecisionActionSubmitSprint = "submit_sprint"

const (
	LocalTenantID     = "00000000-0000-4000-8000-000000000001"
	LocalRepositoryID = "00000000-0000-4000-8000-000000000002"
)

type Authority struct {
	TenantID     string
	RepositoryID string
}

type Sprint = contracts.Sprint

type Decision = contracts.Decision

type PlanResult struct {
	Sprint Sprint        `json:"sprint"`
	Run    contracts.Run `json:"run"`
}

type SubmissionResult struct {
	Sprint   Sprint        `json:"sprint"`
	Decision Decision      `json:"decision"`
	Run      contracts.Run `json:"run"`
}

type DecisionResult struct {
	Sprint   Sprint        `json:"sprint"`
	Decision Decision      `json:"decision"`
	Run      contracts.Run `json:"run"`
}

type PlanCommand struct {
	SprintID  identity.SprintID
	RunID     identity.RunID
	Title     string
	Objective string
}

type SubmitCommand struct {
	SprintID        identity.SprintID
	DecisionID      identity.DecisionID
	ExpectedVersion int
	RiskClass       string
}

type DecideCommand struct {
	DecisionID      identity.DecisionID
	ExpectedVersion int
	Approve         bool
	Reason          string
}

type AuditRecord struct {
	ToolName       string    `json:"tool_name"`
	Outcome        string    `json:"outcome"`
	ActorType      string    `json:"actor_type"`
	ActorID        string    `json:"actor_id"`
	CorrelationID  string    `json:"correlation_id"`
	CausationID    *string   `json:"causation_id,omitempty"`
	IdempotencyKey string    `json:"idempotency_key"`
	CommandScope   string    `json:"command_scope,omitempty"`
	ErrorCode      string    `json:"error_code,omitempty"`
	Replay         bool      `json:"replay"`
	OccurredAt     time.Time `json:"occurred_at"`
}

// Repository is the only persistence boundary available to governed commands.
type Repository interface {
	Authority() Authority
	PlanSprint(context.Context, PlanCommand, runstate.CommandMetadata) (PlanResult, error)
	GetSprint(context.Context, identity.SprintID) (Sprint, error)
	SubmitSprint(context.Context, SubmitCommand, runstate.CommandMetadata) (SubmissionResult, error)
	ResolveDecision(context.Context, DecideCommand, runstate.CommandMetadata) (DecisionResult, error)
	GetRun(context.Context, identity.RunID) (contracts.Run, error)
	TransitionRun(context.Context, identity.RunID, int, runstate.State, runstate.CommandMetadata) (contracts.Run, error)
	ResumeRun(context.Context, identity.RunID, int, runstate.CommandMetadata) (contracts.Run, error)
	RecordToolAudit(context.Context, AuditRecord) error
}
