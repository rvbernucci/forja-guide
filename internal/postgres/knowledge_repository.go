package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

func (s *Store) CreateConversation(
	ctx context.Context,
	value contracts.Conversation,
	metadata runstate.CommandMetadata,
) (contracts.Conversation, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return contracts.Conversation{}, err
	}
	now := postgresTimestamp(s.clock.Now())
	value.SchemaVersion = contracts.KnowledgeSchemaVersion
	value.TenantID = "tenant_" + s.tenantID
	value.RepositoryID = "repo_" + s.repositoryID
	value.Status = "active"
	value.Version = 1
	value.CreatedAt = now
	value.UpdatedAt = now
	value.ClosedAt = nil
	value.TombstonedAt = nil
	value.TranscriptArtifactID = nil
	value.TranscriptManifestID = nil
	if value.CreatedBy != metadata.ActorID {
		return contracts.Conversation{}, fault.New(fault.CodeInvalidArgument, "postgres.CreateConversation", "conversation creator does not match the command actor")
	}
	if err := contracts.ValidateConversation(value); err != nil {
		return contracts.Conversation{}, fault.Wrap(fault.CodeInvalidArgument, "postgres.CreateConversation", "validate conversation", err)
	}
	scope := "conversation_create:" + s.repositoryID + ":" + value.ConversationID
	requestHash := hashKnowledgeCommand(metadata, value.ConversationID, value.RetentionClass, value.CreatedBy)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contracts.Conversation{}, databaseError("postgres.CreateConversation.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return contracts.Conversation{}, err
	}
	if replay, found, err := loadControlReplay[contracts.Conversation](ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return contracts.Conversation{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return contracts.Conversation{}, databaseError("postgres.CreateConversation.replay", err)
		}
		return replay, nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.conversations (
			tenant_id, repository_id, conversation_id, status, version,
			retention_class, created_by, created_at, updated_at
		) VALUES ($1, $2, $3, 'active', 1, $4, $5, $6, $6)`,
		s.tenantID, s.repositoryID, value.ConversationID,
		value.RetentionClass, value.CreatedBy, now,
	); err != nil {
		return contracts.Conversation{}, databaseError("postgres.CreateConversation.insert", err)
	}
	if err := s.appendKnowledgeEvent(ctx, tx, "conversation", value.ConversationID, value.Version, "conversation.created", now, value, metadata); err != nil {
		return contracts.Conversation{}, err
	}
	if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 201, value); err != nil {
		return contracts.Conversation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.Conversation{}, databaseError("postgres.CreateConversation.commit", err)
	}
	return value, nil
}

func (s *Store) AppendMessage(
	ctx context.Context,
	draft persistence.MessageDraft,
	metadata runstate.CommandMetadata,
) (contracts.Message, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return contracts.Message{}, err
	}
	if draft.Citations == nil {
		draft.Citations = []contracts.Citation{}
	}
	if draft.AuthorID != metadata.ActorID {
		return contracts.Message{}, fault.New(fault.CodeInvalidArgument, "postgres.AppendMessage", "message author does not match the command actor")
	}
	requestPayload, err := json.Marshal(draft)
	if err != nil {
		return contracts.Message{}, fault.Wrap(fault.CodeInvalidArgument, "postgres.AppendMessage", "encode message draft", err)
	}
	scope := "message_append:" + s.repositoryID + ":" + draft.MessageID
	requestHash := hashKnowledgeCommand(metadata, string(requestPayload))
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contracts.Message{}, databaseError("postgres.AppendMessage.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return contracts.Message{}, err
	}
	if replay, found, err := loadControlReplay[contracts.Message](ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return contracts.Message{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return contracts.Message{}, databaseError("postgres.AppendMessage.replay", err)
		}
		return replay, nil
	}
	conversation, manifestID, err := loadConversation(ctx, tx, s.tenantID, s.repositoryID, draft.ConversationID, true)
	_ = manifestID
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.Message{}, fault.New(fault.CodeNotFound, "postgres.AppendMessage", "conversation was not found")
	}
	if err != nil {
		return contracts.Message{}, err
	}
	if conversation.Status != "active" {
		return contracts.Message{}, fault.New(fault.CodeConflict, "postgres.AppendMessage", "conversation is not active")
	}
	var sequence int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(max(sequence_number), 0) + 1
		FROM forja.messages
		WHERE tenant_id=$1 AND repository_id=$2 AND conversation_id=$3`,
		s.tenantID, s.repositoryID, draft.ConversationID,
	).Scan(&sequence); err != nil {
		return contracts.Message{}, databaseError("postgres.AppendMessage.sequence", err)
	}
	if draft.SupersedesMessageID != nil {
		var sameConversation bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM forja.messages
				WHERE tenant_id=$1 AND repository_id=$2 AND message_id=$3 AND conversation_id=$4
			)`, s.tenantID, s.repositoryID, *draft.SupersedesMessageID, draft.ConversationID,
		).Scan(&sameConversation); err != nil {
			return contracts.Message{}, databaseError("postgres.AppendMessage.supersedes", err)
		}
		if !sameConversation {
			return contracts.Message{}, fault.New(fault.CodeInvalidArgument, "postgres.AppendMessage", "superseded message is outside the conversation")
		}
	}
	if err := verifyMessageArtifactReferences(ctx, tx, s.tenantID, s.repositoryID, draft.ContentParts, draft.Citations); err != nil {
		return contracts.Message{}, err
	}
	contentHash, err := contracts.ComputeMessageContentHash(draft.ContentParts, draft.Citations)
	if err != nil {
		return contracts.Message{}, fault.Wrap(fault.CodeInvalidArgument, "postgres.AppendMessage", "hash message references", err)
	}
	now := postgresTimestamp(s.clock.Now())
	message := contracts.Message{
		MessageID: draft.MessageID, SchemaVersion: contracts.KnowledgeSchemaVersion,
		TenantID: "tenant_" + s.tenantID, RepositoryID: "repo_" + s.repositoryID,
		ConversationID: draft.ConversationID, SequenceNumber: sequence,
		Role: draft.Role, AuthorID: draft.AuthorID, ContentHash: contentHash,
		SupersedesMessageID: draft.SupersedesMessageID,
		ContentParts:        draft.ContentParts, Citations: draft.Citations, CreatedAt: now,
	}
	if err := contracts.ValidateMessage(message); err != nil {
		return contracts.Message{}, fault.Wrap(fault.CodeInvalidArgument, "postgres.AppendMessage", "validate message", err)
	}
	digest, _ := decodeContentHash(message.ContentHash)
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.messages (
			tenant_id, repository_id, message_id, conversation_id, sequence_number,
			role, author_id, content_sha256, supersedes_message_id, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		s.tenantID, s.repositoryID, message.MessageID, message.ConversationID,
		message.SequenceNumber, message.Role, message.AuthorID, digest,
		message.SupersedesMessageID, now,
	); err != nil {
		return contracts.Message{}, databaseError("postgres.AppendMessage.insert", err)
	}
	for _, part := range message.ContentParts {
		partDigest, _ := decodeContentHash(part.ContentHash)
		if _, err := tx.Exec(ctx, `
			INSERT INTO forja.message_parts (
				tenant_id, repository_id, message_id, part_id, ordinal, kind,
				artifact_id, content_sha256, media_type, size_bytes
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			s.tenantID, s.repositoryID, message.MessageID, part.PartID, part.Ordinal,
			part.Kind, part.ArtifactID, partDigest, part.MediaType, part.SizeBytes,
		); err != nil {
			return contracts.Message{}, databaseError("postgres.AppendMessage.part", err)
		}
	}
	for _, citation := range message.Citations {
		citationDigest, _ := decodeContentHash(citation.SourceContentHash)
		if _, err := tx.Exec(ctx, `
			INSERT INTO forja.message_citations (
				tenant_id, repository_id, message_id, citation_id, ordinal,
				source_artifact_id, source_content_sha256, locator_kind, locator_value
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			s.tenantID, s.repositoryID, message.MessageID, citation.CitationID,
			citation.Ordinal, citation.SourceArtifactID, citationDigest,
			citation.Locator.Kind, citation.Locator.Value,
		); err != nil {
			return contracts.Message{}, databaseError("postgres.AppendMessage.citation", err)
		}
	}
	conversation.Version++
	conversation.UpdatedAt = now
	if _, err := tx.Exec(ctx, `
		UPDATE forja.conversations SET version=$4, updated_at=$5
		WHERE tenant_id=$1 AND repository_id=$2 AND conversation_id=$3`,
		s.tenantID, s.repositoryID, conversation.ConversationID, conversation.Version, now,
	); err != nil {
		return contracts.Message{}, databaseError("postgres.AppendMessage.conversation", err)
	}
	if err := s.appendKnowledgeEvent(ctx, tx, "message", message.MessageID, 1, "message.appended", now, message, metadata); err != nil {
		return contracts.Message{}, err
	}
	if err := s.appendKnowledgeEvent(ctx, tx, "conversation", conversation.ConversationID, conversation.Version, "conversation.message_appended", now, conversation, metadata); err != nil {
		return contracts.Message{}, err
	}
	if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 201, message); err != nil {
		return contracts.Message{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.Message{}, databaseError("postgres.AppendMessage.commit", err)
	}
	return message, nil
}

func (s *Store) CreateArtifactBundleManifest(
	ctx context.Context,
	manifest contracts.ArtifactBundleManifest,
	metadata runstate.CommandMetadata,
) (contracts.ArtifactBundleManifest, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return contracts.ArtifactBundleManifest{}, err
	}
	manifest.SchemaVersion = contracts.KnowledgeSchemaVersion
	manifest.TenantID = "tenant_" + s.tenantID
	manifest.RepositoryID = "repo_" + s.repositoryID
	manifest.CreatedAt = time.Time{}
	if manifest.CreatedBy != metadata.ActorID {
		return contracts.ArtifactBundleManifest{}, fault.New(fault.CodeInvalidArgument, "postgres.CreateArtifactBundleManifest", "manifest creator does not match command actor")
	}
	requestBytes, err := json.Marshal(manifest)
	if err != nil {
		return contracts.ArtifactBundleManifest{}, fault.Wrap(fault.CodeInvalidArgument, "postgres.CreateArtifactBundleManifest", "encode manifest intent", err)
	}
	scope := "artifact_manifest_create:" + s.repositoryID + ":" + manifest.ManifestID
	requestHash := hashKnowledgeCommand(metadata, string(requestBytes))
	manifest.CreatedAt = postgresTimestamp(s.clock.Now())
	if err := contracts.ValidateArtifactBundleManifest(manifest); err != nil {
		return contracts.ArtifactBundleManifest{}, fault.Wrap(fault.CodeInvalidArgument, "postgres.CreateArtifactBundleManifest", "validate manifest", err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contracts.ArtifactBundleManifest{}, databaseError("postgres.CreateArtifactBundleManifest.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return contracts.ArtifactBundleManifest{}, err
	}
	if replay, found, err := loadControlReplay[contracts.ArtifactBundleManifest](ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return contracts.ArtifactBundleManifest{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return contracts.ArtifactBundleManifest{}, databaseError("postgres.CreateArtifactBundleManifest.replay", err)
		}
		return replay, nil
	}
	for _, entry := range manifest.Entries {
		if err := verifyExactArtifact(ctx, tx, s.tenantID, s.repositoryID, entry.ArtifactID, entry.ContentHash, entry.SizeBytes, entry.MediaType); err != nil {
			return contracts.ArtifactBundleManifest{}, err
		}
	}
	sourceRefs, _ := json.Marshal(manifest.SourceRefs)
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.artifact_bundle_manifests (
			tenant_id, repository_id, manifest_id, family, total_size_bytes,
			entry_count, source_refs, created_by, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		s.tenantID, s.repositoryID, manifest.ManifestID, manifest.Family,
		manifest.TotalSizeBytes, len(manifest.Entries), sourceRefs,
		manifest.CreatedBy, manifest.CreatedAt,
	); err != nil {
		return contracts.ArtifactBundleManifest{}, databaseError("postgres.CreateArtifactBundleManifest.insert", err)
	}
	for index, entry := range manifest.Entries {
		digest, _ := decodeContentHash(entry.ContentHash)
		if _, err := tx.Exec(ctx, `
			INSERT INTO forja.artifact_bundle_entries (
				tenant_id, repository_id, manifest_id, ordinal, logical_path,
				artifact_id, content_sha256, size_bytes, media_type
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			s.tenantID, s.repositoryID, manifest.ManifestID, index,
			entry.LogicalPath, entry.ArtifactID, digest, entry.SizeBytes, entry.MediaType,
		); err != nil {
			return contracts.ArtifactBundleManifest{}, databaseError("postgres.CreateArtifactBundleManifest.entry", err)
		}
	}
	if err := s.appendKnowledgeEvent(ctx, tx, "artifact_manifest", manifest.ManifestID, 1, "artifact_manifest.created", manifest.CreatedAt, manifest, metadata); err != nil {
		return contracts.ArtifactBundleManifest{}, err
	}
	if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 201, manifest); err != nil {
		return contracts.ArtifactBundleManifest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.ArtifactBundleManifest{}, databaseError("postgres.CreateArtifactBundleManifest.commit", err)
	}
	return manifest, nil
}

