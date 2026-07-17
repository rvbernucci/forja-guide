CREATE SCHEMA IF NOT EXISTS forja;

CREATE TABLE IF NOT EXISTS forja.schema_migrations (
    version bigint PRIMARY KEY,
    name text NOT NULL,
    checksum text NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE forja.tenants (
    tenant_id uuid PRIMARY KEY,
    slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
    display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE forja.repositories (
    repository_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES forja.tenants(tenant_id) ON DELETE RESTRICT,
    canonical_name text NOT NULL CHECK (char_length(canonical_name) BETWEEN 1 AND 500),
    default_branch text NOT NULL DEFAULT 'main',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, repository_id),
    UNIQUE (tenant_id, canonical_name)
);

CREATE TABLE forja.sprints (
    sprint_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES forja.tenants(tenant_id) ON DELETE RESTRICT,
    repository_id uuid NOT NULL,
    sequence_number integer NOT NULL CHECK (sequence_number >= 0),
    title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 500),
    status text NOT NULL DEFAULT 'proposed',
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, sprint_id),
    UNIQUE (tenant_id, repository_id, sprint_id),
    UNIQUE (repository_id, sequence_number),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT
);

CREATE TABLE forja.tasks (
    task_id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES forja.tenants(tenant_id) ON DELETE RESTRICT,
    sprint_id uuid NOT NULL,
    title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 1000),
    status text NOT NULL DEFAULT 'pending',
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (tenant_id, sprint_id)
        REFERENCES forja.sprints(tenant_id, sprint_id) ON DELETE RESTRICT
);

CREATE TABLE forja.runs (
    run_id text PRIMARY KEY CHECK (
        run_id ~ '^run_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    ),
    tenant_id uuid NOT NULL REFERENCES forja.tenants(tenant_id) ON DELETE RESTRICT,
    repository_id uuid NOT NULL,
    sprint_id uuid,
    objective text NOT NULL CHECK (char_length(objective) BETWEEN 3 AND 8000),
    state text NOT NULL,
    version integer NOT NULL CHECK (version > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (updated_at >= created_at),
    UNIQUE (tenant_id, run_id),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, sprint_id)
        REFERENCES forja.sprints(
            tenant_id,
            repository_id,
            sprint_id
        ) ON DELETE RESTRICT
);

CREATE TABLE forja.attempts (
    attempt_id text PRIMARY KEY CHECK (attempt_id ~ '^attempt_[A-Za-z0-9_-]+$'),
    tenant_id uuid NOT NULL REFERENCES forja.tenants(tenant_id) ON DELETE RESTRICT,
    run_id text NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal > 0),
    status text NOT NULL CHECK (char_length(status) BETWEEN 1 AND 100),
    lease_resource_type text NOT NULL CHECK (lease_resource_type='scheduler'),
    lease_resource_id text NOT NULL CHECK (
        char_length(lease_resource_id) BETWEEN 1 AND 500
    ),
    worker_id text NOT NULL CHECK (char_length(worker_id) BETWEEN 1 AND 500),
    fencing_token bigint NOT NULL CHECK (fencing_token > 0),
    started_at timestamptz,
    finished_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (run_id, ordinal),
    FOREIGN KEY (tenant_id, run_id)
        REFERENCES forja.runs(tenant_id, run_id) ON DELETE RESTRICT
);

CREATE TABLE forja.events (
    event_id text PRIMARY KEY CHECK (event_id ~ '^event_[A-Za-z0-9_-]+$'),
    tenant_id uuid NOT NULL REFERENCES forja.tenants(tenant_id) ON DELETE RESTRICT,
    repository_id uuid NOT NULL,
    aggregate_type text NOT NULL CHECK (
        aggregate_type IN (
            'sprint', 'task', 'run', 'attempt', 'approval', 'artifact', 'projection'
        )
    ),
    aggregate_id text NOT NULL CHECK (char_length(aggregate_id) BETWEEN 3 AND 160),
    aggregate_version integer NOT NULL CHECK (aggregate_version > 0),
    event_type text NOT NULL CHECK (char_length(event_type) BETWEEN 3 AND 120),
    schema_version text NOT NULL CHECK (schema_version='1.0'),
    occurred_at timestamptz NOT NULL,
    actor_type text NOT NULL CHECK (
        actor_type IN ('human', 'agent', 'worker', 'system')
    ),
    actor_id text NOT NULL CHECK (char_length(actor_id) BETWEEN 1 AND 160),
    correlation_id text NOT NULL CHECK (
        char_length(correlation_id) BETWEEN 3 AND 160
    ),
    causation_id text CHECK (char_length(causation_id) <= 160),
    idempotency_key text NOT NULL CHECK (
        char_length(idempotency_key) BETWEEN 8 AND 200
    ),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload)='object'),
    UNIQUE (tenant_id, repository_id, event_id),
    UNIQUE (
        tenant_id,
        repository_id,
        aggregate_type,
        aggregate_id,
        aggregate_version
    ),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT
);

CREATE INDEX events_replay_idx
    ON forja.events (
        tenant_id,
        repository_id,
        aggregate_type,
        aggregate_id,
        aggregate_version
    );

CREATE FUNCTION forja.reject_event_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'forja events are append-only';
END
$$;

CREATE TRIGGER events_are_append_only
BEFORE UPDATE OR DELETE ON forja.events
FOR EACH ROW
EXECUTE FUNCTION forja.reject_event_mutation();

