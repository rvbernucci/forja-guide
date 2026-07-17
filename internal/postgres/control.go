package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

func (s *Store) Authority() control.Authority {
	return control.Authority{TenantID: s.tenantID, RepositoryID: s.repositoryID}
}

func (s *Store) PlanSprint(
	ctx context.Context,
	command control.PlanCommand,
	metadata runstate.CommandMetadata,
) (control.PlanResult, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return control.PlanResult{}, err
	}
	scope := "plan_sprint:" + s.repositoryID
	requestHash := hashCommand(metadata, "plan_sprint", s.repositoryID, command.Title, command.Objective)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return control.PlanResult{}, databaseError("postgres.PlanSprint.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return control.PlanResult{}, err
	}
	if replay, found, err := loadControlReplay[control.PlanResult](ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return control.PlanResult{}, err
	} else if found {
		if err := s.appendSuccessToolAudit(ctx, tx, metadata, scope, postgresTimestamp(s.clock.Now()), true); err != nil {
			return control.PlanResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return control.PlanResult{}, databaseError("postgres.PlanSprint.commitReplayAudit", err)
		}
		return replay, nil
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", advisoryLockKey("sprint-sequence:"+s.repositoryID)); err != nil {
		return control.PlanResult{}, databaseError("postgres.PlanSprint.lockSequence", err)
	}
	var sequence int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(sequence_number), 0) + 1
		FROM forja.sprints
		WHERE tenant_id=$1 AND repository_id=$2`,
		s.tenantID, s.repositoryID,
	).Scan(&sequence); err != nil {
		return control.PlanResult{}, databaseError("postgres.PlanSprint.sequence", err)
	}
	now := postgresTimestamp(s.clock.Now())
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.sprints (
			sprint_id, tenant_id, repository_id, sequence_number, title,
			objective, status, version, run_id, created_at, updated_at
		) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, 1, $8, $9, $9)`,
		command.SprintID.UUID(), s.tenantID, s.repositoryID, sequence,
		command.Title, command.Objective, control.SprintProposed,
		command.RunID.String(), now,
	); err != nil {
		return control.PlanResult{}, databaseError("postgres.PlanSprint.insertSprint", err)
	}
	run := contracts.Run{
		RunID: command.RunID.String(), SchemaVersion: "1.0", Objective: command.Objective,
		State: string(runstate.StateDraft), Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.runs (
			run_id, tenant_id, repository_id, sprint_id, objective, state,
			version, created_at, updated_at
		) VALUES ($1, $2, $3, $4::uuid, $5, $6, 1, $7, $7)`,
		run.RunID, s.tenantID, s.repositoryID, command.SprintID.UUID(),
		run.Objective, run.State, now,
	); err != nil {
		return control.PlanResult{}, databaseError("postgres.PlanSprint.insertRun", err)
	}
	sprint := control.Sprint{
		SprintID: command.SprintID.String(), SchemaVersion: "1.0",
		SequenceNumber: sequence, Title: command.Title, Objective: command.Objective,
		Status: string(control.SprintProposed), Version: 1, RunID: run.RunID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.appendControlEvent(ctx, tx, "sprint", sprint.SprintID, sprint.Version, "sprint.planned", now, sprint, metadata); err != nil {
		return control.PlanResult{}, err
	}
	if err := s.appendRunEvent(ctx, tx, "run.created", run, metadata); err != nil {
		return control.PlanResult{}, err
	}
	if err := s.appendSuccessToolAudit(ctx, tx, metadata, scope, now, false); err != nil {
		return control.PlanResult{}, err
	}
	result := control.PlanResult{Sprint: sprint, Run: run}
	if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 201, result); err != nil {
		return control.PlanResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return control.PlanResult{}, databaseError("postgres.PlanSprint.commit", err)
	}
	return result, nil
}

func (s *Store) GetSprint(ctx context.Context, id identity.SprintID) (control.Sprint, error) {
	sprint, err := scanSprint(s.pool.QueryRow(ctx, `
		SELECT 'sprint_' || sp.sprint_id::text, sp.sequence_number, sp.title,
		       sp.objective, sp.status, sp.version, sp.run_id,
		       (
		         SELECT d.decision_id
		         FROM forja.decisions AS d
		         WHERE d.tenant_id=sp.tenant_id
		           AND d.repository_id=sp.repository_id
		           AND d.sprint_id=sp.sprint_id
		           AND d.status='pending'
		         ORDER BY d.created_at, d.decision_id
		         LIMIT 1
		       ),
		       sp.created_at, sp.updated_at
		FROM forja.sprints AS sp
		WHERE sp.tenant_id=$1 AND sp.repository_id=$2 AND sp.sprint_id=$3::uuid`,
		s.tenantID, s.repositoryID, id.UUID(),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return control.Sprint{}, fault.New(fault.CodeNotFound, "postgres.GetSprint", fmt.Sprintf("sprint %s was not found", id))
	}
	if err != nil {
		return control.Sprint{}, databaseError("postgres.GetSprint", err)
	}
	return sprint, nil
}

func (s *Store) SubmitSprint(
	ctx context.Context,
	command control.SubmitCommand,
	metadata runstate.CommandMetadata,
) (control.SubmissionResult, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return control.SubmissionResult{}, err
	}
	scope := "submit_sprint:" + s.repositoryID + ":" + command.SprintID.String()
	requestHash := hashCommand(metadata, "submit_sprint", command.SprintID.String(), fmt.Sprint(command.ExpectedVersion), command.RiskClass)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return control.SubmissionResult{}, databaseError("postgres.SubmitSprint.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return control.SubmissionResult{}, err
	}
	if replay, found, err := loadControlReplay[control.SubmissionResult](ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return control.SubmissionResult{}, err
	} else if found {
		if err := s.appendSuccessToolAudit(ctx, tx, metadata, scope, postgresTimestamp(s.clock.Now()), true); err != nil {
			return control.SubmissionResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return control.SubmissionResult{}, databaseError("postgres.SubmitSprint.commitReplayAudit", err)
		}
		return replay, nil
	}
	sprint, err := scanSprint(tx.QueryRow(ctx, `
		SELECT 'sprint_' || sprint_id::text, sequence_number, title, objective,
		       status, version, run_id, NULL::text, created_at, updated_at
		FROM forja.sprints
		WHERE tenant_id=$1 AND repository_id=$2 AND sprint_id=$3::uuid
		FOR UPDATE`, s.tenantID, s.repositoryID, command.SprintID.UUID()))
	if errors.Is(err, pgx.ErrNoRows) {
		return control.SubmissionResult{}, fault.New(fault.CodeNotFound, "postgres.SubmitSprint", "sprint was not found")
	}
	if err != nil {
		return control.SubmissionResult{}, databaseError("postgres.SubmitSprint.selectSprint", err)
	}
	if sprint.Version != command.ExpectedVersion || sprint.Status != string(control.SprintProposed) {
		return control.SubmissionResult{}, fault.New(fault.CodeConflict, "postgres.SubmitSprint", "sprint is not a matching proposed version")
	}
	run, err := scanRun(tx.QueryRow(ctx, `
		SELECT run_id, objective, state, version, created_at, updated_at
		FROM forja.runs
		WHERE tenant_id=$1 AND repository_id=$2 AND run_id=$3
		FOR UPDATE`, s.tenantID, s.repositoryID, sprint.RunID))
	if err != nil {
		return control.SubmissionResult{}, databaseError("postgres.SubmitSprint.selectRun", err)
	}
	updatedRun, err := s.machine.Transition(run, runstate.StateAwaitingApproval)
	if err != nil {
		return control.SubmissionResult{}, err
	}
	updatedRun.UpdatedAt = postgresTimestamp(updatedRun.UpdatedAt)
	sprint.Status = string(control.SprintAwaitingApproval)
	sprint.Version++
	sprint.UpdatedAt = updatedRun.UpdatedAt
	decisionID := command.DecisionID.String()
	sprint.PendingDecisionID = &decisionID
	decision := control.Decision{
		DecisionID: decisionID, SchemaVersion: "1.0", SprintID: sprint.SprintID,
		RunID: sprint.RunID, Action: control.DecisionActionSubmitSprint,
		RiskClass: command.RiskClass, Status: string(control.DecisionPending), Version: 1,
		RequestedBy: metadata.ActorID, CreatedAt: sprint.UpdatedAt, UpdatedAt: sprint.UpdatedAt,
	}
	tag, err := tx.Exec(ctx, `
		UPDATE forja.sprints SET status=$1, version=$2, updated_at=$3
		WHERE tenant_id=$4 AND repository_id=$5 AND sprint_id=$6::uuid AND version=$7`,
		sprint.Status, sprint.Version, sprint.UpdatedAt, s.tenantID, s.repositoryID,
		command.SprintID.UUID(), command.ExpectedVersion,
	)
	if err != nil {
		return control.SubmissionResult{}, databaseError("postgres.SubmitSprint.updateSprint", err)
	}
	if tag.RowsAffected() != 1 {
		return control.SubmissionResult{}, fault.New(fault.CodeConflict, "postgres.SubmitSprint.updateSprint", "sprint version changed concurrently")
	}
	tag, err = tx.Exec(ctx, `
		UPDATE forja.runs SET state=$1, version=$2, updated_at=$3
		WHERE tenant_id=$4 AND repository_id=$5 AND run_id=$6 AND version=$7`,
		updatedRun.State, updatedRun.Version, updatedRun.UpdatedAt, s.tenantID,
		s.repositoryID, updatedRun.RunID, run.Version,
	)
	if err != nil {
		return control.SubmissionResult{}, databaseError("postgres.SubmitSprint.updateRun", err)
	}
	if tag.RowsAffected() != 1 {
		return control.SubmissionResult{}, fault.New(fault.CodeConflict, "postgres.SubmitSprint.updateRun", "run version changed concurrently")
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO forja.decisions (
			decision_id, tenant_id, repository_id, sprint_id, run_id, action,
			risk_class, status, version, requested_by, created_at, updated_at
		) VALUES ($1, $2, $3, $4::uuid, $5, $6, $7, $8, 1, $9, $10, $10)`,
		decision.DecisionID, s.tenantID, s.repositoryID, command.SprintID.UUID(),
		decision.RunID, decision.Action, decision.RiskClass, decision.Status,
		decision.RequestedBy, decision.CreatedAt,
	)
	if err != nil {
		return control.SubmissionResult{}, databaseError("postgres.SubmitSprint.insertDecision", err)
	}
	if err := s.appendControlEvent(ctx, tx, "sprint", sprint.SprintID, sprint.Version, "sprint.submitted", sprint.UpdatedAt, sprint, metadata); err != nil {
		return control.SubmissionResult{}, err
	}
	if err := s.appendRunEvent(ctx, tx, "run.transitioned", updatedRun, metadata); err != nil {
		return control.SubmissionResult{}, err
	}
	if err := s.appendControlEvent(ctx, tx, "decision", decision.DecisionID, decision.Version, "decision.requested", decision.UpdatedAt, decision, metadata); err != nil {
		return control.SubmissionResult{}, err
	}
	if err := s.appendSuccessToolAudit(ctx, tx, metadata, scope, sprint.UpdatedAt, false); err != nil {
		return control.SubmissionResult{}, err
	}
	result := control.SubmissionResult{Sprint: sprint, Decision: decision, Run: updatedRun}
	if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 200, result); err != nil {
		return control.SubmissionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return control.SubmissionResult{}, databaseError("postgres.SubmitSprint.commit", err)
	}
	return result, nil
}