func (s *Store) CloseConversation(
	ctx context.Context,
	command persistence.CloseConversationCommand,
	metadata runstate.CommandMetadata,
) (contracts.Conversation, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return contracts.Conversation{}, err
	}
	scope := "conversation_close:" + s.repositoryID + ":" + command.ConversationID
	requestHash := hashKnowledgeCommand(metadata, command.ConversationID, fmt.Sprint(command.ExpectedVersion), command.TranscriptArtifact, command.TranscriptManifest)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contracts.Conversation{}, databaseError("postgres.CloseConversation.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return contracts.Conversation{}, err
	}
	if replay, found, err := loadControlReplay[contracts.Conversation](ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return contracts.Conversation{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return contracts.Conversation{}, databaseError("postgres.CloseConversation.replay", err)
		}
		return replay, nil
	}
	conversation, _, err := loadConversation(ctx, tx, s.tenantID, s.repositoryID, command.ConversationID, true)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.Conversation{}, fault.New(fault.CodeNotFound, "postgres.CloseConversation", "conversation was not found")
		}
		return contracts.Conversation{}, databaseError("postgres.CloseConversation.load", err)
	}
	if conversation.Status != "active" || conversation.Version != command.ExpectedVersion {
		return contracts.Conversation{}, fault.New(fault.CodeConflict, "postgres.CloseConversation", "conversation is not a matching active version")
	}
	requiredSourceRefs, expectedTranscript, err := conversationTranscriptBinding(
		ctx, tx, s.tenantID, s.repositoryID, command.ConversationID, command.ExpectedVersion,
	)
	if err != nil {
		return contracts.Conversation{}, err
	}
	var sourceRefsJSON, transcriptDigest []byte
	var transcriptSize int64
	var transcriptMediaType string
	if err := tx.QueryRow(ctx, `
		SELECT manifest.source_refs, artifact.content_sha256,
		       artifact.size_bytes, artifact.media_type
		FROM forja.artifacts AS artifact
		JOIN forja.artifact_bundle_entries AS entry
		  ON entry.tenant_id=artifact.tenant_id
		 AND entry.repository_id=artifact.repository_id
		 AND entry.artifact_id=artifact.artifact_id
		 AND entry.content_sha256=artifact.content_sha256
		JOIN forja.artifact_bundle_manifests AS manifest
		  ON manifest.tenant_id=entry.tenant_id
		 AND manifest.repository_id=entry.repository_id
		 AND manifest.manifest_id=entry.manifest_id
		WHERE artifact.tenant_id=$1 AND artifact.repository_id=$2
		  AND artifact.artifact_id=$3 AND artifact.kind='conversation'
		  AND artifact.status IN ('active', 'validated')
		  AND manifest.manifest_id=$4 AND manifest.family='conversation_transcript'
		LIMIT 1
		FOR SHARE OF artifact`, s.tenantID, s.repositoryID, command.TranscriptArtifact, command.TranscriptManifest,
	).Scan(&sourceRefsJSON, &transcriptDigest, &transcriptSize, &transcriptMediaType); err != nil {
		return contracts.Conversation{}, databaseError("postgres.CloseConversation.transcript", err)
	}
	expectedDigest := sha256.Sum256(expectedTranscript)
	if !bytes.Equal(transcriptDigest, expectedDigest[:]) ||
		transcriptSize != int64(len(expectedTranscript)) || transcriptMediaType != "application/json" {
		return contracts.Conversation{}, fault.New(fault.CodeInvalidArgument, "postgres.CloseConversation", "transcript artifact is not the canonical final message inventory")
	}
	var sourceRefs []string
	if err := json.Unmarshal(sourceRefsJSON, &sourceRefs); err != nil {
		return contracts.Conversation{}, fault.Wrap(fault.CodeInternal, "postgres.CloseConversation", "decode transcript source refs", err)
	}
	if len(sourceRefs) != len(requiredSourceRefs) {
		return contracts.Conversation{}, fault.New(fault.CodeInvalidArgument, "postgres.CloseConversation", "transcript manifest source references are not exact")
	}
	for _, required := range requiredSourceRefs {
		if !slices.Contains(sourceRefs, required) {
			return contracts.Conversation{}, fault.New(fault.CodeInvalidArgument, "postgres.CloseConversation", "transcript manifest is not bound to the exact final conversation history")
		}
	}
	now := postgresTimestamp(s.clock.Now())
	conversation.Status = "closed"
	conversation.Version++
	conversation.UpdatedAt = now
	conversation.ClosedAt = &now
	conversation.TranscriptArtifactID = &command.TranscriptArtifact
	conversation.TranscriptManifestID = &command.TranscriptManifest
	if _, err := tx.Exec(ctx, `
		UPDATE forja.conversations
		SET status='closed', version=$4, transcript_artifact_id=$5,
			transcript_manifest_id=$6, updated_at=$7, closed_at=$7
		WHERE tenant_id=$1 AND repository_id=$2 AND conversation_id=$3`,
		s.tenantID, s.repositoryID, conversation.ConversationID, conversation.Version,
		command.TranscriptArtifact, command.TranscriptManifest, now,
	); err != nil {
		return contracts.Conversation{}, databaseError("postgres.CloseConversation.update", err)
	}
	if err := s.appendKnowledgeEvent(ctx, tx, "conversation", conversation.ConversationID, conversation.Version, "conversation.closed", now, conversation, metadata); err != nil {
		return contracts.Conversation{}, err
	}
	if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 200, conversation); err != nil {
		return contracts.Conversation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.Conversation{}, databaseError("postgres.CloseConversation.commit", err)
	}
	return conversation, nil
}

