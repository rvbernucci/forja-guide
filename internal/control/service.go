package control

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

type Permission string

const (
	PermissionPlan           Permission = "sprint:plan"
	PermissionRead           Permission = "control:read"
	PermissionSubmit         Permission = "sprint:submit"
	PermissionDecide         Permission = "decision:decide"
	PermissionCancel         Permission = "run:cancel"
	PermissionResume         Permission = "run:resume"
	PermissionLegacyRunWrite Permission = "legacy_run:write"
	PermissionMemoryPromote  Permission = "memory:promote"

	auditToolPlanSprint      = "forja.plan_sprint"
	auditToolSubmitSprint    = "forja.submit_sprint"
	auditToolApproveDecision = "forja.approve_decision"
	auditToolRejectDecision  = "forja.reject_decision"
	auditToolCancelRun       = "forja.cancel_run"
	auditToolResumeRun       = "forja.resume_run"
)

var AllPermissions = []Permission{
	PermissionPlan,
	PermissionRead,
	PermissionSubmit,
	PermissionDecide,
	PermissionCancel,
	PermissionResume,
	PermissionLegacyRunWrite,
}

// Principal is an authenticated control-plane identity.
type Principal struct {
	ActorType    string
	ActorID      string
	TenantID     string
	RepositoryID string
	Permissions  map[Permission]struct{}
}

func NewPrincipal(actorType, actorID string, permissions ...Permission) (Principal, error) {
	return NewScopedPrincipal(
		actorType,
		actorID,
		LocalTenantID,
		LocalRepositoryID,
		permissions...,
	)
}

func NewScopedPrincipal(
	actorType,
	actorID,
	tenantID,
	repositoryID string,
	permissions ...Permission,
) (Principal, error) {
	principal := Principal{
		ActorType:    strings.TrimSpace(actorType),
		ActorID:      strings.TrimSpace(actorID),
		TenantID:     strings.TrimSpace(tenantID),
		RepositoryID: strings.TrimSpace(repositoryID),
		Permissions:  make(map[Permission]struct{}, len(permissions)),
	}
	if principal.ActorType != "human" && principal.ActorType != "agent" &&
		principal.ActorType != "worker" && principal.ActorType != "system" {
		return Principal{}, fault.New(
			fault.CodeUnauthenticated,
			"control.NewPrincipal",
			"actor type is not an authenticated Forja identity",
		)
	}
	if length := utf8.RuneCountInString(principal.ActorID); length < 1 || length > 160 {
		return Principal{}, fault.New(
			fault.CodeUnauthenticated,
			"control.NewPrincipal",
			"actor ID length must be between 1 and 160 characters",
		)
	}
	if principal.TenantID == "" || principal.RepositoryID == "" {
		return Principal{}, fault.New(
			fault.CodeUnauthenticated,
			"control.NewScopedPrincipal",
			"tenant and repository authority are required",
		)
	}
	for _, permission := range permissions {
		if !validPermission(permission) {
			return Principal{}, fault.New(
				fault.CodeInvalidArgument,
				"control.NewPrincipal",
				fmt.Sprintf("unknown permission %q", permission),
			)
		}
		if permission == PermissionMemoryPromote &&
			(principal.ActorType == "agent" || principal.ActorType == "worker") {
			return Principal{}, fault.New(
				fault.CodePermissionDenied,
				"control.NewPrincipal",
				"agents and workers cannot hold memory promotion authority",
			)
		}
		principal.Permissions[permission] = struct{}{}
	}
	return principal, nil
}

func validPermission(permission Permission) bool {
	if permission == PermissionMemoryPromote {
		return true
	}
	for _, candidate := range AllPermissions {
		if candidate == permission {
			return true
		}
	}
	return false
}

type CommandContext struct {
	IdempotencyKey string
	CorrelationID  string
	CausationID    *string
}

type PlanSprintInput struct {
	Title     string
	Objective string
	Command   CommandContext
}

type SubmitSprintInput struct {
	SprintID        string
	ExpectedVersion int
	RiskClass       string
	Command         CommandContext
}

type ResolveDecisionInput struct {
	DecisionID      string
	ExpectedVersion int
	Reason          string
	Command         CommandContext
}