func (s *Store) ResolveDecision(
	ctx context.Context,
	command control.DecideCommand,
	metadata runstate.CommandMetadata,
) (control.DecisionResult, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return control.DecisionResult{}, err
	}
	scope := "resolve_decision:" + s.repositoryID + ":" + command.DecisionID.String()
	requestHash := hashCommand(metadata, "resolve_decision", command.DecisionID.String(), fmt.Sprint(command.ExpectedVersion), fmt.Sprint(command.Approve), command.Reason)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return control.DecisionResult{}, databaseError("postgres.ResolveDecision.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return control.DecisionResult{}, err
	}
	if replay, found, err := loadControlReplay[control.DecisionResult](ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return control.DecisionResult{}, err
	} else if found {
		if err := s.appendSuccessToolAudit(ctx, tx, metadata, scope, postgresTimestamp(s.clock.Now()), true); err != nil {
			return control.DecisionResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return control.DecisionResult{}, databaseError("postgres.ResolveDecision.commitReplayAudit", err)
		}
		return replay, nil
	}
	decision, err := scanDecision(tx.QueryRow(ctx, `
		SELECT decision_id, 'sprint_' || sprint_id::text, run_id, action,
		       risk_class, status, version, requested_by, decided_by, reason,
		       created_at, updated_at, decided_at
		FROM forja.decisions
		WHERE tenant_id=$1 AND repository_id=$2 AND decision_id=$3
		FOR UPDATE`, s.tenantID, s.repositoryID, command.DecisionID.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return control.DecisionResult{}, fault.New(fault.CodeNotFound, "postgres.ResolveDecision", "decision was not found")
	}
	if err != nil {
		return control.DecisionResult{}, databaseError("postgres.ResolveDecision.selectDecision", err)
	}
	if decision.Version != command.ExpectedVersion || decision.Status != string(control.DecisionPending) {
		return control.DecisionResult{}, fault.New(fault.CodeConflict, "postgres.ResolveDecision", "decision is not a matching pending version")
	}
	sprintID, err := identity.ParseSprintID(decision.SprintID)
	if err != nil {
		return control.DecisionResult{}, fault.Wrap(
			fault.CodeInternal,
			"postgres.ResolveDecision",
			"stored Sprint ID violates the public contract",
			err,
		)
	}
	sprint, err := scanSprint(tx.QueryRow(ctx, `
		SELECT 'sprint_' || sp.sprint_id::text, sp.sequence_number, sp.title,
		       sp.objective, sp.status, sp.version, sp.run_id, d.decision_id,
		       sp.created_at, sp.updated_at
		FROM forja.sprints AS sp
		JOIN forja.decisions AS d USING (tenant_id, repository_id, sprint_id)
		WHERE sp.tenant_id=$1 AND sp.repository_id=$2 AND sp.sprint_id=$3::uuid
		  AND d.decision_id=$4
		FOR UPDATE OF sp`, s.tenantID, s.repositoryID, sprintID.UUID(), decision.DecisionID))
	if err != nil {
		return control.DecisionResult{}, databaseError("postgres.ResolveDecision.selectSprint", err)
	}
	run, err := scanRun(tx.QueryRow(ctx, `
		SELECT run_id, objective, state, version, created_at, updated_at
		FROM forja.runs
		WHERE tenant_id=$1 AND repository_id=$2 AND run_id=$3
		FOR UPDATE`, s.tenantID, s.repositoryID, decision.RunID))
	if err != nil {
		return control.DecisionResult{}, databaseError("postgres.ResolveDecision.selectRun", err)
	}
	target := runstate.StateCancelling
	decisionStatus := control.DecisionRejected
	sprintStatus := control.SprintRejected
	eventType := "decision.rejected"
	if command.Approve {
		target = runstate.StateQueued
		decisionStatus = control.DecisionApproved
		sprintStatus = control.SprintApproved
		eventType = "decision.approved"
	}
	updatedRun, err := s.machine.Transition(run, target)
	if err != nil {
		return control.DecisionResult{}, err
	}
	updatedRun.UpdatedAt = postgresTimestamp(updatedRun.UpdatedAt)
	now := updatedRun.UpdatedAt
	decision.Status = string(decisionStatus)
	decision.Version++
	decision.DecidedBy = &metadata.ActorID
	decision.Reason = &command.Reason
	decision.DecidedAt = &now
	decision.UpdatedAt = now
	sprint.Status = string(sprintStatus)
	sprint.Version++
	sprint.PendingDecisionID = nil
	sprint.UpdatedAt = now
	tag, err := tx.Exec(ctx, `
		UPDATE forja.decisions
		SET status=$1, version=$2, decided_by=$3, reason=$4,
		    decided_at=$5, updated_at=$5
		WHERE tenant_id=$6 AND repository_id=$7 AND decision_id=$8 AND version=$9`,
		decision.Status, decision.Version, metadata.ActorID, command.Reason, now,
		s.tenantID, s.repositoryID, decision.DecisionID, command.ExpectedVersion,
	)
	if err != nil {
		return control.DecisionResult{}, databaseError("postgres.ResolveDecision.updateDecision", err)
	}
	if tag.RowsAffected() != 1 {
		return control.DecisionResult{}, fault.New(fault.CodeConflict, "postgres.ResolveDecision.updateDecision", "decision version changed concurrently")
	}
	tag, err = tx.Exec(ctx, `
		UPDATE forja.sprints SET status=$1, version=$2, updated_at=$3
		WHERE tenant_id=$4 AND repository_id=$5 AND sprint_id=$6::uuid AND version=$7`,
		sprint.Status, sprint.Version, now, s.tenantID, s.repositoryID,
		sprintID.UUID(), sprint.Version-1,
	)
	if err != nil {
		return control.DecisionResult{}, databaseError("postgres.ResolveDecision.updateSprint", err)
	}
	if tag.RowsAffected() != 1 {
		return control.DecisionResult{}, fault.New(fault.CodeConflict, "postgres.ResolveDecision.updateSprint", "sprint version changed concurrently")
	}
	tag, err = tx.Exec(ctx, `
		UPDATE forja.runs SET state=$1, version=$2, updated_at=$3
		WHERE tenant_id=$4 AND repository_id=$5 AND run_id=$6 AND version=$7`,
		updatedRun.State, updatedRun.Version, now, s.tenantID, s.repositoryID,
		updatedRun.RunID, run.Version,
	)
	if err != nil {
		return control.DecisionResult{}, databaseError("postgres.ResolveDecision.updateRun", err)
	}
	if tag.RowsAffected() != 1 {
		return control.DecisionResult{}, fault.New(fault.CodeConflict, "postgres.ResolveDecision.updateRun", "run version changed concurrently")
	}
	if err := s.appendControlEvent(ctx, tx, "decision", decision.DecisionID, decision.Version, eventType, now, decision, metadata); err != nil {
		return control.DecisionResult{}, err
	}
	if err := s.appendControlEvent(ctx, tx, "sprint", sprint.SprintID, sprint.Version, "sprint.decision_resolved", now, sprint, metadata); err != nil {
		return control.DecisionResult{}, err
	}
	if err := s.appendRunEvent(ctx, tx, "run.transitioned", updatedRun, metadata); err != nil {
		return control.DecisionResult{}, err
	}
	if err := s.appendSuccessToolAudit(ctx, tx, metadata, scope, now, false); err != nil {
		return control.DecisionResult{}, err
	}
	result := control.DecisionResult{Sprint: sprint, Decision: decision, Run: updatedRun}
	if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 200, result); err != nil {
		return control.DecisionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return control.DecisionResult{}, databaseError("postgres.ResolveDecision.commit", err)
	}
	return result, nil
}

