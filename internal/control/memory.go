package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/rvbernucci/forja-guide/internal/clock"
	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

type memoryReceipt struct {
	requestHash [sha256.Size]byte
	response    []byte
}

// MemoryRepository provides deterministic ephemeral behavior for tests and
// local MCP sessions. PostgreSQL remains the durable authority.
type MemoryRepository struct {
	mu           sync.RWMutex
	clock        clock.Clock
	machine      *runstate.Machine
	sprints      map[identity.SprintID]Sprint
	decisions    map[identity.DecisionID]Decision
	runs         map[identity.RunID]contracts.Run
	receipts     map[string]memoryReceipt
	audits       []AuditRecord
	nextSequence int
}

func (s *MemoryRepository) Authority() Authority {
	return Authority{TenantID: LocalTenantID, RepositoryID: LocalRepositoryID}
}

func NewMemoryRepository(source clock.Clock) *MemoryRepository {
	if source == nil {
		source = clock.Real{}
	}
	return &MemoryRepository{
		clock:        source,
		machine:      runstate.NewMachine(source),
		sprints:      make(map[identity.SprintID]Sprint),
		decisions:    make(map[identity.DecisionID]Decision),
		runs:         make(map[identity.RunID]contracts.Run),
		receipts:     make(map[string]memoryReceipt),
		nextSequence: 1,
	}
}

