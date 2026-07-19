package postgres

import (
	"encoding/base64"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/clock"
	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

func TestKnowledgeRepositoryConversationAndMemoryLifecycle(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	futureStore, err := NewStore(
		pool,
		clock.Fixed{Time: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)},
		DefaultTenantID,
		DefaultRepositoryID,
		WithMemoryPolicyPrincipal("memory-policy"),
	)
	if err != nil {
		t.Fatal(err)
	}

	bodyArtifact := publishKnowledgeArtifact(t, store, "artifact_repository_body", "40000000-0000-4000-8000-000000000001", "Durable project decision", "test_report")
	transcriptArtifact := publishKnowledgeArtifact(t, store, "artifact_repository_transcript", "40000000-0000-4000-8000-000000000002", "Canonical transcript", "conversation")
	replacementArtifact := publishKnowledgeArtifact(t, store, "artifact_repository_replacement", "40000000-0000-4000-8000-000000000003", "Updated durable decision", "memory")

	conversationID := "conversation_40000000-0000-4000-8000-000000000004"
	conversation, err := store.CreateConversation(t.Context(), contracts.Conversation{
		ConversationID: conversationID,
		RetentionClass: "project",
		CreatedBy:      "integration-suite",
	}, testMetadata("knowledge-create-conversation"))
	if err != nil || conversation.Status != "active" || conversation.Version != 1 {
		t.Fatalf("create conversation=%#v err=%v", conversation, err)
	}

	const messageCount = 4
	results := make(chan contracts.Message, messageCount)
	errors := make(chan error, messageCount)
	var group sync.WaitGroup
	for index := range messageCount {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			author := fmt.Sprintf("writer-%d", index)
			metadata := knowledgeMetadata(fmt.Sprintf("knowledge-append-%d", index), "agent", author)
			message, appendErr := store.AppendMessage(t.Context(), persistence.MessageDraft{
				MessageID:      fmt.Sprintf("message_40000000-0000-4000-8000-%012d", index+10),
				ConversationID: conversationID,
				Role:           "assistant",
				AuthorID:       author,
				ContentParts: []contracts.ContentPart{{
					PartID:      fmt.Sprintf("part_40000000-0000-4000-8000-%012d", index+20),
					Ordinal:     0,
					Kind:        "text",
					ArtifactID:  bodyArtifact.ArtifactID,
					ContentHash: bodyArtifact.ContentHash,
					MediaType:   bodyArtifact.MediaType,
					SizeBytes:   *bodyArtifact.SizeBytes,
				}},
			}, metadata)
			if appendErr != nil {
				errors <- appendErr
				return
			}
			results <- message
		}(index)
	}
	group.Wait()
	close(results)
	close(errors)
	for appendErr := range errors {
		t.Fatalf("concurrent append: %v", appendErr)
	}
	sequences := make([]int, 0, messageCount)
	messages := make([]contracts.Message, 0, messageCount)
	for message := range results {
		sequences = append(sequences, message.SequenceNumber)
		messages = append(messages, message)
	}
	sort.Ints(sequences)
	if fmt.Sprint(sequences) != "[1 2 3 4]" {
		t.Fatalf("concurrent message sequences=%v", sequences)
	}

	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	transcriptSourceRefs, err := conversationTranscriptSourceRefs(
		t.Context(), tx, DefaultTenantID, DefaultRepositoryID, conversationID, 1+messageCount,
	)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	manifest, err := store.CreateArtifactBundleManifest(t.Context(), contracts.ArtifactBundleManifest{
		ManifestID: "manifest_40000000-0000-4000-8000-000000000030",
		Family:     "conversation_transcript",
		Entries: []contracts.ArtifactBundleEntry{{
			LogicalPath: "conversation/transcript.txt",
			ArtifactID:  transcriptArtifact.ArtifactID,
			ContentHash: transcriptArtifact.ContentHash,
			SizeBytes:   *transcriptArtifact.SizeBytes,
			MediaType:   transcriptArtifact.MediaType,
		}},
		TotalSizeBytes: *transcriptArtifact.SizeBytes,
		SourceRefs:     transcriptSourceRefs,
		CreatedBy:      "integration-suite",
	}, testMetadata("knowledge-create-transcript-manifest"))
	if err != nil {
		t.Fatal(err)
	}
	replayedManifest, err := futureStore.CreateArtifactBundleManifest(
		t.Context(), manifest, testMetadata("knowledge-create-transcript-manifest"),
	)
	if err != nil || !replayedManifest.CreatedAt.Equal(manifest.CreatedAt) {
		t.Fatalf("manifest replay=%#v err=%v", replayedManifest, err)
	}
	closed, err := store.CloseConversation(t.Context(), persistence.CloseConversationCommand{
		ConversationID:     conversationID,
		ExpectedVersion:    1 + messageCount,
		TranscriptArtifact: transcriptArtifact.ArtifactID,
		TranscriptManifest: manifest.ManifestID,
	}, testMetadata("knowledge-close-conversation"))
	if err != nil || closed.Status != "closed" || closed.Version != messageCount+2 {
		t.Fatalf("close conversation=%#v err=%v", closed, err)
	}
	if _, err := store.TombstoneArtifact(
		t.Context(), bodyArtifact.ArtifactID, 1,
		knowledgeMetadata("knowledge-referenced-artifact-tombstone", "human", "reviewer"),
	); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("live message artifact tombstone error=%v", err)
	}

	sort.Slice(messages, func(left, right int) bool {
		return messages[left].SequenceNumber < messages[right].SequenceNumber
	})
	firstCandidate := contracts.MemoryCandidate{
		CandidateID:         "memory_candidate_40000000-0000-4000-8000-000000000040",
		ConversationID:      conversationID,
		SourceMessageIDs:    []string{messages[0].MessageID},
		Kind:                "decision",
		ProposedArtifactID:  bodyArtifact.ArtifactID,
		ProposedContentHash: bodyArtifact.ContentHash,
		ProposedBy:          "planner-agent",
	}
	firstCandidate, err = store.ProposeMemory(t.Context(), firstCandidate, knowledgeMetadata("knowledge-propose-first", "agent", "planner-agent"))
	if err != nil {
		t.Fatal(err)
	}
	replayedCandidate, err := futureStore.ProposeMemory(
		t.Context(), firstCandidate,
		knowledgeMetadata("knowledge-propose-first", "agent", "planner-agent"),
	)
	if err != nil || !replayedCandidate.ProposedAt.Equal(firstCandidate.ProposedAt) {
		t.Fatalf("candidate replay=%#v err=%v", replayedCandidate, err)
	}
	firstMemory := contracts.MemoryRecord{
		MemoryID:          "memory_40000000-0000-4000-8000-000000000041",
		SourceCandidateID: firstCandidate.CandidateID,
		Kind:              firstCandidate.Kind,
		ContentArtifactID: firstCandidate.ProposedArtifactID,
		ContentHash:       firstCandidate.ProposedContentHash,
		AuthorityClass:    "human_approved",
		PromotedBy:        "reviewer",
		PromotionReason:   "Canonical evidence supports durable reuse",
		Supersedes:        []string{},
	}
	if _, err := store.PromoteMemory(t.Context(), persistence.MemoryPromotionCommand{
		Memory: firstMemory, ExpectedCandidateVersion: firstCandidate.Version,
	}, knowledgeMetadata("knowledge-agent-self-promotion", "agent", "reviewer")); !fault.IsCode(err, fault.CodePermissionDenied) {
		t.Fatalf("agent promotion error=%v", err)
	}
	firstMemory, err = store.PromoteMemory(t.Context(), persistence.MemoryPromotionCommand{
		Memory: firstMemory, ExpectedCandidateVersion: firstCandidate.Version,
		Principal: promotionPrincipal(t, "human", "reviewer"),
	}, knowledgeMetadata("knowledge-human-promotion", "human", "reviewer"))
	if err != nil {
		t.Fatal(err)
	}
	replayedMemory, err := futureStore.PromoteMemory(t.Context(), persistence.MemoryPromotionCommand{
		Memory: firstMemory, ExpectedCandidateVersion: firstCandidate.Version,
		Principal: promotionPrincipal(t, "human", "reviewer"),
	}, knowledgeMetadata("knowledge-human-promotion", "human", "reviewer"))
	if err != nil || !replayedMemory.PromotedAt.Equal(firstMemory.PromotedAt) {
		t.Fatalf("memory replay=%#v err=%v", replayedMemory, err)
	}

	secondCandidate, err := store.ProposeMemory(t.Context(), contracts.MemoryCandidate{
		CandidateID:         "memory_candidate_40000000-0000-4000-8000-000000000042",
		ConversationID:      conversationID,
		SourceMessageIDs:    []string{messages[1].MessageID},
		Kind:                "decision",
		ProposedArtifactID:  replacementArtifact.ArtifactID,
		ProposedContentHash: replacementArtifact.ContentHash,
		ProposedBy:          "planner-agent",
	}, knowledgeMetadata("knowledge-propose-replacement", "agent", "planner-agent"))
	if err != nil {
		t.Fatal(err)
	}
	unauthorizedPolicyMemory := contracts.MemoryRecord{
		MemoryID:          "memory_40000000-0000-4000-8000-000000000099",
		SourceCandidateID: secondCandidate.CandidateID,
		Kind:              secondCandidate.Kind,
		ContentArtifactID: secondCandidate.ProposedArtifactID,
		ContentHash:       secondCandidate.ProposedContentHash,
		AuthorityClass:    "policy_approved",
		PromotedBy:        "unconfigured-policy",
		PromotionReason:   "An unconfigured system identity must not promote memory",
		Supersedes:        []string{firstMemory.MemoryID},
	}
	if _, err := store.PromoteMemory(t.Context(), persistence.MemoryPromotionCommand{
		Memory: unauthorizedPolicyMemory, ExpectedCandidateVersion: secondCandidate.Version,
		Principal: promotionPrincipal(t, "system", "unconfigured-policy"),
	}, knowledgeMetadata("knowledge-unconfigured-policy", "system", "unconfigured-policy")); !fault.IsCode(err, fault.CodePermissionDenied) {
		t.Fatalf("unconfigured policy promotion error=%v", err)
	}
	secondMemory, err := store.PromoteMemory(t.Context(), persistence.MemoryPromotionCommand{
		ExpectedCandidateVersion: secondCandidate.Version,
		Principal:                promotionPrincipal(t, "system", "memory-policy"),
		Memory: contracts.MemoryRecord{
			MemoryID:          "memory_40000000-0000-4000-8000-000000000043",
			SourceCandidateID: secondCandidate.CandidateID,
			Kind:              secondCandidate.Kind,
			ContentArtifactID: secondCandidate.ProposedArtifactID,
			ContentHash:       secondCandidate.ProposedContentHash,
			AuthorityClass:    "policy_approved",
			PromotedBy:        "memory-policy",
			PromotionReason:   "Newer canonical decision supersedes prior evidence",
			Supersedes:        []string{firstMemory.MemoryID},
		},
	}, knowledgeMetadata("knowledge-policy-promotion", "system", "memory-policy"))
	if err != nil {
		t.Fatal(err)
	}
	active, err := store.ListActiveMemories(t.Context(), "decision", 10)
	if err != nil || len(active) != 1 || active[0].MemoryID != secondMemory.MemoryID {
		t.Fatalf("active memories=%#v err=%v", active, err)
	}

	rejectedCandidate, err := store.ProposeMemory(t.Context(), contracts.MemoryCandidate{
		CandidateID:         "memory_candidate_40000000-0000-4000-8000-000000000044",
		ConversationID:      conversationID,
		SourceMessageIDs:    []string{messages[2].MessageID},
		Kind:                "lesson",
		ProposedArtifactID:  bodyArtifact.ArtifactID,
		ProposedContentHash: bodyArtifact.ContentHash,
		ProposedBy:          "planner-agent",
	}, knowledgeMetadata("knowledge-propose-rejected", "agent", "planner-agent"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveMemoryCandidate(t.Context(), rejectedCandidate.CandidateID, 1, "rejected", "Insufficient authority for durable reuse", knowledgeMetadata("knowledge-reject", "human", "reviewer")); err != nil {
		t.Fatal(err)
	}
	expired, err := store.TransitionMemory(t.Context(), secondMemory.MemoryID, secondMemory.Version, "expired", knowledgeMetadata("knowledge-expire", "system", "memory-policy"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionMemory(t.Context(), expired.MemoryID, expired.Version, "tombstoned", knowledgeMetadata("knowledge-tombstone-memory", "human", "reviewer")); err != nil {
		t.Fatal(err)
	}
	if active, err := store.ListActiveMemories(t.Context(), "", 10); err != nil || len(active) != 0 {
		t.Fatalf("active after tombstone=%#v err=%v", active, err)
	}
	if _, err := store.TombstoneConversation(t.Context(), conversationID, closed.Version, knowledgeMetadata("knowledge-tombstone-conversation", "human", "reviewer")); err != nil {
		t.Fatal(err)
	}

	var tombstoneEvents, outboxEvents, receipts int
	if err := pool.QueryRow(t.Context(), `
		SELECT
			count(*) FILTER (WHERE event_type IN ('memory.tombstoned', 'conversation.tombstoned')),
			(SELECT count(*) FROM forja.outbox AS pending
			 JOIN forja.events AS emitted ON emitted.event_id=pending.event_id
			 WHERE emitted.event_type IN ('memory.tombstoned', 'conversation.tombstoned')),
			(SELECT count(*) FROM forja.idempotency_keys WHERE scope LIKE 'memory_%' OR scope LIKE 'conversation_%')
		FROM forja.events AS lifecycle`,
	).Scan(&tombstoneEvents, &outboxEvents, &receipts); err != nil {
		t.Fatal(err)
	}
	if tombstoneEvents != 2 || outboxEvents != 2 || receipts < 10 {
		t.Fatalf("tombstone evidence events=%d outbox=%d receipts=%d", tombstoneEvents, outboxEvents, receipts)
	}
}

func TestKnowledgeRepositoryRejectsCrossTenantArtifactReference(t *testing.T) {
	pool := migratedPool(t)
	first := newIntegrationStore(t, pool)
	artifact := publishKnowledgeArtifact(
		t, first, "artifact_first_tenant_only",
		"40000000-0000-4000-8000-000000000090", "tenant-owned body", "test_report",
	)

	const otherTenantID = "40000000-0000-4000-8000-000000000091"
	const otherRepositoryID = "40000000-0000-4000-8000-000000000092"
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.tenants (tenant_id, slug, display_name)
		VALUES ($1, 'knowledge-other', 'Knowledge Other Tenant')`, otherTenantID); err != nil {
		t.Fatalf("create isolated tenant: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.repositories (repository_id, tenant_id, canonical_name)
		VALUES ($2, $1, 'knowledge/other')`, otherTenantID, otherRepositoryID); err != nil {
		t.Fatalf("create isolated repository authority: %v", err)
	}
	second, err := NewStore(pool, nil, otherTenantID, otherRepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	conversationID := "conversation_40000000-0000-4000-8000-000000000093"
	if _, err := second.CreateConversation(t.Context(), contracts.Conversation{
		ConversationID: conversationID,
		RetentionClass: "project",
		CreatedBy:      "other-tenant-agent",
	}, knowledgeMetadata("cross-tenant-conversation", "agent", "other-tenant-agent")); err != nil {
		t.Fatal(err)
	}
	_, err = second.AppendMessage(t.Context(), persistence.MessageDraft{
		MessageID:      "message_40000000-0000-4000-8000-000000000094",
		ConversationID: conversationID,
		Role:           "assistant",
		AuthorID:       "other-tenant-agent",
		ContentParts: []contracts.ContentPart{{
			PartID: "part_40000000-0000-4000-8000-000000000095", Ordinal: 0,
			Kind: "text", ArtifactID: artifact.ArtifactID,
			ContentHash: artifact.ContentHash, MediaType: artifact.MediaType,
			SizeBytes: *artifact.SizeBytes,
		}},
	}, knowledgeMetadata("cross-tenant-message", "agent", "other-tenant-agent"))
	if !fault.IsCode(err, fault.CodeInvalidArgument) {
		t.Fatalf("cross-tenant artifact reference error=%v", err)
	}
	var messages, receipts int
	if err := pool.QueryRow(t.Context(), `
		SELECT
			(SELECT count(*) FROM forja.messages WHERE tenant_id=$1),
			(SELECT count(*) FROM forja.idempotency_keys
			 WHERE tenant_id=$1 AND scope=$2)`,
		otherTenantID, "message_append:"+otherRepositoryID+":message_40000000-0000-4000-8000-000000000094",
	).Scan(&messages, &receipts); err != nil {
		t.Fatal(err)
	}
	if messages != 0 || receipts != 0 {
		t.Fatalf("cross-tenant rejection left messages=%d receipts=%d", messages, receipts)
	}
}

func TestConversationCloseRejectsStaleTranscriptInventory(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	body := publishKnowledgeArtifact(
		t, store, "artifact_stale_transcript_body",
		"40000000-0000-4000-8000-000000000110", "message body", "test_report",
	)
	transcript := publishKnowledgeArtifact(
		t, store, "artifact_stale_transcript",
		"40000000-0000-4000-8000-000000000111", "stale transcript", "conversation",
	)
	conversationID := "conversation_40000000-0000-4000-8000-000000000112"
	if _, err := store.CreateConversation(t.Context(), contracts.Conversation{
		ConversationID: conversationID, RetentionClass: "project", CreatedBy: "co-architect",
	}, knowledgeMetadata("stale-transcript-conversation", "agent", "co-architect")); err != nil {
		t.Fatal(err)
	}
	appendMessage := func(messageID, partID, key string) {
		t.Helper()
		if _, err := store.AppendMessage(t.Context(), persistence.MessageDraft{
			MessageID: messageID, ConversationID: conversationID,
			Role: "human", AuthorID: "author",
			ContentParts: []contracts.ContentPart{{
				PartID: partID, Ordinal: 0, Kind: "text",
				ArtifactID: body.ArtifactID, ContentHash: body.ContentHash,
				MediaType: body.MediaType, SizeBytes: *body.SizeBytes,
			}},
		}, knowledgeMetadata(key, "human", "author")); err != nil {
			t.Fatal(err)
		}
	}
	appendMessage(
		"message_40000000-0000-4000-8000-000000000113",
		"part_40000000-0000-4000-8000-000000000114",
		"stale-transcript-first-message",
	)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	refs, err := conversationTranscriptSourceRefs(
		t.Context(), tx, DefaultTenantID, DefaultRepositoryID, conversationID, 2,
	)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	manifest, err := store.CreateArtifactBundleManifest(t.Context(), contracts.ArtifactBundleManifest{
		ManifestID: "manifest_40000000-0000-4000-8000-000000000115",
		Family:     "conversation_transcript",
		Entries: []contracts.ArtifactBundleEntry{{
			LogicalPath: "conversation/transcript.txt", ArtifactID: transcript.ArtifactID,
			ContentHash: transcript.ContentHash, SizeBytes: *transcript.SizeBytes,
			MediaType: transcript.MediaType,
		}},
		TotalSizeBytes: *transcript.SizeBytes, SourceRefs: refs, CreatedBy: "integration-suite",
	}, testMetadata("stale-transcript-manifest"))
	if err != nil {
		t.Fatal(err)
	}
	appendMessage(
		"message_40000000-0000-4000-8000-000000000116",
		"part_40000000-0000-4000-8000-000000000117",
		"stale-transcript-second-message",
	)
	if _, err := store.CloseConversation(t.Context(), persistence.CloseConversationCommand{
		ConversationID: conversationID, ExpectedVersion: 3,
		TranscriptArtifact: transcript.ArtifactID, TranscriptManifest: manifest.ManifestID,
	}, testMetadata("stale-transcript-close")); !fault.IsCode(err, fault.CodeInvalidArgument) {
		t.Fatalf("stale transcript close error=%v", err)
	}
}

func publishKnowledgeArtifact(
	t *testing.T,
	store *Store,
	artifactID, operationSuffix, body, kind string,
) contracts.Artifact {
	t.Helper()
	intent := artifactPublicationIntentFixture(artifactID, operationSuffix, body)
	intent.Kind = kind
	metadata := testMetadata("knowledge-publish-" + artifactID)
	if _, _, err := store.PrepareArtifactPublication(t.Context(), intent, metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkArtifactPublicationUploading(t.Context(), intent, metadata); err != nil {
		t.Fatal(err)
	}
	digest, err := hexDigest(intent.ContentHash)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.CompleteArtifactPublication(t.Context(), intent, persistence.ArtifactEvidence{
		ObjectKey:              artifactObjectKey(DefaultTenantID, DefaultRepositoryID, digest),
		ETag:                   fmt.Sprintf("\"%s-etag\"", artifactID),
		VersionID:              artifactID + "-version",
		ProviderChecksumSHA256: base64.StdEncoding.EncodeToString(digest),
	}, metadata)
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func knowledgeMetadata(key, actorType, actorID string) runstate.CommandMetadata {
	return runstate.CommandMetadata{
		IdempotencyKey: key,
		ActorType:      actorType,
		ActorID:        actorID,
		CorrelationID:  key,
		AuditToolName:  "knowledge-integration",
	}
}

func promotionPrincipal(t *testing.T, actorType, actorID string) control.Principal {
	t.Helper()
	principal, err := control.NewScopedPrincipal(
		actorType, actorID, DefaultTenantID, DefaultRepositoryID,
		control.PermissionMemoryPromote,
	)
	if err != nil {
		t.Fatal(err)
	}
	return principal
}