func (s *Store) RecordToolAudit(ctx context.Context, record control.AuditRecord) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return databaseError("postgres.RecordToolAudit.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	metadata := runstate.CommandMetadata{
		IdempotencyKey: record.IdempotencyKey, ActorType: record.ActorType,
		ActorID: record.ActorID, CorrelationID: record.CorrelationID, CausationID: record.CausationID,
	}
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return err
	}
	if err := s.appendToolAudit(ctx, tx, record, metadata); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return databaseError("postgres.RecordToolAudit.commit", err)
	}
	return nil
}

func (s *Store) appendSuccessToolAudit(
	ctx context.Context,
	tx pgx.Tx,
	metadata runstate.CommandMetadata,
	commandScope string,
	occurredAt time.Time,
	replay bool,
) error {
	if metadata.AuditToolName == "" {
		return nil
	}
	return s.appendToolAudit(ctx, tx, control.AuditRecord{
		ToolName:       metadata.AuditToolName,
		Outcome:        "succeeded",
		ActorType:      metadata.ActorType,
		ActorID:        metadata.ActorID,
		CorrelationID:  metadata.CorrelationID,
		CausationID:    metadata.CausationID,
		IdempotencyKey: metadata.IdempotencyKey,
		CommandScope:   commandScope,
		Replay:         replay,
		OccurredAt:     occurredAt,
	}, metadata)
}