func (s *MemoryRepository) PlanSprint(
	_ context.Context,
	command PlanCommand,
	metadata runstate.CommandMetadata,
) (PlanResult, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return PlanResult{}, err
	}
	requestHash := controlHash(metadata, "plan_sprint", command.Title, command.Objective)
	s.mu.Lock()
	defer s.mu.Unlock()
	var replay PlanResult
	if found, err := s.replayLocked("plan_sprint", metadata.IdempotencyKey, requestHash, &replay); err != nil {
		return PlanResult{}, err
	} else if found {
		s.appendSuccessAuditLocked(metadata, "plan_sprint", true)
		return replay, nil
	}
	if _, exists := s.sprints[command.SprintID]; exists {
		return PlanResult{}, fault.New(fault.CodeConflict, "control.MemoryRepository.PlanSprint", "sprint already exists")
	}
	if _, exists := s.runs[command.RunID]; exists {
		return PlanResult{}, fault.New(fault.CodeConflict, "control.MemoryRepository.PlanSprint", "run already exists")
	}
	now := s.clock.Now().UTC()
	sprint := Sprint{
		SprintID:       command.SprintID.String(),
		SchemaVersion:  "1.0",
		SequenceNumber: s.nextSequence,
		Title:          command.Title,
		Objective:      command.Objective,
		Status:         string(SprintProposed),
		Version:        1,
		RunID:          command.RunID.String(),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	run := contracts.Run{
		RunID:         command.RunID.String(),
		SchemaVersion: "1.0",
		Objective:     command.Objective,
		State:         string(runstate.StateDraft),
		Version:       1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	s.nextSequence++
	s.sprints[command.SprintID] = sprint
	s.runs[command.RunID] = run
	result := PlanResult{Sprint: sprint, Run: run}
	if err := s.saveReceiptLocked("plan_sprint", metadata.IdempotencyKey, requestHash, result); err != nil {
		return PlanResult{}, err
	}
	s.appendSuccessAuditLocked(metadata, "plan_sprint", false)
	return result, nil
}

func (s *MemoryRepository) GetSprint(
	_ context.Context,
	id identity.SprintID,
) (Sprint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sprint, ok := s.sprints[id]
	if !ok {
		return Sprint{}, fault.New(fault.CodeNotFound, "control.MemoryRepository.GetSprint", fmt.Sprintf("sprint %s was not found", id))
	}
	return sprint, nil
}

func (s *MemoryRepository) SubmitSprint(
	_ context.Context,
	command SubmitCommand,
	metadata runstate.CommandMetadata,
) (SubmissionResult, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return SubmissionResult{}, err
	}
	requestHash := controlHash(
		metadata,
		"submit_sprint",
		command.SprintID.String(),
		fmt.Sprint(command.ExpectedVersion),
		command.RiskClass,
	)
	s.mu.Lock()
	defer s.mu.Unlock()
	var replay SubmissionResult
	scope := "submit_sprint:" + command.SprintID.String()
	if found, err := s.replayLocked(scope, metadata.IdempotencyKey, requestHash, &replay); err != nil {
		return SubmissionResult{}, err
	} else if found {
		s.appendSuccessAuditLocked(metadata, scope, true)
		return replay, nil
	}
	sprint, ok := s.sprints[command.SprintID]
	if !ok {
		return SubmissionResult{}, fault.New(fault.CodeNotFound, "control.MemoryRepository.SubmitSprint", "sprint was not found")
	}
	if sprint.Version != command.ExpectedVersion || sprint.Status != string(SprintProposed) {
		return SubmissionResult{}, fault.New(fault.CodeConflict, "control.MemoryRepository.SubmitSprint", "sprint is not a matching proposed version")
	}
	runID, err := identity.ParseRunID(sprint.RunID)
	if err != nil {
		return SubmissionResult{}, fault.Wrap(fault.CodeInternal, "control.MemoryRepository.SubmitSprint", "stored run ID is invalid", err)
	}
	run := s.runs[runID]
	updatedRun, err := s.machine.Transition(run, runstate.StateAwaitingApproval)
	if err != nil {
		return SubmissionResult{}, err
	}
	now := s.clock.Now().UTC()
	if now.Before(sprint.UpdatedAt) {
		now = sprint.UpdatedAt
	}
	sprint.Status = string(SprintAwaitingApproval)
	sprint.Version++
	sprint.UpdatedAt = now
	decisionID := command.DecisionID.String()
	sprint.PendingDecisionID = &decisionID
	decision := Decision{
		DecisionID:    decisionID,
		SchemaVersion: "1.0",
		SprintID:      sprint.SprintID,
		RunID:         sprint.RunID,
		Action:        DecisionActionSubmitSprint,
		RiskClass:     command.RiskClass,
		Status:        string(DecisionPending),
		Version:       1,
		RequestedBy:   metadata.ActorID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	s.sprints[command.SprintID] = sprint
	s.runs[runID] = updatedRun
	s.decisions[command.DecisionID] = decision
	result := SubmissionResult{Sprint: sprint, Decision: decision, Run: updatedRun}
	if err := s.saveReceiptLocked(scope, metadata.IdempotencyKey, requestHash, result); err != nil {
		return SubmissionResult{}, err
	}
	s.appendSuccessAuditLocked(metadata, scope, false)
	return result, nil
}

func (s *MemoryRepository) ResolveDecision(
	_ context.Context,
	command DecideCommand,
	metadata runstate.CommandMetadata,
) (DecisionResult, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return DecisionResult{}, err
	}
	requestHash := controlHash(
		metadata,
		"resolve_decision",
		command.DecisionID.String(),
		fmt.Sprint(command.ExpectedVersion),
		fmt.Sprint(command.Approve),
		command.Reason,
	)
	scope := "resolve_decision:" + command.DecisionID.String()
	s.mu.Lock()
	defer s.mu.Unlock()
	var replay DecisionResult
	if found, err := s.replayLocked(scope, metadata.IdempotencyKey, requestHash, &replay); err != nil {
		return DecisionResult{}, err
	} else if found {
		s.appendSuccessAuditLocked(metadata, scope, true)
		return replay, nil
	}
	decision, ok := s.decisions[command.DecisionID]
	if !ok {
		return DecisionResult{}, fault.New(fault.CodeNotFound, "control.MemoryRepository.ResolveDecision", "decision was not found")
	}
	if decision.Version != command.ExpectedVersion || decision.Status != string(DecisionPending) {
		return DecisionResult{}, fault.New(fault.CodeConflict, "control.MemoryRepository.ResolveDecision", "decision is not a matching pending version")
	}
	sprintID, err := identity.ParseSprintID(decision.SprintID)
	if err != nil {
		return DecisionResult{}, fault.Wrap(fault.CodeInternal, "control.MemoryRepository.ResolveDecision", "stored sprint ID is invalid", err)
	}
	runID, err := identity.ParseRunID(decision.RunID)
	if err != nil {
		return DecisionResult{}, fault.Wrap(fault.CodeInternal, "control.MemoryRepository.ResolveDecision", "stored run ID is invalid", err)
	}
	sprint := s.sprints[sprintID]
	run := s.runs[runID]
	target := runstate.StateCancelling
	decisionStatus := DecisionRejected
	sprintStatus := SprintRejected
	if command.Approve {
		target = runstate.StateQueued
		decisionStatus = DecisionApproved
		sprintStatus = SprintApproved
	}
	updatedRun, err := s.machine.Transition(run, target)
	if err != nil {
		return DecisionResult{}, err
	}
	now := s.clock.Now().UTC()
	if now.Before(decision.UpdatedAt) {
		now = decision.UpdatedAt
	}
	decision.Status = string(decisionStatus)
	decision.Version++
	decision.UpdatedAt = now
	decision.DecidedAt = &now
	decision.DecidedBy = &metadata.ActorID
	decision.Reason = &command.Reason
	sprint.Status = string(sprintStatus)
	sprint.Version++
	sprint.UpdatedAt = now
	sprint.PendingDecisionID = nil
	s.decisions[command.DecisionID] = decision
	s.sprints[sprintID] = sprint
	s.runs[runID] = updatedRun
	result := DecisionResult{Sprint: sprint, Decision: decision, Run: updatedRun}
	if err := s.saveReceiptLocked(scope, metadata.IdempotencyKey, requestHash, result); err != nil {
		return DecisionResult{}, err
	}
	s.appendSuccessAuditLocked(metadata, scope, false)
	return result, nil
}