func conversationTranscriptSourceRefs(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, repositoryID, conversationID string,
	conversationVersion int,
) ([]string, error) {
	refs, _, err := conversationTranscriptBinding(
		ctx, tx, tenantID, repositoryID, conversationID, conversationVersion,
	)
	return refs, err
}

func conversationTranscriptBinding(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, repositoryID, conversationID string,
	conversationVersion int,
) ([]string, []byte, error) {
	rows, err := tx.Query(ctx, `
		SELECT sequence_number, message_id, encode(content_sha256, 'hex')
		FROM forja.messages
		WHERE tenant_id=$1 AND repository_id=$2 AND conversation_id=$3
		ORDER BY sequence_number`, tenantID, repositoryID, conversationID)
	if err != nil {
		return nil, nil, databaseError("postgres.conversationTranscriptBinding", err)
	}
	defer rows.Close()
	type transcriptMessage struct {
		SequenceNumber int    `json:"sequence_number"`
		MessageID      string `json:"message_id"`
		ContentHash    string `json:"content_hash"`
	}
	messages := make([]transcriptMessage, 0)
	for rows.Next() {
		var sequence int
		var messageID, digestHex string
		if err := rows.Scan(&sequence, &messageID, &digestHex); err != nil {
			return nil, nil, databaseError("postgres.conversationTranscriptBinding.scan", err)
		}
		messages = append(messages, transcriptMessage{
			SequenceNumber: sequence,
			MessageID:      messageID,
			ContentHash:    "sha256:" + digestHex,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, databaseError("postgres.conversationTranscriptBinding.rows", err)
	}
	transcript, err := json.Marshal(struct {
		SchemaVersion       string              `json:"schema_version"`
		ConversationID      string              `json:"conversation_id"`
		ConversationVersion int                 `json:"conversation_version"`
		Messages            []transcriptMessage `json:"messages"`
	}{
		SchemaVersion: contracts.KnowledgeSchemaVersion, ConversationID: conversationID,
		ConversationVersion: conversationVersion, Messages: messages,
	})
	if err != nil {
		return nil, nil, fault.Wrap(fault.CodeInternal, "postgres.conversationTranscriptBinding", "encode canonical transcript inventory", err)
	}
	digest := sha256.Sum256(transcript)
	digestHex := hex.EncodeToString(digest[:])
	return []string{
		conversationID,
		fmt.Sprintf("conversation_version:%d", conversationVersion),
		"message_inventory:sha256:" + digestHex,
	}, transcript, nil
}

func (s *Store) TombstoneConversation(
	ctx context.Context,
	conversationID string,
	expectedVersion int,
	metadata runstate.CommandMetadata,
) (contracts.Conversation, error) {
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return contracts.Conversation{}, err
	}
	scope := "conversation_tombstone:" + s.repositoryID + ":" + conversationID
	requestHash := hashKnowledgeCommand(metadata, conversationID, fmt.Sprint(expectedVersion))
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return contracts.Conversation{}, databaseError("postgres.TombstoneConversation.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return contracts.Conversation{}, err
	}
	if replay, found, err := loadControlReplay[contracts.Conversation](ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash); err != nil {
		return contracts.Conversation{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return contracts.Conversation{}, databaseError("postgres.TombstoneConversation.replay", err)
		}
		return replay, nil
	}
	conversation, _, err := loadConversation(ctx, tx, s.tenantID, s.repositoryID, conversationID, true)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return contracts.Conversation{}, fault.New(fault.CodeNotFound, "postgres.TombstoneConversation", "conversation was not found")
		}
		return contracts.Conversation{}, databaseError("postgres.TombstoneConversation.load", err)
	}
	if conversation.Status == "tombstoned" || conversation.Version != expectedVersion {
		return contracts.Conversation{}, fault.New(fault.CodeConflict, "postgres.TombstoneConversation", "conversation is not a matching live version")
	}
	now := postgresTimestamp(s.clock.Now())
	conversation.Status = "tombstoned"
	conversation.Version++
	conversation.UpdatedAt = now
	conversation.TombstonedAt = &now
	if _, err := tx.Exec(ctx, `
		UPDATE forja.conversations SET status='tombstoned', version=$4,
			updated_at=$5, tombstoned_at=$5
		WHERE tenant_id=$1 AND repository_id=$2 AND conversation_id=$3`,
		s.tenantID, s.repositoryID, conversationID, conversation.Version, now,
	); err != nil {
		return contracts.Conversation{}, databaseError("postgres.TombstoneConversation.update", err)
	}
	if err := s.appendKnowledgeEvent(ctx, tx, "conversation", conversationID, conversation.Version, "conversation.tombstoned", now, conversation, metadata); err != nil {
		return contracts.Conversation{}, err
	}
	if err := saveControlReplay(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, requestHash, 200, conversation); err != nil {
		return contracts.Conversation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contracts.Conversation{}, databaseError("postgres.TombstoneConversation.commit", err)
	}
	return conversation, nil
}

