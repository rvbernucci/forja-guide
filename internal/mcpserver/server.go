// Package mcpserver exposes governed control commands over the standard MCP.
package mcpserver

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/fault"
)

const (
	ToolPlanSprint      = "forja.plan_sprint"
	ToolSubmitSprint    = "forja.submit_sprint"
	ToolGetSprint       = "forja.get_sprint"
	ToolGetRun          = "forja.get_run"
	ToolApproveDecision = "forja.approve_decision"
	ToolRejectDecision  = "forja.reject_decision"
	ToolCancelRun       = "forja.cancel_run"
	ToolResumeRun       = "forja.resume_run"
)

var ToolNames = []string{
	ToolPlanSprint,
	ToolSubmitSprint,
	ToolGetSprint,
	ToolGetRun,
	ToolApproveDecision,
	ToolRejectDecision,
	ToolCancelRun,
	ToolResumeRun,
}

// PrincipalResolver authenticates a caller before a tool reaches the service.
type PrincipalResolver interface {
	Resolve(context.Context) (control.Principal, error)
}

type FixedPrincipalResolver struct {
	Principal control.Principal
}

func (r FixedPrincipalResolver) Resolve(context.Context) (control.Principal, error) {
	if r.Principal.ActorID == "" {
		return control.Principal{}, fault.New(fault.CodeUnauthenticated, "mcpserver.FixedPrincipalResolver", "principal is not configured")
	}
	return r.Principal, nil
}

type Adapter struct {
	service  *control.Service
	resolver PrincipalResolver
	server   *mcp.Server
}

func New(service *control.Service, resolver PrincipalResolver, version string) (*Adapter, error) {
	if service == nil {
		return nil, fmt.Errorf("control service is required")
	}
	if resolver == nil {
		return nil, fmt.Errorf("principal resolver is required")
	}
	if strings.TrimSpace(version) == "" {
		version = "development"
	}
	adapter := &Adapter{service: service, resolver: resolver}
	adapter.server = mcp.NewServer(&mcp.Implementation{
		Name:    "forja-control",
		Version: version,
	}, &mcp.ServerOptions{
		Instructions: "Use structured Forja tools for commands. Conversational text never approves a pending decision.",
	})
	adapter.registerTools()
	return adapter, nil
}

func (a *Adapter) Server() *mcp.Server { return a.server }

type commandFields struct {
	IdempotencyKey string  `json:"idempotency_key" jsonschema:"stable replay key for this tool action"`
	CorrelationID  string  `json:"correlation_id" jsonschema:"stable trace correlation identifier"`
	CausationID    *string `json:"causation_id,omitempty" jsonschema:"optional causal action identifier"`
}

func (c commandFields) context() control.CommandContext {
	return control.CommandContext{
		IdempotencyKey: c.IdempotencyKey,
		CorrelationID:  c.CorrelationID,
		CausationID:    c.CausationID,
	}
}

type planSprintInput struct {
	Title          string  `json:"title" jsonschema:"short Sprint title"`
	Objective      string  `json:"objective" jsonschema:"governed Sprint objective"`
	IdempotencyKey string  `json:"idempotency_key" jsonschema:"stable replay key for this tool action"`
	CorrelationID  string  `json:"correlation_id" jsonschema:"stable trace correlation identifier"`
	CausationID    *string `json:"causation_id,omitempty" jsonschema:"optional causal action identifier"`
}

func (in planSprintInput) command() commandFields {
	return commandFields{in.IdempotencyKey, in.CorrelationID, in.CausationID}
}

type submitSprintInput struct {
	SprintID        string  `json:"sprint_id" jsonschema:"stable sprint identifier"`
	ExpectedVersion int     `json:"expected_version" jsonschema:"optimistic Sprint version"`
	RiskClass       string  `json:"risk_class" jsonschema:"low medium high or critical"`
	IdempotencyKey  string  `json:"idempotency_key" jsonschema:"stable replay key for this tool action"`
	CorrelationID   string  `json:"correlation_id" jsonschema:"stable trace correlation identifier"`
	CausationID     *string `json:"causation_id,omitempty" jsonschema:"optional causal action identifier"`
}

