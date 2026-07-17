DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM forja.runs
        WHERE sprint_id IS NOT NULL
        GROUP BY tenant_id, repository_id, sprint_id
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE='55000',
            MESSAGE='migration 003 requires at most one Run linked to each legacy Sprint';
    END IF;

	IF EXISTS (
		SELECT 1
		FROM forja.sprints AS sprint
		WHERE sprint.status='awaiting_approval'
		   OR sprint.status NOT IN ('proposed', 'approved', 'rejected', 'cancelling')
		   OR (
		       sprint.status='proposed'
		       AND EXISTS (
		           SELECT 1
		           FROM forja.runs AS run
		           WHERE run.tenant_id=sprint.tenant_id
		             AND run.repository_id=sprint.repository_id
		             AND run.sprint_id=sprint.sprint_id
		             AND run.state <> 'draft'
		       )
		   )
		   OR (
		       sprint.status <> 'proposed'
		       AND NOT EXISTS (
		           SELECT 1
		           FROM forja.events AS event
		           WHERE event.tenant_id=sprint.tenant_id
		             AND event.repository_id=sprint.repository_id
		             AND event.aggregate_type='sprint'
		             AND event.aggregate_id='sprint_' || sprint.sprint_id::text
		             AND event.event_type IN (
		                 'sprint.decision_resolved',
		                 'sprint.cancellation_requested'
		             )
		             AND event.payload->>'status'=sprint.status
		       )
		   )
	) THEN
		RAISE EXCEPTION USING
			ERRCODE='55000',
			MESSAGE='migration 003 rejects legacy Sprint approval states without governed event evidence';
	END IF;
END
$$;

ALTER TABLE forja.runs
    ADD CONSTRAINT runs_tenant_id_repository_id_run_id_key
    UNIQUE (tenant_id, repository_id, run_id);

ALTER TABLE forja.sprints
    ADD COLUMN objective text,
    ADD COLUMN run_id text;

WITH linked_runs AS (
    SELECT tenant_id, repository_id, sprint_id, min(run_id) AS run_id
    FROM forja.runs
    WHERE sprint_id IS NOT NULL
    GROUP BY tenant_id, repository_id, sprint_id
)
UPDATE forja.sprints AS sp
SET run_id=linked_runs.run_id
FROM linked_runs
WHERE sp.tenant_id=linked_runs.tenant_id
  AND sp.repository_id=linked_runs.repository_id
  AND sp.sprint_id=linked_runs.sprint_id
  AND sp.run_id IS NULL;

UPDATE forja.sprints AS sp
SET objective=COALESCE(
    (
        SELECT r.objective
        FROM forja.runs AS r
        WHERE r.tenant_id=sp.tenant_id
          AND r.repository_id=sp.repository_id
          AND r.run_id=sp.run_id
    ),
    CASE
        WHEN char_length(sp.title) >= 3 THEN sp.title
        ELSE 'Legacy Sprint ' || sp.sequence_number::text
    END
)
WHERE sp.objective IS NULL;

CREATE TEMP TABLE forja_legacy_sprint_runs ON COMMIT DROP AS
WITH legacy_hashes AS (
    SELECT
        sp.tenant_id,
        sp.repository_id,
        sp.sprint_id,
        sp.objective,
        sp.created_at,
        sp.updated_at,
        md5(
            'forja-legacy-sprint-run:' || sp.tenant_id::text || ':' ||
            sp.repository_id::text || ':' || sp.sprint_id::text
        ) AS value_hash,
        md5(
            'forja-legacy-sprint-event:' || sp.tenant_id::text || ':' ||
            sp.repository_id::text || ':' || sp.sprint_id::text
        ) AS event_hash
    FROM forja.sprints AS sp
    WHERE sp.run_id IS NULL
)
SELECT
    tenant_id,
    repository_id,
    sprint_id,
    objective,
    created_at,
    updated_at,
    'event_' || event_hash AS event_id,
    'run_' ||
        substr(value_hash, 1, 8) || '-' ||
        substr(value_hash, 9, 4) || '-4' ||
        substr(value_hash, 14, 3) || '-8' ||
        substr(value_hash, 18, 3) || '-' ||
        substr(value_hash, 21, 12) AS run_id
FROM legacy_hashes;

INSERT INTO forja.runs (
    run_id, tenant_id, repository_id, sprint_id, objective, state,
    version, created_at, updated_at
)
SELECT
    run_id, tenant_id, repository_id, sprint_id, objective, 'draft',
    1, created_at, created_at
FROM forja_legacy_sprint_runs;

INSERT INTO forja.events (
    event_id, tenant_id, repository_id, aggregate_type, aggregate_id,
    aggregate_version, event_type, schema_version, occurred_at,
    actor_type, actor_id, correlation_id, causation_id,
    idempotency_key, payload
)
SELECT
    event_id, tenant_id, repository_id, 'run', run_id,
    1, 'run.created', '1.0', created_at,
    'system', 'migration-003', 'migration-003-legacy-run', NULL,
    'migration-003-legacy-run',
    jsonb_build_object(
        'run_id', run_id,
        'schema_version', '1.0',
        'objective', objective,
        'state', 'draft',
        'version', 1,
        'created_at', to_char(
            created_at AT TIME ZONE 'UTC',
            'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'
        ),
        'updated_at', to_char(
            created_at AT TIME ZONE 'UTC',
            'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'
        )
    )
FROM forja_legacy_sprint_runs;

INSERT INTO forja.outbox (event_id, tenant_id, repository_id)
SELECT event_id, tenant_id, repository_id
FROM forja_legacy_sprint_runs;