func loadConversation(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, repositoryID, conversationID string,
	forUpdate bool,
) (contracts.Conversation, *string, error) {
	query := `
		SELECT conversation_id, status, version, retention_class, created_by,
		       transcript_artifact_id, transcript_manifest_id,
		       created_at, updated_at, closed_at, tombstoned_at
		FROM forja.conversations
		WHERE tenant_id=$1 AND repository_id=$2 AND conversation_id=$3`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var value contracts.Conversation
	err := tx.QueryRow(ctx, query, tenantID, repositoryID, conversationID).Scan(
		&value.ConversationID, &value.Status, &value.Version, &value.RetentionClass,
		&value.CreatedBy, &value.TranscriptArtifactID, &value.TranscriptManifestID,
		&value.CreatedAt, &value.UpdatedAt, &value.ClosedAt, &value.TombstonedAt,
	)
	value.SchemaVersion = contracts.KnowledgeSchemaVersion
	value.TenantID = "tenant_" + tenantID
	value.RepositoryID = "repo_" + repositoryID
	value.CreatedAt = value.CreatedAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	value.ClosedAt = utcTimePointer(value.ClosedAt)
	value.TombstonedAt = utcTimePointer(value.TombstonedAt)
	return value, value.TranscriptManifestID, err
}

func utcTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func verifyMessageArtifactReferences(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, repositoryID string,
	parts []contracts.ContentPart,
	citations []contracts.Citation,
) error {
	for _, part := range parts {
		if err := verifyExactArtifact(ctx, tx, tenantID, repositoryID, part.ArtifactID, part.ContentHash, part.SizeBytes, part.MediaType); err != nil {
			return err
		}
	}
	for _, citation := range citations {
		if err := verifyExactArtifact(ctx, tx, tenantID, repositoryID, citation.SourceArtifactID, citation.SourceContentHash, -1, ""); err != nil {
			return err
		}
	}
	return nil
}