type TransitionInput struct {
	RunID           string
	ExpectedVersion int
	Command         CommandContext
}

type Service struct {
	repository    Repository
	newSprintID   func() (identity.SprintID, error)
	newDecisionID func() (identity.DecisionID, error)
	newRunID      func() (identity.RunID, error)
}

func NewService(repository Repository) (*Service, error) {
	if repository == nil {
		return nil, fmt.Errorf("repository is required")
	}
	return &Service{
		repository:    repository,
		newSprintID:   identity.NewSprintID,
		newDecisionID: identity.NewDecisionID,
		newRunID:      identity.NewRunID,
	}, nil
}

func (s *Service) WithIDGenerators(
	newSprintID func() (identity.SprintID, error),
	newDecisionID func() (identity.DecisionID, error),
	newRunID func() (identity.RunID, error),
) *Service {
	if newSprintID != nil {
		s.newSprintID = newSprintID
	}
	if newDecisionID != nil {
		s.newDecisionID = newDecisionID
	}
	if newRunID != nil {
		s.newRunID = newRunID
	}
	return s
}

func (s *Service) PlanSprint(
	ctx context.Context,
	principal Principal,
	input PlanSprintInput,
) (PlanResult, error) {
	if err := s.authorize(principal, PermissionPlan); err != nil {
		return PlanResult{}, err
	}
	if err := validateTitle(input.Title); err != nil {
		return PlanResult{}, err
	}
	if err := validateObjective(input.Objective); err != nil {
		return PlanResult{}, err
	}
	metadata, err := metadata(principal, input.Command, auditToolPlanSprint)
	if err != nil {
		return PlanResult{}, err
	}
	sprintID, err := s.newSprintID()
	if err != nil {
		return PlanResult{}, fault.Wrap(fault.CodeInternal, "control.PlanSprint", "generate sprint ID", err)
	}
	runID, err := s.newRunID()
	if err != nil {
		return PlanResult{}, fault.Wrap(fault.CodeInternal, "control.PlanSprint", "generate run ID", err)
	}
	return s.repository.PlanSprint(ctx, PlanCommand{
		SprintID:  sprintID,
		RunID:     runID,
		Title:     strings.TrimSpace(input.Title),
		Objective: strings.TrimSpace(input.Objective),
	}, metadata)
}

func (s *Service) GetSprint(
	ctx context.Context,
	principal Principal,
	sprintID string,
	command CommandContext,
) (Sprint, error) {
	if err := s.authorize(principal, PermissionRead); err != nil {
		return Sprint{}, err
	}
	if _, err := metadata(principal, command); err != nil {
		return Sprint{}, err
	}
	id, err := identity.ParseSprintID(sprintID)
	if err != nil {
		return Sprint{}, fault.Wrap(fault.CodeInvalidArgument, "control.GetSprint", "parse sprint ID", err)
	}
	return s.repository.GetSprint(ctx, id)
}

func (s *Service) SubmitSprint(
	ctx context.Context,
	principal Principal,
	input SubmitSprintInput,
) (SubmissionResult, error) {
	if err := s.authorize(principal, PermissionSubmit); err != nil {
		return SubmissionResult{}, err
	}
	id, err := identity.ParseSprintID(input.SprintID)
	if err != nil {
		return SubmissionResult{}, fault.Wrap(fault.CodeInvalidArgument, "control.SubmitSprint", "parse sprint ID", err)
	}
	if input.ExpectedVersion < 1 {
		return SubmissionResult{}, fault.New(fault.CodeInvalidArgument, "control.SubmitSprint", "expected version must be positive")
	}
	if err := validateRiskClass(input.RiskClass); err != nil {
		return SubmissionResult{}, err
	}
	commandMetadata, err := metadata(principal, input.Command, auditToolSubmitSprint)
	if err != nil {
		return SubmissionResult{}, err
	}
	decisionID, err := s.newDecisionID()
	if err != nil {
		return SubmissionResult{}, fault.Wrap(fault.CodeInternal, "control.SubmitSprint", "generate decision ID", err)
	}
	return s.repository.SubmitSprint(ctx, SubmitCommand{
		SprintID:        id,
		DecisionID:      decisionID,
		ExpectedVersion: input.ExpectedVersion,
		RiskClass:       input.RiskClass,
	}, commandMetadata)
}