func (s *Store) appendToolAudit(
	ctx context.Context,
	tx pgx.Tx,
	record control.AuditRecord,
	metadata runstate.CommandMetadata,
) error {
	if strings.TrimSpace(record.ToolName) == "" || strings.TrimSpace(record.Outcome) == "" ||
		strings.TrimSpace(record.CorrelationID) == "" ||
		strings.TrimSpace(record.IdempotencyKey) == "" || strings.TrimSpace(record.ActorID) == "" {
		return fault.New(
			fault.CodeInvalidArgument,
			"postgres.appendToolAudit",
			"tool, outcome, actor, correlation, and idempotency key are required",
		)
	}
	if record.OccurredAt.IsZero() {
		record.OccurredAt = postgresTimestamp(s.clock.Now())
	} else {
		record.OccurredAt = postgresTimestamp(record.OccurredAt)
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return fault.Wrap(fault.CodeInternal, "postgres.appendToolAudit", "encode audit", err)
	}
	auditID, err := newPrefixedID("audit")
	if err != nil {
		return fault.Wrap(fault.CodeInternal, "postgres.appendToolAudit", "generate audit ID", err)
	}
	return s.appendEvent(
		ctx,
		tx,
		"audit",
		auditID,
		1,
		"mcp.tool."+record.Outcome,
		record.OccurredAt,
		payload,
		metadata,
	)
}