func verifyExactArtifact(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, repositoryID, artifactID, contentHash string,
	size int64,
	mediaType string,
) error {
	digest, err := decodeContentHash(contentHash)
	if err != nil {
		return fault.Wrap(fault.CodeInvalidArgument, "postgres.verifyExactArtifact", "decode content hash", err)
	}
	var storedDigest []byte
	var storedSize int64
	var storedMediaType, status string
	if err := tx.QueryRow(ctx, `
		SELECT content_sha256, size_bytes, media_type, status
		FROM forja.artifacts
		WHERE tenant_id=$1 AND repository_id=$2 AND artifact_id=$3
		FOR SHARE`, tenantID, repositoryID, artifactID,
	).Scan(&storedDigest, &storedSize, &storedMediaType, &status); errors.Is(err, pgx.ErrNoRows) {
		return fault.New(fault.CodeInvalidArgument, "postgres.verifyExactArtifact", "artifact reference is not exact active evidence")
	} else if err != nil {
		return databaseError("postgres.verifyExactArtifact", err)
	}
	if !bytes.Equal(storedDigest, digest) ||
		(status != "active" && status != "validated") ||
		(size >= 0 && storedSize != size) ||
		(mediaType != "" && storedMediaType != mediaType) {
		return fault.New(fault.CodeInvalidArgument, "postgres.verifyExactArtifact", "artifact reference is not exact active evidence")
	}
	return nil
}