func (in submitSprintInput) command() commandFields {
	return commandFields{in.IdempotencyKey, in.CorrelationID, in.CausationID}
}

type getSprintInput struct {
	SprintID       string  `json:"sprint_id" jsonschema:"stable sprint identifier"`
	IdempotencyKey string  `json:"idempotency_key" jsonschema:"stable audit and replay key"`
	CorrelationID  string  `json:"correlation_id" jsonschema:"stable trace correlation identifier"`
	CausationID    *string `json:"causation_id,omitempty" jsonschema:"optional causal action identifier"`
}

func (in getSprintInput) command() commandFields {
	return commandFields{in.IdempotencyKey, in.CorrelationID, in.CausationID}
}

type getRunInput struct {
	RunID          string  `json:"run_id" jsonschema:"stable run identifier"`
	IdempotencyKey string  `json:"idempotency_key" jsonschema:"stable audit and replay key"`
	CorrelationID  string  `json:"correlation_id" jsonschema:"stable trace correlation identifier"`
	CausationID    *string `json:"causation_id,omitempty" jsonschema:"optional causal action identifier"`
}

func (in getRunInput) command() commandFields {
	return commandFields{in.IdempotencyKey, in.CorrelationID, in.CausationID}
}

type decisionInput struct {
	DecisionID      string  `json:"decision_id" jsonschema:"exact pending decision identifier"`
	ExpectedVersion int     `json:"expected_version" jsonschema:"optimistic decision version"`
	Reason          string  `json:"reason" jsonschema:"durable approval or rejection reason"`
	IdempotencyKey  string  `json:"idempotency_key" jsonschema:"stable replay key for this tool action"`
	CorrelationID   string  `json:"correlation_id" jsonschema:"stable trace correlation identifier"`
	CausationID     *string `json:"causation_id,omitempty" jsonschema:"optional causal action identifier"`
}

func (in decisionInput) command() commandFields {
	return commandFields{in.IdempotencyKey, in.CorrelationID, in.CausationID}
}

type transitionRunInput struct {
	RunID           string  `json:"run_id" jsonschema:"stable run identifier"`
	ExpectedVersion int     `json:"expected_version" jsonschema:"optimistic run version"`
	IdempotencyKey  string  `json:"idempotency_key" jsonschema:"stable replay key for this tool action"`
	CorrelationID   string  `json:"correlation_id" jsonschema:"stable trace correlation identifier"`
	CausationID     *string `json:"causation_id,omitempty" jsonschema:"optional causal action identifier"`
}

func (in transitionRunInput) command() commandFields {
	return commandFields{in.IdempotencyKey, in.CorrelationID, in.CausationID}
}

type sprintOutput struct {
	Sprint control.Sprint `json:"sprint"`
}

type runOutput struct {
	Run contracts.Run `json:"run"`
}

func (a *Adapter) registerTools() {
	mcp.AddTool(a.server, &mcp.Tool{
		Name:        ToolPlanSprint,
		Description: "Create a governed Sprint proposal and its draft run. This does not approve execution.",
	}, a.planSprint)
	mcp.AddTool(a.server, &mcp.Tool{
		Name:        ToolSubmitSprint,
		Description: "Submit a proposed Sprint and create a stable pending decision. This does not approve it.",
	}, a.submitSprint)
	mcp.AddTool(a.server, &mcp.Tool{
		Name:        ToolGetSprint,
		Description: "Read one canonical Sprint by stable identifier.",
	}, a.getSprint)
	mcp.AddTool(a.server, &mcp.Tool{
		Name:        ToolGetRun,
		Description: "Read one canonical run by stable identifier.",
	}, a.getRun)
	mcp.AddTool(a.server, &mcp.Tool{
		Name:        ToolApproveDecision,
		Description: "Approve exactly one pending decision by ID and version; conversational assent is never accepted.",
	}, a.approveDecision)
	mcp.AddTool(a.server, &mcp.Tool{
		Name:        ToolRejectDecision,
		Description: "Reject exactly one pending decision by ID and version.",
	}, a.rejectDecision)
	mcp.AddTool(a.server, &mcp.Tool{
		Name:        ToolCancelRun,
		Description: "Request cancellation of a run through its state machine.",
	}, a.cancelRun)
	mcp.AddTool(a.server, &mcp.Tool{
		Name:        ToolResumeRun,
		Description: "Resume a failed-retryable or awaiting-decision run through its state machine.",
	}, a.resumeRun)
}