func (s *Store) appendControlEvent(
	ctx context.Context,
	tx pgx.Tx,
	aggregateType, aggregateID string,
	version int,
	eventType string,
	occurredAt time.Time,
	payload any,
	metadata runstate.CommandMetadata,
) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fault.Wrap(fault.CodeInternal, "postgres.appendControlEvent", "encode event", err)
	}
	return s.appendEvent(
		ctx,
		tx,
		aggregateType,
		aggregateID,
		version,
		eventType,
		occurredAt,
		data,
		metadata,
	)
}

func scanSprint(row rowScanner) (control.Sprint, error) {
	var sprint control.Sprint
	err := row.Scan(
		&sprint.SprintID, &sprint.SequenceNumber, &sprint.Title, &sprint.Objective,
		&sprint.Status, &sprint.Version, &sprint.RunID, &sprint.PendingDecisionID,
		&sprint.CreatedAt, &sprint.UpdatedAt,
	)
	sprint.SchemaVersion = "1.0"
	sprint.CreatedAt = sprint.CreatedAt.UTC()
	sprint.UpdatedAt = sprint.UpdatedAt.UTC()
	return sprint, err
}

func scanDecision(row rowScanner) (control.Decision, error) {
	var decision control.Decision
	err := row.Scan(
		&decision.DecisionID, &decision.SprintID, &decision.RunID, &decision.Action,
		&decision.RiskClass, &decision.Status, &decision.Version, &decision.RequestedBy,
		&decision.DecidedBy, &decision.Reason, &decision.CreatedAt, &decision.UpdatedAt,
		&decision.DecidedAt,
	)
	decision.SchemaVersion = "1.0"
	decision.CreatedAt = decision.CreatedAt.UTC()
	decision.UpdatedAt = decision.UpdatedAt.UTC()
	if decision.DecidedAt != nil {
		value := decision.DecidedAt.UTC()
		decision.DecidedAt = &value
	}
	return decision, err
}