func decodeContentHash(value string) ([]byte, error) {
	if !strings.HasPrefix(value, "sha256:") {
		return nil, fmt.Errorf("content hash must use sha256")
	}
	digest, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	if err != nil || len(digest) != 32 || value != "sha256:"+hex.EncodeToString(digest) {
		return nil, fmt.Errorf("content hash is invalid")
	}
	return digest, nil
}

func hashKnowledgeCommand(metadata runstate.CommandMetadata, parts ...string) []byte {
	// Audit tool names describe transport, not business identity, and are not
	// part of the canonical knowledge event envelope.
	metadata.AuditToolName = ""
	return hashCommand(metadata, append([]string{"knowledge"}, parts...)...)
}

func (s *Store) appendKnowledgeEvent(
	ctx context.Context,
	tx pgx.Tx,
	aggregateType, aggregateID string,
	version int,
	eventType string,
	occurredAt time.Time,
	payload any,
	metadata runstate.CommandMetadata,
) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fault.Wrap(fault.CodeInternal, "postgres.appendKnowledgeEvent", "encode event", err)
	}
	if occurredAt.IsZero() {
		return fault.New(fault.CodeInternal, "postgres.appendKnowledgeEvent", "event timestamp is invalid")
	}
	return s.appendEvent(ctx, tx, aggregateType, aggregateID, version, eventType, occurredAt, encoded, metadata)
}