func (s *Service) ResolveDecision(
	ctx context.Context,
	principal Principal,
	input ResolveDecisionInput,
	approve bool,
) (DecisionResult, error) {
	if err := s.authorize(principal, PermissionDecide); err != nil {
		return DecisionResult{}, err
	}
	id, err := identity.ParseDecisionID(input.DecisionID)
	if err != nil {
		return DecisionResult{}, fault.Wrap(fault.CodeInvalidArgument, "control.ResolveDecision", "parse decision ID", err)
	}
	if input.ExpectedVersion < 1 {
		return DecisionResult{}, fault.New(fault.CodeInvalidArgument, "control.ResolveDecision", "expected version must be positive")
	}
	reason := strings.TrimSpace(input.Reason)
	if length := utf8.RuneCountInString(reason); length < 3 || length > 2000 {
		return DecisionResult{}, fault.New(fault.CodeInvalidArgument, "control.ResolveDecision", "reason length must be between 3 and 2000 characters")
	}
	auditTool := auditToolRejectDecision
	if approve {
		auditTool = auditToolApproveDecision
	}
	commandMetadata, err := metadata(principal, input.Command, auditTool)
	if err != nil {
		return DecisionResult{}, err
	}
	return s.repository.ResolveDecision(ctx, DecideCommand{
		DecisionID:      id,
		ExpectedVersion: input.ExpectedVersion,
		Approve:         approve,
		Reason:          reason,
	}, commandMetadata)
}

func (s *Service) GetRun(
	ctx context.Context,
	principal Principal,
	runID string,
	command CommandContext,
) (contracts.Run, error) {
	if err := s.authorize(principal, PermissionRead); err != nil {
		return contracts.Run{}, err
	}
	if _, err := metadata(principal, command); err != nil {
		return contracts.Run{}, err
	}
	id, err := identity.ParseRunID(runID)
	if err != nil {
		return contracts.Run{}, fault.Wrap(fault.CodeInvalidArgument, "control.GetRun", "parse run ID", err)
	}
	return s.repository.GetRun(ctx, id)
}

func (s *Service) CancelRun(
	ctx context.Context,
	principal Principal,
	input TransitionInput,
) (contracts.Run, error) {
	return s.transition(ctx, principal, input, PermissionCancel, runstate.StateCancelling)
}

func (s *Service) ResumeRun(
	ctx context.Context,
	principal Principal,
	input TransitionInput,
) (contracts.Run, error) {
	if err := s.authorize(principal, PermissionResume); err != nil {
		return contracts.Run{}, err
	}
	if input.ExpectedVersion < 1 {
		return contracts.Run{}, fault.New(fault.CodeInvalidArgument, "control.ResumeRun", "expected version must be positive")
	}
	id, err := identity.ParseRunID(input.RunID)
	if err != nil {
		return contracts.Run{}, fault.Wrap(fault.CodeInvalidArgument, "control.ResumeRun", "parse run ID", err)
	}
	commandMetadata, err := metadata(principal, input.Command, auditToolResumeRun)
	if err != nil {
		return contracts.Run{}, err
	}
	return s.repository.ResumeRun(ctx, id, input.ExpectedVersion, commandMetadata)
}

func (s *Service) transition(
	ctx context.Context,
	principal Principal,
	input TransitionInput,
	permission Permission,
	target runstate.State,
) (contracts.Run, error) {
	if err := s.authorize(principal, permission); err != nil {
		return contracts.Run{}, err
	}
	id, err := identity.ParseRunID(input.RunID)
	if err != nil {
		return contracts.Run{}, fault.Wrap(fault.CodeInvalidArgument, "control.transition", "parse run ID", err)
	}
	if input.ExpectedVersion < 1 {
		return contracts.Run{}, fault.New(fault.CodeInvalidArgument, "control.transition", "expected version must be positive")
	}
	auditTool := ""
	if target == runstate.StateCancelling {
		auditTool = auditToolCancelRun
	}
	commandMetadata, err := metadata(principal, input.Command, auditTool)
	if err != nil {
		return contracts.Run{}, err
	}
	return s.repository.TransitionRun(ctx, id, input.ExpectedVersion, target, commandMetadata)
}