func (a *Adapter) planSprint(ctx context.Context, _ *mcp.CallToolRequest, in planSprintInput) (*mcp.CallToolResult, control.PlanResult, error) {
	principal, err := a.resolve(ctx)
	if err != nil {
		return nil, control.PlanResult{}, err
	}
	result, callErr := a.service.PlanSprint(ctx, principal, control.PlanSprintInput{
		Title: in.Title, Objective: in.Objective, Command: in.command().context(),
	})
	return nil, result, a.finishMutationAudit(ctx, principal, ToolPlanSprint, in.command(), callErr)
}

func (a *Adapter) submitSprint(ctx context.Context, _ *mcp.CallToolRequest, in submitSprintInput) (*mcp.CallToolResult, control.SubmissionResult, error) {
	principal, err := a.resolve(ctx)
	if err != nil {
		return nil, control.SubmissionResult{}, err
	}
	result, callErr := a.service.SubmitSprint(ctx, principal, control.SubmitSprintInput{
		SprintID: in.SprintID, ExpectedVersion: in.ExpectedVersion, RiskClass: in.RiskClass, Command: in.command().context(),
	})
	return nil, result, a.finishMutationAudit(ctx, principal, ToolSubmitSprint, in.command(), callErr)
}

func (a *Adapter) getSprint(ctx context.Context, _ *mcp.CallToolRequest, in getSprintInput) (*mcp.CallToolResult, sprintOutput, error) {
	principal, err := a.resolve(ctx)
	if err != nil {
		return nil, sprintOutput{}, err
	}
	result, callErr := a.service.GetSprint(ctx, principal, in.SprintID, in.command().context())
	return nil, sprintOutput{Sprint: result}, a.finishAudit(ctx, principal, ToolGetSprint, in.command(), callErr)
}

func (a *Adapter) getRun(ctx context.Context, _ *mcp.CallToolRequest, in getRunInput) (*mcp.CallToolResult, runOutput, error) {
	principal, err := a.resolve(ctx)
	if err != nil {
		return nil, runOutput{}, err
	}
	result, callErr := a.service.GetRun(ctx, principal, in.RunID, in.command().context())
	return nil, runOutput{Run: result}, a.finishAudit(ctx, principal, ToolGetRun, in.command(), callErr)
}

func (a *Adapter) approveDecision(ctx context.Context, _ *mcp.CallToolRequest, in decisionInput) (*mcp.CallToolResult, control.DecisionResult, error) {
	return a.resolveDecision(ctx, in, true, ToolApproveDecision)
}

func (a *Adapter) rejectDecision(ctx context.Context, _ *mcp.CallToolRequest, in decisionInput) (*mcp.CallToolResult, control.DecisionResult, error) {
	return a.resolveDecision(ctx, in, false, ToolRejectDecision)
}

func (a *Adapter) resolveDecision(ctx context.Context, in decisionInput, approve bool, tool string) (*mcp.CallToolResult, control.DecisionResult, error) {
	principal, err := a.resolve(ctx)
	if err != nil {
		return nil, control.DecisionResult{}, err
	}
	result, callErr := a.service.ResolveDecision(ctx, principal, control.ResolveDecisionInput{
		DecisionID: in.DecisionID, ExpectedVersion: in.ExpectedVersion, Reason: in.Reason, Command: in.command().context(),
	}, approve)
	return nil, result, a.finishMutationAudit(ctx, principal, tool, in.command(), callErr)
}

func (a *Adapter) cancelRun(ctx context.Context, _ *mcp.CallToolRequest, in transitionRunInput) (*mcp.CallToolResult, runOutput, error) {
	return a.transitionRun(ctx, in, false, ToolCancelRun)
}