func (s *MemoryRepository) GetRun(_ context.Context, id identity.RunID) (contracts.Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.runs[id]
	if !ok {
		return contracts.Run{}, fault.New(fault.CodeNotFound, "control.MemoryRepository.GetRun", fmt.Sprintf("run %s was not found", id))
	}
	return run, nil
}

func (s *MemoryRepository) TransitionRun(
	_ context.Context,
	id identity.RunID,
	expectedVersion int,
	target runstate.State,
	metadata runstate.CommandMetadata,
) (contracts.Run, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return contracts.Run{}, err
	}
	requestHash := controlHash(metadata, "transition_run", id.String(), fmt.Sprint(expectedVersion), string(target))
	scope := "transition_run:" + id.String()
	s.mu.Lock()
	defer s.mu.Unlock()
	var replay contracts.Run
	if found, err := s.replayLocked(scope, metadata.IdempotencyKey, requestHash, &replay); err != nil {
		return contracts.Run{}, err
	} else if found {
		s.appendSuccessAuditLocked(metadata, scope, true)
		return replay, nil
	}
	run, ok := s.runs[id]
	if !ok {
		return contracts.Run{}, fault.New(fault.CodeNotFound, "control.MemoryRepository.TransitionRun", "run was not found")
	}
	if run.Version != expectedVersion {
		return contracts.Run{}, fault.New(fault.CodeConflict, "control.MemoryRepository.TransitionRun", "run version mismatch")
	}
	if isPrivilegedResumeTransition(run.State, target) {
		return contracts.Run{}, fault.New(
			fault.CodePermissionDenied,
			"control.MemoryRepository.TransitionRun",
			"resume transitions require the governed ResumeRun command",
		)
	}
	var linkedSprintID identity.SprintID
	var linkedSprint Sprint
	for sprintID, sprint := range s.sprints {
		if sprint.RunID != id.String() {
			continue
		}
		if sprint.Status == string(SprintAwaitingApproval) || sprint.PendingDecisionID != nil {
			return contracts.Run{}, fault.New(
				fault.CodeConflict,
				"control.MemoryRepository.TransitionRun",
				"resolve the pending decision before transitioning its run",
			)
		}
		if sprint.Status == string(SprintProposed) && target != runstate.StateCancelling {
			return contracts.Run{}, fault.New(
				fault.CodeConflict,
				"control.MemoryRepository.TransitionRun",
				"submit the proposed Sprint before transitioning its run",
			)
		}
		if target == runstate.StateCancelling {
			linkedSprintID = sprintID
			linkedSprint = sprint
		}
		break
	}
	updated, err := s.machine.Transition(run, target)
	if err != nil {
		return contracts.Run{}, err
	}
	s.runs[id] = updated
	if linkedSprintID != "" &&
		(linkedSprint.Status == string(SprintProposed) || linkedSprint.Status == string(SprintApproved)) {
		linkedSprint.Status = string(SprintCancelling)
		linkedSprint.Version++
		linkedSprint.UpdatedAt = updated.UpdatedAt
		s.sprints[linkedSprintID] = linkedSprint
	}
	if err := s.saveReceiptLocked(scope, metadata.IdempotencyKey, requestHash, updated); err != nil {
		return contracts.Run{}, err
	}
	s.appendSuccessAuditLocked(metadata, scope, false)
	return updated, nil
}