CREATE TABLE forja.idempotency_keys (
    tenant_id uuid NOT NULL REFERENCES forja.tenants(tenant_id) ON DELETE RESTRICT,
    scope text NOT NULL,
    idempotency_key text NOT NULL,
    request_hash bytea NOT NULL,
    response_status integer NOT NULL,
    response_body jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz,
    PRIMARY KEY (tenant_id, scope, idempotency_key)
);

CREATE TABLE forja.leases (
    tenant_id uuid NOT NULL REFERENCES forja.tenants(tenant_id) ON DELETE RESTRICT,
    repository_id uuid NOT NULL,
    resource_type text NOT NULL CHECK (
        resource_type IN ('worker', 'scheduler', 'file', 'worktree')
    ),
    resource_id text NOT NULL,
    owner_id text NOT NULL,
    fencing_token bigint NOT NULL CHECK (fencing_token > 0),
    acquired_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, repository_id, resource_type, resource_id),
    CHECK (expires_at >= acquired_at),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT
);

CREATE FUNCTION forja.enforce_attempt_commit_fence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    live_fence boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1
        FROM forja.runs AS r
        JOIN forja.leases AS l
          ON l.tenant_id=NEW.tenant_id
         AND l.repository_id=r.repository_id
         AND l.resource_type=NEW.lease_resource_type
         AND l.resource_id=NEW.lease_resource_id
         AND l.owner_id=NEW.worker_id
         AND l.fencing_token=NEW.fencing_token
        WHERE r.tenant_id=NEW.tenant_id
          AND r.run_id=NEW.run_id
          AND l.expires_at > clock_timestamp()
    ) INTO live_fence;
    IF NOT live_fence THEN
        RAISE EXCEPTION 'attempt lease expired before commit'
            USING ERRCODE='40001';
    END IF;
    RETURN NEW;
END
$$;

CREATE CONSTRAINT TRIGGER zz_attempt_requires_live_lease_at_commit
AFTER INSERT ON forja.attempts
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION forja.enforce_attempt_commit_fence();

CREATE TABLE forja.outbox (
    outbox_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES forja.tenants(tenant_id) ON DELETE RESTRICT,
    repository_id uuid NOT NULL,
    event_id text NOT NULL UNIQUE,
    state text NOT NULL DEFAULT 'pending' CHECK (
        state IN ('pending', 'inflight', 'published', 'dead')
    ),
    available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    locked_by text,
    locked_until timestamptz,
    fencing_token bigint NOT NULL DEFAULT 0 CHECK (fencing_token >= 0),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    published_at timestamptz,
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, event_id)
        REFERENCES forja.events(tenant_id, repository_id, event_id) ON DELETE RESTRICT
);

CREATE INDEX outbox_claim_idx
    ON forja.outbox (tenant_id, repository_id, available_at, outbox_id)
    WHERE state IN ('pending', 'inflight');

CREATE FUNCTION forja.enforce_outbox_claim_at_commit()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.locked_by IS NULL
       OR OLD.locked_until IS NULL
       OR OLD.locked_until <= clock_timestamp() THEN
        RAISE EXCEPTION 'outbox claim expired before commit'
            USING ERRCODE='40001';
    END IF;
    RETURN NEW;
END
$$;

CREATE CONSTRAINT TRIGGER zz_outbox_requires_live_claim_at_commit
AFTER UPDATE ON forja.outbox
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
WHEN (OLD.state='inflight' AND NEW.state<>'inflight')
EXECUTE FUNCTION forja.enforce_outbox_claim_at_commit();

CREATE TABLE forja.projection_checkpoints (
    tenant_id uuid NOT NULL REFERENCES forja.tenants(tenant_id) ON DELETE RESTRICT,
    repository_id uuid NOT NULL,
    projector_name text NOT NULL,
    last_outbox_id bigint NOT NULL DEFAULT 0 CHECK (last_outbox_id >= 0),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, repository_id, projector_name),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT
);

CREATE TABLE forja.projection_dead_letters (
    dead_letter_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES forja.tenants(tenant_id) ON DELETE RESTRICT,
    repository_id uuid NOT NULL,
    projector_name text NOT NULL,
    outbox_id bigint REFERENCES forja.outbox(outbox_id) ON DELETE RESTRICT,
    event_id text NOT NULL,
    error_class text NOT NULL,
    error_message text NOT NULL,
    payload jsonb NOT NULL,
    failed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    resolved_at timestamptz,
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, event_id)
        REFERENCES forja.events(tenant_id, repository_id, event_id) ON DELETE RESTRICT
);

CREATE TABLE forja.run_projections (
    tenant_id uuid NOT NULL REFERENCES forja.tenants(tenant_id) ON DELETE RESTRICT,
    repository_id uuid NOT NULL,
    projector_name text NOT NULL,
    run_id text NOT NULL,
    objective text NOT NULL,
    state text NOT NULL,
    aggregate_version integer NOT NULL CHECK (aggregate_version > 0),
    source_event_id text NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, repository_id, projector_name, run_id),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, source_event_id)
        REFERENCES forja.events(tenant_id, repository_id, event_id) ON DELETE RESTRICT
);

INSERT INTO forja.tenants (tenant_id, slug, display_name)
VALUES ('00000000-0000-4000-8000-000000000001', 'local', 'Local development');

INSERT INTO forja.repositories (
    repository_id,
    tenant_id,
    canonical_name
) VALUES (
    '00000000-0000-4000-8000-000000000002',
    '00000000-0000-4000-8000-000000000001',
    'local/default'
);