func (s *Service) RecordToolAudit(ctx context.Context, principal Principal, record AuditRecord) error {
	if principal.ActorID == "" {
		return fault.New(fault.CodeUnauthenticated, "control.RecordToolAudit", "principal is required")
	}
	if record.Outcome == "denied" {
		if record.ErrorCode != string(fault.CodePermissionDenied) {
			return fault.New(
				fault.CodeInvalidArgument,
				"control.RecordToolAudit",
				"denied audits require the permission_denied error code",
			)
		}
	} else if err := s.authorizeScope(principal); err != nil {
		return err
	}
	if record.Replay {
		return fault.New(
			fault.CodeInvalidArgument,
			"control.RecordToolAudit",
			"replay audits are emitted only by the idempotent repository path",
		)
	}
	if record.Outcome == "succeeded" && isMutatingAuditTool(record.ToolName) {
		return fault.New(
			fault.CodeInvalidArgument,
			"control.RecordToolAudit",
			"mutating success audits must commit atomically with their domain command",
		)
	}
	record.ActorType = principal.ActorType
	record.ActorID = principal.ActorID
	return s.repository.RecordToolAudit(ctx, record)
}

func isMutatingAuditTool(tool string) bool {
	switch tool {
	case auditToolPlanSprint,
		auditToolSubmitSprint,
		auditToolApproveDecision,
		auditToolRejectDecision,
		auditToolCancelRun,
		auditToolResumeRun:
		return true
	default:
		return false
	}
}

func (s *Service) authorize(principal Principal, permission Permission) error {
	if principal.ActorID == "" || principal.ActorType == "" {
		return fault.New(fault.CodeUnauthenticated, "control.authorize", "authenticated principal is required")
	}
	if _, ok := principal.Permissions[permission]; !ok {
		return fault.New(fault.CodePermissionDenied, "control.authorize", fmt.Sprintf("permission %q is required", permission))
	}
	return s.authorizeScope(principal)
}

func (s *Service) authorizeScope(principal Principal) error {
	authority := s.repository.Authority()
	if principal.TenantID != authority.TenantID ||
		principal.RepositoryID != authority.RepositoryID {
		return fault.New(
			fault.CodePermissionDenied,
			"control.authorizeScope",
			"principal authority does not match the bound repository",
		)
	}
	return nil
}

func metadata(
	principal Principal,
	command CommandContext,
	auditToolName ...string,
) (runstate.CommandMetadata, error) {
	toolName := ""
	if len(auditToolName) > 0 {
		toolName = auditToolName[0]
	}
	value := runstate.CommandMetadata{
		IdempotencyKey: strings.TrimSpace(command.IdempotencyKey),
		ActorType:      principal.ActorType,
		ActorID:        principal.ActorID,
		CorrelationID:  strings.TrimSpace(command.CorrelationID),
		CausationID:    command.CausationID,
		AuditToolName:  toolName,
	}
	if err := runstate.ValidateCommandMetadata(value); err != nil {
		return runstate.CommandMetadata{}, err
	}
	return value, nil
}

func validateTitle(value string) error {
	if length := utf8.RuneCountInString(strings.TrimSpace(value)); length < 1 || length > 500 {
		return fault.New(fault.CodeInvalidArgument, "control.validateTitle", "title length must be between 1 and 500 characters")
	}
	return nil
}

func validateObjective(value string) error {
	if length := utf8.RuneCountInString(strings.TrimSpace(value)); length < 3 || length > 8000 {
		return fault.New(fault.CodeInvalidArgument, "control.validateObjective", "objective length must be between 3 and 8000 characters")
	}
	return nil
}

func validateRiskClass(value string) error {
	switch value {
	case "low", "medium", "high", "critical":
		return nil
	default:
		return fault.New(fault.CodeInvalidArgument, "control.validateRiskClass", "risk class must be low, medium, high, or critical")
	}
}
