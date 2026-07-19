package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

func (s *Store) ProposeMemory(
	ctx context.Context,
	candidate contracts.MemoryCandidate,
	metadata runstate.CommandMetadata,
) (contracts.MemoryCandidate, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return contracts.MemoryCandidate{}, err
	}
	now := postgresTimestamp(s.clock.Now())
	candidate.SchemaVersion = contracts.KnowledgeSchemaVersion
	candidate.TenantID = "tenant_" + s.tenantID
	candidate.RepositoryID = "repo_" + s.repositoryID
	candidate.Status = "proposed"
	candidate.Version = 1
	candidate.ProposedAt = now
	candidate.MemoryID = nil
	candidate.ResolvedBy = nil
	candidate.ResolutionReason = nil
	candidate.ResolvedAt = nil
	if candidate.ProposedBy != metadata.ActorID {
		return contracts.MemoryCandidate{}, fault.New(fault.CodeInvalidArgument, "postgres.ProposeMemory", "candidate proposer does not match command actor")
	}
	if err := contracts.ValidateMemoryCandidate(candidate); err != nil {
		return contracts.MemoryCandidate{}, fault.Wrap(fault.CodeInvalidArgument, "postgres.ProposeMemory", "validate candidate", err)
	}
	encoded, _ := json.Marshal(candidate)
	scope := "memory_candidate_propose:" + s.repositoryID + ":" + candidate.CandidateID
	requestHash := hashKnowledgeCommand(metadata, string(encoded))
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contracts.MemoryCandidate{}, databaseError("postgres.ProposeMemory.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return contracts.MemoryCandidate{}, err
	}
	if replay, found, err := loadControlReplay[contracts.MemoryCandidate](ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return contracts.MemoryCandidate{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return contracts.MemoryCandidate{}, databaseError("postgres.ProposeMemory.replay", err)
		}
		return replay, nil
	}
	conversation, _, err := loadConversation(ctx, tx, s.tenantID, s.repositoryID, candidate.ConversationID, true)
	if err != nil {
		return contracts.MemoryCandidate{}, databaseError("postgres.ProposeMemory.conversation", err)
	}
	if conversation.Status == "tombstoned" {
		return contracts.MemoryCandidate{}, fault.New(fault.CodeConflict, "postgres.ProposeMemory", "tombstoned conversation cannot produce memory")
	}
	for _, messageID := range candidate.SourceMessageIDs {
		var exact bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM forja.messages
				WHERE tenant_id=$1 AND repository_id=$2
				  AND conversation_id=$3 AND message_id=$4
			)`, s.tenantID, s.repositoryID, candidate.ConversationID, messageID,
		).Scan(&exact); err != nil {
			return contracts.MemoryCandidate{}, databaseError("postgres.ProposeMemory.source", err)
		}
		if !exact {
			return contracts.MemoryCandidate{}, fault.New(fault.CodeInvalidArgument, "postgres.ProposeMemory", "candidate source message is outside its conversation")
		}
	}
	if err := verifyExactArtifact(ctx, tx, s.tenantID, s.repositoryID, candidate.ProposedArtifactID, candidate.ProposedContentHash, -1, ""); err != nil {
		return contracts.MemoryCandidate{}, err
	}
	digest, _ := decodeContentHash(candidate.ProposedContentHash)
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.memory_candidates (
			tenant_id, repository_id, candidate_id, conversation_id, kind,
			proposed_artifact_id, proposed_content_sha256, status, version,
			proposed_by, proposed_at, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'proposed', 1, $8, $9, $10)`,
		s.tenantID, s.repositoryID, candidate.CandidateID, candidate.ConversationID,
		candidate.Kind, candidate.ProposedArtifactID, digest, candidate.ProposedBy,
		candidate.ProposedAt, candidate.ExpiresAt,
	); err != nil {
		return contracts.MemoryCandidate{}, databaseError("postgres.ProposeMemory.insert", err)
	}
	for ordinal, messageID := range candidate.SourceMessageIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO forja.memory_candidate_sources (
				tenant_id, repository_id, candidate_id, ordinal, message_id
			) VALUES ($1, $2, $3, $4, $5)`,
			s.tenantID, s.repositoryID, candidate.CandidateID, ordinal, messageID,
		); err != nil {
			return contracts.MemoryCandidate{}, databaseError("postgres.ProposeMemory.source", err)
		}
	}
	if err := s.appendKnowledgeEvent(ctx, tx, "memory_candidate", candidate.CandidateID, 1, "memory_candidate.proposed", now, candidate, metadata); err != nil {
		return contracts.MemoryCandidate{}, err
	}
	if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 201, candidate); err != nil {
		return contracts.MemoryCandidate{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.MemoryCandidate{}, databaseError("postgres.ProposeMemory.commit", err)
	}
	return candidate, nil
}

func (s *Store) PromoteMemory(
	ctx context.Context,
	command persistence.MemoryPromotionCommand,
	metadata runstate.CommandMetadata,
) (contracts.MemoryRecord, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return contracts.MemoryRecord{}, err
	}
	memory := command.Memory
	if memory.PromotedBy != metadata.ActorID ||
		(memory.AuthorityClass == "human_approved" && metadata.ActorType != "human") ||
		(memory.AuthorityClass == "policy_approved" && metadata.ActorType != "system") ||
		!slices.Contains([]string{"human_approved", "policy_approved"}, memory.AuthorityClass) {
		return contracts.MemoryRecord{}, fault.New(fault.CodePermissionDenied, "postgres.PromoteMemory", "actor cannot promote durable memory")
	}
	now := postgresTimestamp(s.clock.Now())
	memory.SchemaVersion = contracts.KnowledgeSchemaVersion
	memory.TenantID = "tenant_" + s.tenantID
	memory.RepositoryID = "repo_" + s.repositoryID
	memory.Status = "active"
	memory.Version = 1
	memory.PromotedAt = now
	memory.SupersededBy = nil
	memory.SupersededAt = nil
	memory.ExpiredAt = nil
	memory.TombstonedAt = nil
	if err := contracts.ValidateMemoryRecord(memory); err != nil {
		return contracts.MemoryRecord{}, fault.Wrap(fault.CodeInvalidArgument, "postgres.PromoteMemory", "validate memory", err)
	}
	command.Memory = memory
	encoded, _ := json.Marshal(command)
	scope := "memory_promote:" + s.repositoryID + ":" + memory.MemoryID
	requestHash := hashKnowledgeCommand(metadata, string(encoded))
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contracts.MemoryRecord{}, databaseError("postgres.PromoteMemory.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return contracts.MemoryRecord{}, err
	}
	if replay, found, err := loadControlReplay[contracts.MemoryRecord](ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return contracts.MemoryRecord{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return contracts.MemoryRecord{}, databaseError("postgres.PromoteMemory.replay", err)
		}
		return replay, nil
	}
	candidate, err := loadMemoryCandidate(ctx, tx, s.tenantID, s.repositoryID, memory.SourceCandidateID, true)
	if err != nil {
		return contracts.MemoryRecord{}, databaseError("postgres.PromoteMemory.candidate", err)
	}
	if candidate.Status != "proposed" || candidate.Version != command.ExpectedCandidateVersion ||
		(candidate.ExpiresAt != nil && !candidate.ExpiresAt.After(now)) {
		return contracts.MemoryRecord{}, fault.New(fault.CodeConflict, "postgres.PromoteMemory", "candidate is not a matching live proposal")
	}
	if candidate.Kind != memory.Kind || candidate.ProposedArtifactID != memory.ContentArtifactID ||
		candidate.ProposedContentHash != memory.ContentHash {
		return contracts.MemoryRecord{}, fault.New(fault.CodeInvalidArgument, "postgres.PromoteMemory", "memory differs from its candidate")
	}
	lockedSuperseded := make([]contracts.MemoryRecord, 0, len(memory.Supersedes))
	orderedIDs := append([]string(nil), memory.Supersedes...)
	sort.Strings(orderedIDs)
	for _, memoryID := range orderedIDs {
		prior, err := loadMemoryRecord(ctx, tx, s.tenantID, s.repositoryID, memoryID, true)
		if err != nil {
			return contracts.MemoryRecord{}, databaseError("postgres.PromoteMemory.superseded", err)
		}
		if prior.Status != "active" || (prior.ExpiresAt != nil && !prior.ExpiresAt.After(now)) {
			return contracts.MemoryRecord{}, fault.New(fault.CodeConflict, "postgres.PromoteMemory", "superseded memory is not active")
		}
		lockedSuperseded = append(lockedSuperseded, prior)
	}
	digest, _ := decodeContentHash(memory.ContentHash)
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.memory_records (
			tenant_id, repository_id, memory_id, source_candidate_id, kind,
			status, version, content_artifact_id, content_sha256, authority_class,
			promoted_by, promotion_reason, promoted_at, expires_at
		) VALUES ($1, $2, $3, $4, $5, 'active', 1, $6, $7, $8, $9, $10, $11, $12)`,
		s.tenantID, s.repositoryID, memory.MemoryID, memory.SourceCandidateID,
		memory.Kind, memory.ContentArtifactID, digest, memory.AuthorityClass,
		memory.PromotedBy, memory.PromotionReason, now, memory.ExpiresAt,
	); err != nil {
		return contracts.MemoryRecord{}, databaseError("postgres.PromoteMemory.insert", err)
	}
	for _, prior := range lockedSuperseded {
		prior.Status = "superseded"
		prior.Version++
		prior.SupersededBy = &memory.MemoryID
		prior.SupersededAt = &now
		if _, err := tx.Exec(ctx, `
			UPDATE forja.memory_records
			SET status='superseded', version=$4, superseded_by=$5, superseded_at=$6
			WHERE tenant_id=$1 AND repository_id=$2 AND memory_id=$3`,
			s.tenantID, s.repositoryID, prior.MemoryID, prior.Version, memory.MemoryID, now,
		); err != nil {
			return contracts.MemoryRecord{}, databaseError("postgres.PromoteMemory.supersede", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO forja.memory_supersessions (
				tenant_id, repository_id, memory_id, superseded_memory_id, created_at
			) VALUES ($1, $2, $3, $4, $5)`,
			s.tenantID, s.repositoryID, memory.MemoryID, prior.MemoryID, now,
		); err != nil {
			return contracts.MemoryRecord{}, databaseError("postgres.PromoteMemory.edge", err)
		}
		if err := s.appendKnowledgeEvent(ctx, tx, "memory", prior.MemoryID, prior.Version, "memory.superseded", now, prior, metadata); err != nil {
			return contracts.MemoryRecord{}, err
		}
	}
	candidate.Status = "promoted"
	candidate.Version++
	candidate.MemoryID = &memory.MemoryID
	candidate.ResolvedBy = &memory.PromotedBy
	candidate.ResolutionReason = &memory.PromotionReason
	candidate.ResolvedAt = &now
	if _, err := tx.Exec(ctx, `
		UPDATE forja.memory_candidates
		SET status='promoted', version=$4, memory_id=$5, resolved_by=$6,
			resolution_reason=$7, resolved_at=$8
		WHERE tenant_id=$1 AND repository_id=$2 AND candidate_id=$3`,
		s.tenantID, s.repositoryID, candidate.CandidateID, candidate.Version,
		memory.MemoryID, memory.PromotedBy, memory.PromotionReason, now,
	); err != nil {
		return contracts.MemoryRecord{}, databaseError("postgres.PromoteMemory.resolveCandidate", err)
	}
	if err := s.appendKnowledgeEvent(ctx, tx, "memory_candidate", candidate.CandidateID, candidate.Version, "memory_candidate.promoted", now, candidate, metadata); err != nil {
		return contracts.MemoryRecord{}, err
	}
	if err := s.appendKnowledgeEvent(ctx, tx, "memory", memory.MemoryID, 1, "memory.promoted", now, memory, metadata); err != nil {
		return contracts.MemoryRecord{}, err
	}
	if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 201, memory); err != nil {
		return contracts.MemoryRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.MemoryRecord{}, databaseError("postgres.PromoteMemory.commit", err)
	}
	return memory, nil
}

func (s *Store) ResolveMemoryCandidate(
	ctx context.Context,
	candidateID string,
	expectedVersion int,
	status string,
	reason string,
	metadata runstate.CommandMetadata,
) (contracts.MemoryCandidate, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return contracts.MemoryCandidate{}, err
	}
	if metadata.ActorType != "human" && metadata.ActorType != "system" ||
		(status != "rejected" && status != "expired") || len(reason) < 1 || len(reason) > 2000 {
		return contracts.MemoryCandidate{}, fault.New(fault.CodePermissionDenied, "postgres.ResolveMemoryCandidate", "candidate resolution authority is invalid")
	}
	scope := "memory_candidate_resolve:" + s.repositoryID + ":" + candidateID
	requestHash := hashKnowledgeCommand(metadata, candidateID, fmt.Sprint(expectedVersion), status, reason)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contracts.MemoryCandidate{}, databaseError("postgres.ResolveMemoryCandidate.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return contracts.MemoryCandidate{}, err
	}
	if replay, found, err := loadControlReplay[contracts.MemoryCandidate](ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return contracts.MemoryCandidate{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return contracts.MemoryCandidate{}, databaseError("postgres.ResolveMemoryCandidate.replay", err)
		}
		return replay, nil
	}
	candidate, err := loadMemoryCandidate(ctx, tx, s.tenantID, s.repositoryID, candidateID, true)
	if err != nil {
		return contracts.MemoryCandidate{}, databaseError("postgres.ResolveMemoryCandidate.load", err)
	}
	if candidate.Status != "proposed" || candidate.Version != expectedVersion {
		return contracts.MemoryCandidate{}, fault.New(fault.CodeConflict, "postgres.ResolveMemoryCandidate", "candidate is not a matching proposal")
	}
	now := postgresTimestamp(s.clock.Now())
	candidate.Status = status
	candidate.Version++
	candidate.ResolvedBy = &metadata.ActorID
	candidate.ResolutionReason = &reason
	candidate.ResolvedAt = &now
	if _, err := tx.Exec(ctx, `
		UPDATE forja.memory_candidates SET status=$4, version=$5, resolved_by=$6,
			resolution_reason=$7, resolved_at=$8
		WHERE tenant_id=$1 AND repository_id=$2 AND candidate_id=$3`,
		s.tenantID, s.repositoryID, candidateID, status, candidate.Version,
		metadata.ActorID, reason, now,
	); err != nil {
		return contracts.MemoryCandidate{}, databaseError("postgres.ResolveMemoryCandidate.update", err)
	}
	if err := s.appendKnowledgeEvent(ctx, tx, "memory_candidate", candidateID, candidate.Version, "memory_candidate."+status, now, candidate, metadata); err != nil {
		return contracts.MemoryCandidate{}, err
	}
	if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 200, candidate); err != nil {
		return contracts.MemoryCandidate{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.MemoryCandidate{}, databaseError("postgres.ResolveMemoryCandidate.commit", err)
	}
	return candidate, nil
}

func (s *Store) TransitionMemory(
	ctx context.Context,
	memoryID string,
	expectedVersion int,
	status string,
	metadata runstate.CommandMetadata,
) (contracts.MemoryRecord, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return contracts.MemoryRecord{}, err
	}
	if metadata.ActorType != "human" && metadata.ActorType != "system" ||
		(status != "expired" && status != "tombstoned") {
		return contracts.MemoryRecord{}, fault.New(fault.CodePermissionDenied, "postgres.TransitionMemory", "memory lifecycle authority is invalid")
	}
	scope := "memory_transition:" + s.repositoryID + ":" + memoryID + ":" + status
	requestHash := hashKnowledgeCommand(metadata, memoryID, fmt.Sprint(expectedVersion), status)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contracts.MemoryRecord{}, databaseError("postgres.TransitionMemory.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return contracts.MemoryRecord{}, err
	}
	if replay, found, err := loadControlReplay[contracts.MemoryRecord](ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return contracts.MemoryRecord{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return contracts.MemoryRecord{}, databaseError("postgres.TransitionMemory.replay", err)
		}
		return replay, nil
	}
	memory, err := loadMemoryRecord(ctx, tx, s.tenantID, s.repositoryID, memoryID, true)
	if err != nil {
		return contracts.MemoryRecord{}, databaseError("postgres.TransitionMemory.load", err)
	}
	if memory.Version != expectedVersion || memory.Status == "tombstoned" ||
		status == "expired" && memory.Status != "active" {
		return contracts.MemoryRecord{}, fault.New(fault.CodeConflict, "postgres.TransitionMemory", "memory is not a matching live version")
	}
	now := postgresTimestamp(s.clock.Now())
	memory.Status = status
	memory.Version++
	if status == "expired" {
		memory.ExpiredAt = &now
	} else {
		memory.TombstonedAt = &now
	}
	if _, err := tx.Exec(ctx, `
		UPDATE forja.memory_records
		SET status=$4, version=$5,
			expired_at=CASE WHEN $4='expired' THEN $6 ELSE expired_at END,
			tombstoned_at=CASE WHEN $4='tombstoned' THEN $6 ELSE tombstoned_at END
		WHERE tenant_id=$1 AND repository_id=$2 AND memory_id=$3`,
		s.tenantID, s.repositoryID, memoryID, status, memory.Version, now,
	); err != nil {
		return contracts.MemoryRecord{}, databaseError("postgres.TransitionMemory.update", err)
	}
	if err := s.appendKnowledgeEvent(ctx, tx, "memory", memoryID, memory.Version, "memory."+status, now, memory, metadata); err != nil {
		return contracts.MemoryRecord{}, err
	}
	if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 200, memory); err != nil {
		return contracts.MemoryRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.MemoryRecord{}, databaseError("postgres.TransitionMemory.commit", err)
	}
	return memory, nil
}

func (s *Store) ListActiveMemories(ctx context.Context, kind string, limit int) ([]contracts.MemoryRecord, error) {
	if (kind != "" && !slices.Contains([]string{"fact", "preference", "decision", "lesson"}, kind)) ||
		limit < 1 || limit > 500 {
		return nil, fault.New(fault.CodeInvalidArgument, "postgres.ListActiveMemories", "memory query is invalid")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, databaseError("postgres.ListActiveMemories.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		SELECT memory_id
		FROM forja.memory_records
		WHERE tenant_id=$1 AND repository_id=$2 AND status='active'
		  AND (expires_at IS NULL OR expires_at > clock_timestamp())
		  AND ($3='' OR kind=$3)
		ORDER BY promoted_at DESC, memory_id
		LIMIT $4`, s.tenantID, s.repositoryID, kind, limit)
	if err != nil {
		return nil, databaseError("postgres.ListActiveMemories", err)
	}
	defer rows.Close()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, databaseError("postgres.ListActiveMemories.scan", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError("postgres.ListActiveMemories.rows", err)
	}
	rows.Close()
	result := make([]contracts.MemoryRecord, 0, len(ids))
	for _, id := range ids {
		value, err := loadMemoryRecord(ctx, tx, s.tenantID, s.repositoryID, id, false)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, databaseError("postgres.ListActiveMemories.commit", err)
	}
	return result, nil
}

func loadMemoryCandidate(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, repositoryID, candidateID string,
	forUpdate bool,
) (contracts.MemoryCandidate, error) {
	query := `
		SELECT candidate_id, conversation_id, kind, proposed_artifact_id,
		       encode(proposed_content_sha256, 'hex'), status, version,
		       proposed_by, proposed_at, expires_at, memory_id, resolved_by,
		       resolution_reason, resolved_at
		FROM forja.memory_candidates
		WHERE tenant_id=$1 AND repository_id=$2 AND candidate_id=$3`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var value contracts.MemoryCandidate
	var digestHex string
	err := tx.QueryRow(ctx, query, tenantID, repositoryID, candidateID).Scan(
		&value.CandidateID, &value.ConversationID, &value.Kind,
		&value.ProposedArtifactID, &digestHex, &value.Status, &value.Version,
		&value.ProposedBy, &value.ProposedAt, &value.ExpiresAt, &value.MemoryID,
		&value.ResolvedBy, &value.ResolutionReason, &value.ResolvedAt,
	)
	if err != nil {
		return contracts.MemoryCandidate{}, err
	}
	rows, err := tx.Query(ctx, `
		SELECT message_id FROM forja.memory_candidate_sources
		WHERE tenant_id=$1 AND repository_id=$2 AND candidate_id=$3
		ORDER BY ordinal`, tenantID, repositoryID, candidateID)
	if err != nil {
		return contracts.MemoryCandidate{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return contracts.MemoryCandidate{}, err
		}
		value.SourceMessageIDs = append(value.SourceMessageIDs, id)
	}
	value.SchemaVersion = contracts.KnowledgeSchemaVersion
	value.TenantID = "tenant_" + tenantID
	value.RepositoryID = "repo_" + repositoryID
	value.ProposedContentHash = "sha256:" + digestHex
	value.ProposedAt = value.ProposedAt.UTC()
	value.ExpiresAt = utcTimePointer(value.ExpiresAt)
	value.ResolvedAt = utcTimePointer(value.ResolvedAt)
	return value, rows.Err()
}

func loadMemoryRecord(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, repositoryID, memoryID string,
	forUpdate bool,
) (contracts.MemoryRecord, error) {
	query := `
		SELECT memory_id, source_candidate_id, kind, status, version,
		       content_artifact_id, encode(content_sha256, 'hex'), authority_class,
		       promoted_by, promotion_reason, promoted_at, expires_at,
		       superseded_by, superseded_at, expired_at, tombstoned_at
		FROM forja.memory_records
		WHERE tenant_id=$1 AND repository_id=$2 AND memory_id=$3`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var value contracts.MemoryRecord
	var digestHex string
	err := tx.QueryRow(ctx, query, tenantID, repositoryID, memoryID).Scan(
		&value.MemoryID, &value.SourceCandidateID, &value.Kind, &value.Status,
		&value.Version, &value.ContentArtifactID, &digestHex, &value.AuthorityClass,
		&value.PromotedBy, &value.PromotionReason, &value.PromotedAt, &value.ExpiresAt,
		&value.SupersededBy, &value.SupersededAt, &value.ExpiredAt, &value.TombstonedAt,
	)
	if err != nil {
		return contracts.MemoryRecord{}, err
	}
	rows, err := tx.Query(ctx, `
		SELECT superseded_memory_id FROM forja.memory_supersessions
		WHERE tenant_id=$1 AND repository_id=$2 AND memory_id=$3
		ORDER BY superseded_memory_id`, tenantID, repositoryID, memoryID)
	if err != nil {
		return contracts.MemoryRecord{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return contracts.MemoryRecord{}, err
		}
		value.Supersedes = append(value.Supersedes, id)
	}
	value.SchemaVersion = contracts.KnowledgeSchemaVersion
	value.TenantID = "tenant_" + tenantID
	value.RepositoryID = "repo_" + repositoryID
	value.ContentHash = "sha256:" + digestHex
	value.PromotedAt = value.PromotedAt.UTC()
	value.ExpiresAt = utcTimePointer(value.ExpiresAt)
	value.SupersededAt = utcTimePointer(value.SupersededAt)
	value.ExpiredAt = utcTimePointer(value.ExpiredAt)
	value.TombstonedAt = utcTimePointer(value.TombstonedAt)
	return value, rows.Err()
}

var _ persistence.KnowledgeRepository = (*Store)(nil)