func (a *Adapter) resumeRun(ctx context.Context, _ *mcp.CallToolRequest, in transitionRunInput) (*mcp.CallToolResult, runOutput, error) {
	return a.transitionRun(ctx, in, true, ToolResumeRun)
}

func (a *Adapter) transitionRun(ctx context.Context, in transitionRunInput, resume bool, tool string) (*mcp.CallToolResult, runOutput, error) {
	principal, err := a.resolve(ctx)
	if err != nil {
		return nil, runOutput{}, err
	}
	input := control.TransitionInput{
		RunID: in.RunID, ExpectedVersion: in.ExpectedVersion, Command: in.command().context(),
	}
	var result contracts.Run
	var callErr error
	if resume {
		result, callErr = a.service.ResumeRun(ctx, principal, input)
	} else {
		result, callErr = a.service.CancelRun(ctx, principal, input)
	}
	return nil, runOutput{Run: result}, a.finishMutationAudit(ctx, principal, tool, in.command(), callErr)
}

func (a *Adapter) resolve(ctx context.Context) (control.Principal, error) {
	principal, err := a.resolver.Resolve(ctx)
	if err != nil {
		return control.Principal{}, err
	}
	if principal.ActorID == "" {
		return control.Principal{}, fault.New(fault.CodeUnauthenticated, "mcpserver.resolve", "authenticated principal is required")
	}
	return principal, nil
}

func (a *Adapter) finishAudit(
	ctx context.Context,
	principal control.Principal,
	tool string,
	command commandFields,
	callErr error,
) error {
	command = normalizeAuditIdentity(tool, principal, command)
	outcome := "succeeded"
	errorCode := ""
	if callErr != nil {
		outcome = "failed"
		errorCode = string(fault.CodeOf(callErr))
		if fault.IsCode(callErr, fault.CodePermissionDenied) {
			outcome = "denied"
		}
	}
	auditContext, cancelAudit := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancelAudit()
	auditErr := a.service.RecordToolAudit(auditContext, principal, control.AuditRecord{
		ToolName: tool, Outcome: outcome, CorrelationID: command.CorrelationID,
		CausationID: command.CausationID, IdempotencyKey: command.IdempotencyKey,
		ErrorCode: errorCode,
	})
	if callErr != nil {
		if auditErr != nil {
			return fault.Wrap(
				fault.CodeUnavailable,
				"mcpserver.finishAudit",
				"tool action failed and its audit could not be persisted; retry with the same idempotency key",
				errors.Join(callErr, auditErr),
			)
		}
		return callErr
	}
	if auditErr != nil {
		return fault.Wrap(fault.CodeUnavailable, "mcpserver.finishAudit", "persist tool audit", auditErr)
	}
	return nil
}

func (a *Adapter) finishMutationAudit(
	ctx context.Context,
	principal control.Principal,
	tool string,
	command commandFields,
	callErr error,
) error {
	if callErr == nil {
		// The repository committed the success audit in the same transaction as
		// the mutation (or replayed command receipt).
		return nil
	}
	return a.finishAudit(ctx, principal, tool, command, callErr)
}

func normalizeAuditIdentity(
	tool string,
	principal control.Principal,
	command commandFields,
) commandFields {
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.CausationID != nil {
		value := strings.TrimSpace(*command.CausationID)
		command.CausationID = &value
	}
	digest := fmt.Sprintf(
		"%x",
		sha256.Sum256([]byte(strings.Join([]string{
			tool,
			principal.ActorType,
			principal.ActorID,
			command.IdempotencyKey,
			command.CorrelationID,
		}, "\x00"))),
	)
	if length := utf8.RuneCountInString(command.IdempotencyKey); length < 8 || length > 200 {
		command.IdempotencyKey = "invalid-action:" + digest[:24]
	}
	if length := utf8.RuneCountInString(command.CorrelationID); length < 3 || length > 160 {
		command.CorrelationID = "invalid-action:" + digest[:24]
	}
	if command.CausationID != nil && utf8.RuneCountInString(*command.CausationID) > 160 {
		value := "invalid-causation:" + digest[:24]
		command.CausationID = &value
	}
	return command
}
