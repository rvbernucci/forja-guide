-- PostgreSQL cannot restore the Sprint 02 aggregate-type constraint while
-- Sprint 03 audit and decision events remain. Derived references are removed
-- first, then those unsupported events are deliberately discarded. Sprint and
-- Run domain events remain available after rollback.
--
-- Rollback is a maintenance operation. Lock every table touched by governed
-- command writers and downgrade cleanup before examining safety invariants.
-- Without this barrier, a command could create a pending decision after the
-- check below and have that decision discarded by the same rollback.
LOCK TABLE
    forja.idempotency_keys,
    forja.repositories,
    forja.sprints,
    forja.runs,
    forja.decisions,
    forja.events,
    forja.outbox,
    forja.projection_dead_letters,
    forja.run_projections,
    forja.projection_checkpoints
IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM forja.decisions WHERE status='pending') THEN
        RAISE EXCEPTION USING
            ERRCODE='55000',
            MESSAGE='migration 003 rollback requires every pending decision to be resolved';
    END IF;
END
$$;

-- Receipts whose response bodies depend on Sprint 03 columns or decisions
-- cannot survive this destructive rollback. Keeping them would allow a later
-- re-upgrade to replay state that no longer exists in the canonical tables.
-- Preserve an immutable, identity-exact invalidation marker so recovery can
-- distinguish this deliberate removal from accidental receipt loss.
WITH receipt_candidates AS (
    SELECT i.tenant_id,
           r.repository_id,
           i.scope,
           i.idempotency_key,
           i.response_body,
           split_part(i.scope, ':', 1) AS command_name
    FROM forja.idempotency_keys AS i
    JOIN forja.repositories AS r
      ON r.tenant_id=i.tenant_id
     AND r.repository_id::text=split_part(i.scope, ':', 2)
    WHERE split_part(i.scope, ':', 1) IN (
        'plan_sprint',
        'submit_sprint',
        'resolve_decision',
        'transition_run',
        'resume_run'
    )
), governed_receipts AS (
    SELECT DISTINCT
           receipt.*,
           anchor.event_id AS command_event_id,
           anchor.aggregate_type AS command_aggregate_type,
           anchor.aggregate_id AS command_aggregate_id,
           anchor.event_type AS command_event_type,
           anchor.actor_type,
           anchor.actor_id,
           anchor.correlation_id,
           anchor.causation_id
    FROM receipt_candidates AS receipt
    JOIN forja.events AS anchor
      ON anchor.tenant_id=receipt.tenant_id
     AND anchor.repository_id=receipt.repository_id
     AND anchor.idempotency_key=receipt.idempotency_key
     AND (
            (receipt.command_name='plan_sprint'
             AND anchor.aggregate_type='sprint'
             AND anchor.event_type='sprint.planned'
             AND anchor.aggregate_version=(receipt.response_body#>>'{sprint,version}')::integer
             AND anchor.payload=receipt.response_body->'sprint')
         OR (receipt.command_name='submit_sprint'
             AND anchor.aggregate_type='sprint'
             AND anchor.aggregate_id=split_part(receipt.scope, ':', 3)
             AND anchor.event_type='sprint.submitted'
             AND anchor.aggregate_version=(receipt.response_body#>>'{sprint,version}')::integer
             AND anchor.payload=receipt.response_body->'sprint')
         OR (receipt.command_name='resolve_decision'
             AND anchor.aggregate_type='decision'
             AND anchor.aggregate_id=split_part(receipt.scope, ':', 3)
             AND anchor.event_type IN ('decision.approved', 'decision.rejected')
             AND anchor.aggregate_version=(receipt.response_body#>>'{decision,version}')::integer
             AND anchor.payload=receipt.response_body->'decision')
         OR (receipt.command_name='transition_run'
             AND anchor.aggregate_type='run'
             AND anchor.aggregate_id=split_part(receipt.scope, ':', 3)
             AND anchor.event_type='run.transitioned'
             AND anchor.aggregate_version=(receipt.response_body->>'version')::integer
             AND anchor.payload=receipt.response_body)
         OR (receipt.command_name='resume_run'
             AND anchor.aggregate_type='run'
             AND anchor.aggregate_id=split_part(receipt.scope, ':', 3)
             AND anchor.event_type='run.transitioned'
             AND anchor.aggregate_version=(receipt.response_body->>'version')::integer
             AND anchor.payload=receipt.response_body)
     )
    JOIN forja.events AS audit
      ON audit.tenant_id=anchor.tenant_id
     AND audit.repository_id=anchor.repository_id
     AND audit.idempotency_key=anchor.idempotency_key
     AND audit.actor_type=anchor.actor_type
     AND audit.actor_id=anchor.actor_id
     AND audit.correlation_id=anchor.correlation_id
     AND audit.causation_id IS NOT DISTINCT FROM anchor.causation_id
     AND audit.aggregate_type='audit'
     AND audit.event_type='mcp.tool.succeeded'
     AND COALESCE((audit.payload->>'replay')::boolean, false)=false
     AND audit.payload->>'command_scope'=receipt.scope
     AND (
            (receipt.command_name='plan_sprint' AND audit.payload->>'tool_name'='forja.plan_sprint')
         OR (receipt.command_name='submit_sprint' AND audit.payload->>'tool_name'='forja.submit_sprint')
         OR (receipt.command_name='resolve_decision' AND audit.payload->>'tool_name' IN ('forja.approve_decision', 'forja.reject_decision'))
         OR (receipt.command_name='transition_run' AND audit.payload->>'tool_name'='forja.cancel_run')
         OR (receipt.command_name='resume_run' AND audit.payload->>'tool_name'='forja.resume_run')
     )
), invalidated_identities AS (
    SELECT DISTINCT
           receipt.tenant_id,
           receipt.repository_id,
           receipt.scope,
           receipt.command_name,
           receipt.idempotency_key,
           event.event_id AS domain_event_id,
           receipt.command_event_id,
           receipt.command_aggregate_type,
           receipt.command_aggregate_id,
           receipt.command_event_type,
           receipt.actor_type,
           receipt.actor_id,
           receipt.correlation_id,
           receipt.causation_id
    FROM governed_receipts AS receipt
    JOIN forja.events AS event
      ON event.tenant_id=receipt.tenant_id
     AND event.repository_id=receipt.repository_id
     AND event.idempotency_key=receipt.idempotency_key
     AND event.actor_type=receipt.actor_type
     AND event.actor_id=receipt.actor_id
     AND event.correlation_id=receipt.correlation_id
     AND event.causation_id IS NOT DISTINCT FROM receipt.causation_id
    WHERE (
            receipt.command_name='plan_sprint'
        AND (
               (event.event_type='sprint.planned'
                AND event.aggregate_id=receipt.response_body#>>'{sprint,sprint_id}'
                AND event.aggregate_version=(receipt.response_body#>>'{sprint,version}')::integer
                AND event.payload=receipt.response_body->'sprint')
            OR (event.event_type='run.created'
                AND event.aggregate_id=receipt.response_body#>>'{run,run_id}'
                AND event.aggregate_version=(receipt.response_body#>>'{run,version}')::integer
                AND event.payload=receipt.response_body->'run')
        )
    ) OR (
            receipt.command_name='submit_sprint'
        AND (
               (event.event_type='sprint.submitted'
                AND event.aggregate_id=split_part(receipt.scope, ':', 3)
                AND event.aggregate_version=(receipt.response_body#>>'{sprint,version}')::integer
                AND event.payload=receipt.response_body->'sprint')
            OR (event.event_type='run.transitioned'
                AND event.aggregate_id=receipt.response_body#>>'{run,run_id}'
                AND event.aggregate_version=(receipt.response_body#>>'{run,version}')::integer
                AND event.payload=receipt.response_body->'run')
        )
    ) OR (
            receipt.command_name='resolve_decision'
        AND (
               (event.event_type='sprint.decision_resolved'
                AND event.aggregate_id=receipt.response_body#>>'{sprint,sprint_id}'
                AND event.aggregate_version=(receipt.response_body#>>'{sprint,version}')::integer
                AND event.payload=receipt.response_body->'sprint')
            OR (event.event_type='run.transitioned'
                AND event.aggregate_id=receipt.response_body#>>'{run,run_id}'
                AND event.aggregate_version=(receipt.response_body#>>'{run,version}')::integer
                AND event.payload=receipt.response_body->'run')
        )
    ) OR (
            receipt.command_name='transition_run'
        AND (
               (event.event_type='sprint.cancellation_requested'
                AND event.payload->>'run_id'=split_part(receipt.scope, ':', 3))
            OR (event.event_type='run.transitioned'
                AND event.aggregate_id=split_part(receipt.scope, ':', 3)
                AND event.aggregate_version=(receipt.response_body->>'version')::integer
                AND event.payload=receipt.response_body)
        )
    ) OR (
            receipt.command_name='resume_run'
        AND event.event_type='run.transitioned'
        AND event.aggregate_id=split_part(receipt.scope, ':', 3)
        AND event.aggregate_version=(receipt.response_body->>'version')::integer
        AND event.payload=receipt.response_body
    )
), markers AS (
    SELECT identity.*,
           md5(concat_ws(chr(31),
               identity.tenant_id::text,
               identity.repository_id::text,
               identity.scope,
               identity.idempotency_key,
               identity.domain_event_id,
               identity.command_event_id,
               identity.command_aggregate_type,
               identity.command_aggregate_id,
               identity.command_event_type,
               identity.actor_type,
               identity.actor_id,
               identity.correlation_id,
               COALESCE(identity.causation_id, '')
           )) AS marker_hash
    FROM invalidated_identities AS identity
), inserted AS (
    INSERT INTO forja.events (
        event_id, tenant_id, repository_id, aggregate_type, aggregate_id,
        aggregate_version, event_type, schema_version, occurred_at,
        actor_type, actor_id, correlation_id, causation_id,
        idempotency_key, payload
    )
    SELECT
        'event_receipt_invalidation_' || marker_hash,
        tenant_id,
        repository_id,
        'projection',
        'receipt_invalidation_' || marker_hash,
        1,
        'idempotency.receipt_invalidated',
        '1.0',
        statement_timestamp(),
        'system',
        'migration-003-rollback',
        'migration-003-rollback',
        NULL,
        idempotency_key,
        jsonb_build_object(
            'scope', scope,
            'domain_event_id', domain_event_id,
            'command_event_id', command_event_id,
            'command_aggregate_type', command_aggregate_type,
            'command_aggregate_id', command_aggregate_id,
            'command_event_type', command_event_type,
            'tenant_id', tenant_id::text,
            'repository_id', repository_id::text,
            'idempotency_key', idempotency_key,
            'actor_type', actor_type,
            'actor_id', actor_id,
            'correlation_id', correlation_id,
            'causation_id', causation_id
        )
    FROM markers
    ON CONFLICT (event_id) DO NOTHING
    RETURNING event_id, tenant_id, repository_id
), outboxed AS (
    INSERT INTO forja.outbox (event_id, tenant_id, repository_id)
    SELECT event_id, tenant_id, repository_id
    FROM inserted
    RETURNING event_id
), deleted_receipts AS (
    DELETE FROM forja.idempotency_keys AS receipt
    USING (
        SELECT DISTINCT tenant_id, scope, idempotency_key
        FROM governed_receipts
    ) AS governed
    WHERE receipt.tenant_id=governed.tenant_id
      AND receipt.scope=governed.scope
      AND receipt.idempotency_key=governed.idempotency_key
    RETURNING receipt.scope
)
SELECT (SELECT count(*) FROM outboxed),
       (SELECT count(*) FROM deleted_receipts);

DELETE FROM forja.projection_dead_letters
WHERE event_id IN (
        SELECT event_id
        FROM forja.events
        WHERE aggregate_type IN ('audit', 'decision')
    )
   OR outbox_id IN (
        SELECT o.outbox_id
        FROM forja.outbox AS o
        JOIN forja.events AS e USING (tenant_id, repository_id, event_id)
        WHERE e.aggregate_type IN ('audit', 'decision')
    );

DELETE FROM forja.run_projections
WHERE source_event_id IN (
    SELECT event_id
    FROM forja.events
    WHERE aggregate_type IN ('audit', 'decision')
);

DELETE FROM forja.outbox
WHERE event_id IN (
    SELECT event_id
    FROM forja.events
    WHERE aggregate_type IN ('audit', 'decision')
);

DROP TRIGGER events_are_append_only ON forja.events;

DELETE FROM forja.events
WHERE aggregate_type IN ('audit', 'decision');

CREATE TRIGGER events_are_append_only
BEFORE UPDATE OR DELETE ON forja.events
FOR EACH ROW
EXECUTE FUNCTION forja.reject_event_mutation();

UPDATE forja.projection_checkpoints
SET last_outbox_id=LEAST(
    last_outbox_id,
    COALESCE((SELECT max(outbox_id) FROM forja.outbox), 0)
);

ALTER TABLE forja.events
    DROP CONSTRAINT events_aggregate_type_check,
    ADD CONSTRAINT events_aggregate_type_check CHECK (
        aggregate_type IN (
            'sprint',
            'task',
            'run',
            'attempt',
            'approval',
            'artifact',
            'projection'
        )
    );

DROP TABLE forja.decisions;

ALTER TABLE forja.sprints
    DROP CONSTRAINT sprints_tenant_id_repository_id_run_id_fkey,
    DROP CONSTRAINT sprints_tenant_id_repository_id_run_id_key,
    DROP CONSTRAINT sprints_status_check,
    DROP CONSTRAINT sprints_objective_check,
    DROP COLUMN run_id,
    DROP COLUMN objective;

ALTER TABLE forja.runs
    DROP CONSTRAINT runs_tenant_id_repository_id_run_id_key;
