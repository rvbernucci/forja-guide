LOCK TABLE
    forja.idempotency_keys,
    forja.events,
    forja.outbox,
    forja.artifact_objects,
    forja.artifact_operations,
    forja.artifacts,
    forja.artifact_supersessions,
    forja.artifact_bundle_manifests,
    forja.artifact_bundle_entries,
    forja.conversations,
    forja.messages,
    forja.message_parts,
    forja.message_citations,
    forja.memory_candidates,
    forja.memory_candidate_sources,
    forja.memory_records,
    forja.memory_supersessions
IN ACCESS EXCLUSIVE MODE NOWAIT;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM forja.artifact_objects)
       OR EXISTS (SELECT 1 FROM forja.artifact_operations)
       OR EXISTS (SELECT 1 FROM forja.artifacts)
       OR EXISTS (SELECT 1 FROM forja.artifact_bundle_manifests)
       OR EXISTS (SELECT 1 FROM forja.conversations)
       OR EXISTS (SELECT 1 FROM forja.messages)
       OR EXISTS (SELECT 1 FROM forja.memory_candidates)
       OR EXISTS (SELECT 1 FROM forja.memory_records)
       OR EXISTS (
           SELECT 1 FROM forja.events
           WHERE aggregate_type IN (
               'artifact_operation', 'artifact_manifest', 'conversation',
               'message', 'memory_candidate', 'memory'
           )
       )
       OR EXISTS (
           SELECT 1 FROM forja.idempotency_keys
           WHERE scope LIKE 'artifact\_%' ESCAPE '\'
              OR scope LIKE 'conversation\_%' ESCAPE '\'
              OR scope LIKE 'message\_%' ESCAPE '\'
              OR scope LIKE 'memory\_%' ESCAPE '\'
       ) THEN
        RAISE EXCEPTION USING
            ERRCODE='55000',
            MESSAGE='migration 007 rollback requires unused artifact, conversation, and memory authority';
    END IF;
END
$$;

ALTER TABLE forja.events
    DROP CONSTRAINT events_aggregate_type_check,
    ADD CONSTRAINT events_aggregate_type_check CHECK (
        aggregate_type IN (
            'sprint', 'task', 'run', 'attempt', 'approval', 'decision',
            'audit', 'artifact', 'projection'
        )
    );

DROP TRIGGER artifact_bundle_entries_are_append_only ON forja.artifact_bundle_entries;
DROP TRIGGER artifact_bundle_manifests_are_append_only ON forja.artifact_bundle_manifests;
DROP TRIGGER memory_supersessions_are_append_only ON forja.memory_supersessions;
DROP TRIGGER artifact_supersessions_are_append_only ON forja.artifact_supersessions;
DROP TRIGGER memory_candidate_sources_are_append_only ON forja.memory_candidate_sources;
DROP TRIGGER message_citations_are_append_only ON forja.message_citations;
DROP TRIGGER message_parts_are_append_only ON forja.message_parts;
DROP TRIGGER messages_are_append_only ON forja.messages;
DROP TRIGGER zz_memory_candidate_sources_at_commit ON forja.memory_candidates;
DROP TRIGGER zz_message_has_content_at_commit ON forja.messages;
DROP TRIGGER zz_memory_requires_promoted_candidate_at_commit ON forja.memory_records;
DROP TRIGGER zz_artifact_bundle_totals_at_commit ON forja.artifact_bundle_manifests;
DROP TRIGGER zz_artifact_requires_active_object_at_commit ON forja.artifacts;
DROP FUNCTION forja.enforce_memory_promotion();
DROP FUNCTION forja.enforce_memory_candidate_sources();
DROP FUNCTION forja.enforce_message_has_content();
DROP FUNCTION forja.enforce_artifact_bundle_totals();
DROP FUNCTION forja.enforce_artifact_activation();
DROP FUNCTION forja.reject_knowledge_immutable_mutation();

ALTER TABLE forja.memory_candidates
    DROP CONSTRAINT memory_candidates_promoted_memory_fkey;

DROP TABLE forja.memory_supersessions;
DROP TABLE forja.memory_records;
DROP TABLE forja.memory_candidate_sources;
DROP TABLE forja.memory_candidates;
DROP TABLE forja.message_citations;
DROP TABLE forja.message_parts;
DROP TABLE forja.messages;
DROP TABLE forja.conversations;
DROP TABLE forja.artifact_bundle_entries;
DROP TABLE forja.artifact_bundle_manifests;
DROP TABLE forja.artifact_supersessions;
DROP TABLE forja.artifacts;
DROP TABLE forja.artifact_operations;
DROP TABLE forja.artifact_objects;