UPDATE forja.sprints AS sp
SET run_id=legacy.run_id
FROM forja_legacy_sprint_runs AS legacy
WHERE sp.tenant_id=legacy.tenant_id
  AND sp.repository_id=legacy.repository_id
  AND sp.sprint_id=legacy.sprint_id;

-- Older schemas stored Sprint rows without an immutable Sprint event stream.
-- Establish one explicit migration baseline for every such aggregate so a
-- restore can reconstruct and compare all governed canonical state.
WITH migrated_sprints AS (
    INSERT INTO forja.events (
        event_id, tenant_id, repository_id, aggregate_type, aggregate_id,
        aggregate_version, event_type, schema_version, occurred_at,
        actor_type, actor_id, correlation_id, causation_id,
        idempotency_key, payload
    )
    SELECT
        'event_legacy_sprint_' || md5(
            sp.tenant_id::text || ':' || sp.repository_id::text || ':' ||
            sp.sprint_id::text
        ),
        sp.tenant_id,
        sp.repository_id,
        'sprint',
        'sprint_' || sp.sprint_id::text,
        sp.version,
        'sprint.migrated',
        '1.0',
        sp.updated_at,
        'system',
        'migration-003',
        'migration-003-legacy-sprint',
        NULL,
        'migration-003-legacy-sprint',
        jsonb_build_object(
            'sprint_id', 'sprint_' || sp.sprint_id::text,
            'schema_version', '1.0',
            'sequence_number', sp.sequence_number,
            'title', sp.title,
            'objective', sp.objective,
            'status', sp.status,
            'version', sp.version,
            'run_id', sp.run_id,
            'created_at', to_char(
                sp.created_at AT TIME ZONE 'UTC',
                'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'
            ),
            'updated_at', to_char(
                sp.updated_at AT TIME ZONE 'UTC',
                'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'
            )
        )
    FROM forja.sprints AS sp
    WHERE NOT EXISTS (
        SELECT 1
        FROM forja.events AS event
        WHERE event.tenant_id=sp.tenant_id
          AND event.repository_id=sp.repository_id
          AND event.aggregate_type='sprint'
          AND event.aggregate_id='sprint_' || sp.sprint_id::text
    )
    ON CONFLICT (event_id) DO NOTHING
    RETURNING event_id, tenant_id, repository_id
)
INSERT INTO forja.outbox (event_id, tenant_id, repository_id)
SELECT event_id, tenant_id, repository_id
FROM migrated_sprints;

ALTER TABLE forja.sprints
    ALTER COLUMN objective SET NOT NULL,
    ALTER COLUMN run_id SET NOT NULL,
    ADD CONSTRAINT sprints_objective_check
        CHECK (char_length(objective) BETWEEN 3 AND 8000),
    ADD CONSTRAINT sprints_status_check
        CHECK (status IN (
            'proposed',
            'awaiting_approval',
            'approved',
            'rejected',
            'cancelling'
        )),
    ADD CONSTRAINT sprints_tenant_id_repository_id_run_id_key
        UNIQUE (tenant_id, repository_id, run_id),
    ADD CONSTRAINT sprints_tenant_id_repository_id_run_id_fkey
        FOREIGN KEY (tenant_id, repository_id, run_id)
        REFERENCES forja.runs (tenant_id, repository_id, run_id)
        ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE forja.decisions (
    decision_id text PRIMARY KEY CHECK (
        decision_id ~ '^decision_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    ),
    tenant_id uuid NOT NULL REFERENCES forja.tenants(tenant_id) ON DELETE RESTRICT,
    repository_id uuid NOT NULL,
    sprint_id uuid NOT NULL,
    run_id text NOT NULL,
    action text NOT NULL CHECK (action='submit_sprint'),
    risk_class text NOT NULL CHECK (risk_class IN ('low', 'medium', 'high', 'critical')),
    status text NOT NULL CHECK (status IN ('pending', 'approved', 'rejected')),
    version integer NOT NULL CHECK (version > 0),
    requested_by text NOT NULL CHECK (char_length(requested_by) BETWEEN 1 AND 160),
    decided_by text CHECK (char_length(decided_by) BETWEEN 1 AND 160),
    reason text CHECK (char_length(reason) BETWEEN 3 AND 2000),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    decided_at timestamptz,
    CHECK (updated_at >= created_at),
    CHECK (decided_at IS NULL OR decided_at >= created_at),
    CHECK (
        (status='pending' AND decided_by IS NULL AND reason IS NULL AND decided_at IS NULL)
        OR
        (status IN ('approved', 'rejected') AND decided_by IS NOT NULL AND reason IS NOT NULL AND decided_at IS NOT NULL)
    ),
    UNIQUE (tenant_id, repository_id, decision_id),
    FOREIGN KEY (tenant_id, repository_id, sprint_id)
        REFERENCES forja.sprints (tenant_id, repository_id, sprint_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, run_id)
        REFERENCES forja.runs (tenant_id, repository_id, run_id)
        ON DELETE RESTRICT
);

CREATE UNIQUE INDEX decisions_one_pending_action_per_sprint_idx
    ON forja.decisions (tenant_id, repository_id, sprint_id, action)
    WHERE status='pending';

CREATE INDEX decisions_run_status_idx
    ON forja.decisions (tenant_id, repository_id, run_id, status);

ALTER TABLE forja.events
    DROP CONSTRAINT events_aggregate_type_check,
    ADD CONSTRAINT events_aggregate_type_check CHECK (
        aggregate_type IN (
            'sprint',
            'task',
            'run',
            'attempt',
            'approval',
            'decision',
            'audit',
            'artifact',
            'projection'
        )
    );