func loadControlReplay[T any](
	ctx context.Context,
	tx pgx.Tx,
	tenantID, scope, key string,
	requestHash []byte,
) (T, bool, error) {
	var zero T
	var storedHash, response []byte
	err := tx.QueryRow(ctx, `
		SELECT request_hash, response_body
		FROM forja.idempotency_keys
		WHERE tenant_id=$1 AND scope=$2 AND idempotency_key=$3`,
		tenantID, scope, key,
	).Scan(&storedHash, &response)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, false, nil
	}
	if err != nil {
		return zero, false, databaseError("postgres.loadControlReplay", err)
	}
	if !bytes.Equal(storedHash, requestHash) {
		return zero, false, fault.New(fault.CodeConflict, "postgres.loadControlReplay", "idempotency key was already used for a different command")
	}
	decoder := json.NewDecoder(bytes.NewReader(response))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return zero, false, fault.Wrap(fault.CodeInternal, "postgres.loadControlReplay", "decode stored response", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return zero, false, fault.New(fault.CodeInternal, "postgres.loadControlReplay", "stored response contains multiple documents")
	}
	return value, true, nil
}

func saveControlReplay(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, scope, key string,
	requestHash []byte,
	status int,
	response any,
) error {
	data, err := json.Marshal(response)
	if err != nil {
		return fault.Wrap(fault.CodeInternal, "postgres.saveControlReplay", "encode response", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO forja.idempotency_keys (
			tenant_id, scope, idempotency_key, request_hash, response_status, response_body
		) VALUES ($1, $2, $3, $4, $5, $6)`,
		tenantID, scope, key, requestHash, status, data,
	)
	if err != nil {
		return databaseError("postgres.saveControlReplay", err)
	}
	return nil
}

var _ control.Repository = (*Store)(nil)

func resumeTarget(run contracts.Run) (runstate.State, error) {
	switch run.State {
	case string(runstate.StateFailedRetryable):
		return runstate.StateQueued, nil
	case string(runstate.StateAwaitingDecision):
		return runstate.StateRunning, nil
	default:
		return "", fault.New(
			fault.CodeConflict,
			"postgres.resumeTarget",
			"only failed_retryable or awaiting_decision runs can resume",
		)
	}
}
