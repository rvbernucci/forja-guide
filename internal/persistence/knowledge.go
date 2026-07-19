package persistence

import (
	"context"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

type MessageDraft struct {
	MessageID           string
	ConversationID      string
	Role                string
	AuthorID            string
	SupersedesMessageID *string
	ContentParts        []contracts.ContentPart
	Citations           []contracts.Citation
}

type CloseConversationCommand struct {
	ConversationID     string
	ExpectedVersion    int
	TranscriptArtifact string
	TranscriptManifest string
}

type MemoryPromotionCommand struct {
	Memory                   contracts.MemoryRecord
	ExpectedCandidateVersion int
	Principal                control.Principal
}

type KnowledgeRepository interface {
	CreateConversation(
		context.Context,
		contracts.Conversation,
		runstate.CommandMetadata,
	) (contracts.Conversation, error)
	AppendMessage(
		context.Context,
		MessageDraft,
		runstate.CommandMetadata,
	) (contracts.Message, error)
	CreateArtifactBundleManifest(
		context.Context,
		contracts.ArtifactBundleManifest,
		runstate.CommandMetadata,
	) (contracts.ArtifactBundleManifest, error)
	CloseConversation(
		context.Context,
		CloseConversationCommand,
		runstate.CommandMetadata,
	) (contracts.Conversation, error)
	TombstoneConversation(
		context.Context,
		string,
		int,
		runstate.CommandMetadata,
	) (contracts.Conversation, error)
	ProposeMemory(
		context.Context,
		contracts.MemoryCandidate,
		runstate.CommandMetadata,
	) (contracts.MemoryCandidate, error)
	PromoteMemory(
		context.Context,
		MemoryPromotionCommand,
		runstate.CommandMetadata,
	) (contracts.MemoryRecord, error)
	ResolveMemoryCandidate(
		context.Context,
		string,
		int,
		string,
		string,
		runstate.CommandMetadata,
	) (contracts.MemoryCandidate, error)
	TransitionMemory(
		context.Context,
		string,
		int,
		string,
		runstate.CommandMetadata,
	) (contracts.MemoryRecord, error)
	ListActiveMemories(context.Context, string, int) ([]contracts.MemoryRecord, error)
}

type RetentionCandidate struct {
	TenantID     string
	RepositoryID string
	ContentHash  string
	ObjectKey    string
	ETag         string
	VersionID    string
	SizeBytes    int64
	MediaType    string
	TombstonedAt time.Time
}

type ArtifactRetentionRepository interface {
	TombstoneArtifact(
		context.Context,
		string,
		int,
		runstate.CommandMetadata,
	) (contracts.Artifact, error)
	ListArtifactRetentionCandidates(
		context.Context,
		time.Time,
		int,
	) ([]RetentionCandidate, error)
	MarkArtifactObjectPurged(
		context.Context,
		string,
		string,
		string,
		runstate.CommandMetadata,
	) error
}
