CREATE TABLE forja.projection_consumers (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    projector_name text NOT NULL CHECK (projector_name ~ '^[a-z][a-z0-9_.-]{0,119}$'),
    status text NOT NULL CHECK (status IN ('active', 'draining', 'retired')),
    configuration_sha256 bytea NOT NULL CHECK (octet_length(configuration_sha256)=32),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    retired_at timestamptz,
    PRIMARY KEY (tenant_id, repository_id, projector_name),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT,
    CHECK ((status='retired') = (retired_at IS NOT NULL))
);

CREATE TABLE forja.projection_deliveries (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    projector_name text NOT NULL,
    outbox_id bigint NOT NULL,
    state text NOT NULL CHECK (state IN ('pending', 'inflight', 'published', 'dead')),
    available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    locked_by text,
    locked_until timestamptz,
    fencing_token bigint NOT NULL DEFAULT 0 CHECK (fencing_token >= 0),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error text,
    published_at timestamptz,
    PRIMARY KEY (tenant_id, repository_id, projector_name, outbox_id),
    FOREIGN KEY (tenant_id, repository_id, projector_name)
        REFERENCES forja.projection_consumers(tenant_id, repository_id, projector_name)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, outbox_id)
        REFERENCES forja.outbox(tenant_id, repository_id, outbox_id) ON DELETE RESTRICT,
    CHECK (
        (state='pending' AND locked_by IS NULL AND locked_until IS NULL AND published_at IS NULL)
        OR (state='inflight' AND locked_by IS NOT NULL AND locked_until IS NOT NULL AND published_at IS NULL)
        OR (state='published' AND locked_by IS NULL AND locked_until IS NULL AND published_at IS NOT NULL)
        OR (state='dead' AND locked_by IS NULL AND locked_until IS NULL AND published_at IS NULL)
    )
);

CREATE INDEX projection_deliveries_claim_idx
    ON forja.projection_deliveries (
        tenant_id, repository_id, projector_name, available_at, outbox_id
    ) WHERE state IN ('pending', 'inflight');

CREATE FUNCTION forja.fanout_projection_delivery()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO forja.projection_deliveries (
        tenant_id, repository_id, projector_name, outbox_id, state, available_at
    )
    SELECT NEW.tenant_id, NEW.repository_id, consumer.projector_name,
           NEW.outbox_id, 'pending', NEW.available_at
    FROM forja.projection_consumers AS consumer
    WHERE consumer.tenant_id=NEW.tenant_id
      AND consumer.repository_id=NEW.repository_id
      AND consumer.status='active';
    RETURN NEW;
END
$$;

CREATE TRIGGER outbox_fans_out_to_active_projectors
    AFTER INSERT ON forja.outbox
    FOR EACH ROW EXECUTE FUNCTION forja.fanout_projection_delivery();

CREATE TABLE forja.retrieval_generations (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    generation_id text NOT NULL CHECK (generation_id ~ '^retrieval_generation_[a-f0-9]{64}$'),
    collection_alias text NOT NULL CHECK (collection_alias ~ '^[a-z][a-z0-9_-]{0,119}$'),
    collection_name text NOT NULL CHECK (collection_name ~ '^[a-z][a-z0-9_-]{0,119}$'),
    embedding_model text NOT NULL CHECK (char_length(embedding_model) BETWEEN 1 AND 200),
    embedding_version text NOT NULL CHECK (char_length(embedding_version) BETWEEN 1 AND 160),
    dimensions integer NOT NULL CHECK (dimensions BETWEEN 1 AND 4096),
    sparse_encoder_version text NOT NULL CHECK (char_length(sparse_encoder_version) BETWEEN 1 AND 160),
    status text NOT NULL CHECK (status IN ('building', 'active', 'draining', 'retired', 'failed')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    activated_at timestamptz,
    retired_at timestamptz,
    PRIMARY KEY (tenant_id, repository_id, generation_id),
    UNIQUE (tenant_id, repository_id, collection_name),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT,
    CHECK (activated_at IS NULL OR activated_at >= created_at),
    CHECK (retired_at IS NULL OR retired_at >= created_at)
);

CREATE UNIQUE INDEX retrieval_generations_one_active_alias_idx
    ON forja.retrieval_generations (tenant_id, repository_id, collection_alias)
    WHERE status='active';

CREATE TABLE forja.retrieval_projection_points (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    generation_id text NOT NULL,
    point_id text NOT NULL CHECK (point_id ~ '^retrieval_[a-f0-9]{64}$'),
    entity_id text NOT NULL CHECK (entity_id ~ '^(symbol|file|artifact|memory|decision|test|incident)_[A-Za-z0-9_-]{1,200}$'),
    artifact_family text NOT NULL CHECK (artifact_family IN ('symbol', 'decision', 'test', 'memory', 'incident')),
    source_commit text CHECK (source_commit IS NULL OR source_commit ~ '^[a-f0-9]{40,64}$'),
    source_sha256 bytea NOT NULL CHECK (octet_length(source_sha256)=32),
    card_sha256 bytea NOT NULL CHECK (octet_length(card_sha256)=32),
    status text NOT NULL CHECK (status IN ('active', 'superseded', 'tombstoned')),
    authority_class text NOT NULL CHECK (authority_class IN ('canonical', 'supporting', 'candidate')),
    stale boolean NOT NULL DEFAULT false,
    language text,
    symbol_kind text,
    repository_path text,
    proof_refs jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(proof_refs)='array'),
    source_outbox_id bigint,
    indexed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    deleted_at timestamptz,
    PRIMARY KEY (tenant_id, repository_id, generation_id, point_id),
    UNIQUE (tenant_id, repository_id, generation_id, entity_id, source_sha256),
    FOREIGN KEY (tenant_id, repository_id, generation_id)
        REFERENCES forja.retrieval_generations(tenant_id, repository_id, generation_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, source_outbox_id)
        REFERENCES forja.outbox(tenant_id, repository_id, outbox_id) ON DELETE RESTRICT,
    CHECK (repository_path IS NULL OR (
        char_length(repository_path) BETWEEN 1 AND 4096
        AND repository_path <> '.'
        AND repository_path !~ '(^/|/$|(^|/)\\.\\.?(?:/|$)|//|\\\\)'
    )),
    CHECK ((status='tombstoned') = (deleted_at IS NOT NULL))
);

CREATE INDEX retrieval_projection_points_resolve_idx
    ON forja.retrieval_projection_points (
        tenant_id, repository_id, generation_id, point_id, status, stale
    );

CREATE INDEX retrieval_projection_points_entity_idx
    ON forja.retrieval_projection_points (
        tenant_id, repository_id, generation_id, entity_id, source_commit
    );
