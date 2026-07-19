LOCK TABLE
    forja.idempotency_keys,
    forja.events,
    forja.outbox,
    forja.index_snapshots,
    forja.index_files,
    forja.index_symbols,
    forja.index_relations,
    forja.index_adapter_runs,
    forja.index_deltas,
    forja.index_invalidations
IN ACCESS EXCLUSIVE MODE NOWAIT;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM forja.index_snapshots)
       OR EXISTS (SELECT 1 FROM forja.index_files)
       OR EXISTS (SELECT 1 FROM forja.index_symbols)
       OR EXISTS (SELECT 1 FROM forja.index_relations)
       OR EXISTS (SELECT 1 FROM forja.index_adapter_runs)
       OR EXISTS (SELECT 1 FROM forja.index_deltas)
       OR EXISTS (SELECT 1 FROM forja.index_invalidations)
       OR EXISTS (SELECT 1 FROM forja.events WHERE aggregate_type='index_snapshot')
       OR EXISTS (SELECT 1 FROM forja.idempotency_keys WHERE scope LIKE 'index\_%' ESCAPE '\') THEN
        RAISE EXCEPTION USING
            ERRCODE='55000',
            MESSAGE='migration 008 rollback requires unused deterministic index authority';
    END IF;
END
$$;

ALTER TABLE forja.events
    DROP CONSTRAINT events_aggregate_type_check,
    ADD CONSTRAINT events_aggregate_type_check CHECK (
        aggregate_type IN (
            'sprint', 'task', 'run', 'attempt', 'approval', 'decision',
            'audit', 'artifact', 'artifact_operation', 'artifact_manifest',
            'conversation', 'message', 'memory_candidate', 'memory',
            'projection'
        )
    );

DROP TRIGGER index_invalidations_are_append_only ON forja.index_invalidations;
DROP TRIGGER index_deltas_are_append_only ON forja.index_deltas;
DROP TRIGGER index_adapter_runs_are_append_only ON forja.index_adapter_runs;
DROP TRIGGER index_relations_are_append_only ON forja.index_relations;
DROP TRIGGER index_symbols_are_append_only ON forja.index_symbols;
DROP TRIGGER index_files_are_append_only ON forja.index_files;
DROP FUNCTION forja.reject_index_immutable_mutation();

DROP TRIGGER live_index_snapshots_protect_artifacts ON forja.artifacts;
DROP TRIGGER index_snapshot_transitions_are_guarded ON forja.index_snapshots;
DROP TRIGGER index_snapshots_require_canonical_artifact ON forja.index_snapshots;
DROP FUNCTION forja.protect_index_snapshot_artifact();
DROP FUNCTION forja.enforce_index_snapshot_transition();
DROP FUNCTION forja.enforce_index_snapshot_authority();

DROP TABLE forja.index_invalidations;
DROP TABLE forja.index_deltas;
DROP TABLE forja.index_adapter_runs;
DROP TABLE forja.index_relations;
DROP TABLE forja.index_symbols;
DROP TABLE forja.index_files;
DROP TABLE forja.index_snapshots;