func (s *MemoryRepository) ResumeRun(
	_ context.Context,
	id identity.RunID,
	expectedVersion int,
	metadata runstate.CommandMetadata,
) (contracts.Run, error) {
	if expectedVersion < 1 {
		return contracts.Run{}, fault.New(fault.CodeInvalidArgument, "control.MemoryRepository.ResumeRun", "expected version must be positive")
	}
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return contracts.Run{}, err
	}
	requestHash := controlHash(metadata, "resume_run", id.String(), fmt.Sprint(expectedVersion))
	scope := "resume_run:" + id.String()
	s.mu.Lock()
	defer s.mu.Unlock()
	var replay contracts.Run
	if found, err := s.replayLocked(scope, metadata.IdempotencyKey, requestHash, &replay); err != nil {
		return contracts.Run{}, err
	} else if found {
		s.appendSuccessAuditLocked(metadata, scope, true)
		return replay, nil
	}
	run, ok := s.runs[id]
	if !ok {
		return contracts.Run{}, fault.New(fault.CodeNotFound, "control.MemoryRepository.ResumeRun", "run was not found")
	}
	if run.Version != expectedVersion {
		return contracts.Run{}, fault.New(fault.CodeConflict, "control.MemoryRepository.ResumeRun", "run version mismatch")
	}
	target, err := resumeTarget(run)
	if err != nil {
		return contracts.Run{}, err
	}
	updated, err := s.machine.Transition(run, target)
	if err != nil {
		return contracts.Run{}, err
	}
	s.runs[id] = updated
	if err := s.saveReceiptLocked(scope, metadata.IdempotencyKey, requestHash, updated); err != nil {
		return contracts.Run{}, err
	}
	s.appendSuccessAuditLocked(metadata, scope, false)
	return updated, nil
}

func resumeTarget(run contracts.Run) (runstate.State, error) {
	switch run.State {
	case string(runstate.StateFailedRetryable):
		return runstate.StateQueued, nil
	case string(runstate.StateAwaitingDecision):
		return runstate.StateRunning, nil
	default:
		return "", fault.New(
			fault.CodeConflict,
			"control.resumeTarget",
			"only failed_retryable or awaiting_decision runs can resume",
		)
	}
}

func (s *MemoryRepository) RecordToolAudit(_ context.Context, record AuditRecord) error {
	if record.ToolName == "" || record.CorrelationID == "" || record.ActorID == "" {
		return fault.New(fault.CodeInvalidArgument, "control.MemoryRepository.RecordToolAudit", "tool, actor, and correlation are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if record.OccurredAt.IsZero() {
		record.OccurredAt = s.clock.Now().UTC()
	}
	s.audits = append(s.audits, record)
	return nil
}

func (s *MemoryRepository) AuditRecords() []AuditRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]AuditRecord(nil), s.audits...)
}

func (s *MemoryRepository) appendSuccessAuditLocked(
	metadata runstate.CommandMetadata,
	commandScope string,
	replay bool,
) {
	if metadata.AuditToolName == "" {
		return
	}
	s.audits = append(s.audits, AuditRecord{
		ToolName:       metadata.AuditToolName,
		Outcome:        "succeeded",
		ActorType:      metadata.ActorType,
		ActorID:        metadata.ActorID,
		CorrelationID:  metadata.CorrelationID,
		CausationID:    metadata.CausationID,
		IdempotencyKey: metadata.IdempotencyKey,
		CommandScope:   commandScope,
		Replay:         replay,
		OccurredAt:     s.clock.Now().UTC(),
	})
}

func isPrivilegedResumeTransition(source string, target runstate.State) bool {
	return (source == string(runstate.StateFailedRetryable) && target == runstate.StateQueued) ||
		(source == string(runstate.StateAwaitingDecision) && target == runstate.StateRunning)
}

func (s *MemoryRepository) replayLocked(scope, key string, requestHash [sha256.Size]byte, target any) (bool, error) {
	receipt, found := s.receipts[scope+"\x00"+key]
	if !found {
		return false, nil
	}
	if receipt.requestHash != requestHash {
		return true, fault.New(fault.CodeConflict, "control.MemoryRepository.idempotency", "idempotency key was reused with a different command")
	}
	decoder := json.NewDecoder(bytes.NewReader(receipt.response))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return true, fault.Wrap(fault.CodeInternal, "control.MemoryRepository.idempotency", "decode replay", err)
	}
	return true, nil
}

func (s *MemoryRepository) saveReceiptLocked(scope, key string, requestHash [sha256.Size]byte, response any) error {
	data, err := json.Marshal(response)
	if err != nil {
		return fault.Wrap(fault.CodeInternal, "control.MemoryRepository.idempotency", "encode replay receipt", err)
	}
	s.receipts[scope+"\x00"+key] = memoryReceipt{requestHash: requestHash, response: data}
	return nil
}

func controlHash(metadata runstate.CommandMetadata, parts ...string) [sha256.Size]byte {
	causation := ""
	if metadata.CausationID != nil {
		causation = *metadata.CausationID
	}
	parts = append(parts, metadata.ActorType, metadata.ActorID, causation)
	if metadata.AuditToolName != "" {
		parts = append(parts, metadata.AuditToolName)
	}
	data, _ := json.Marshal(parts)
	return sha256.Sum256(data)
}

var _ Repository = (*MemoryRepository)(nil)
