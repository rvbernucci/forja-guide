CREATE TABLE forja.delivery_publications (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    delivery_id text NOT NULL CHECK (
        delivery_id ~ '^delivery_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    ),
    attempt_id text NOT NULL CHECK (
        attempt_id ~ '^attempt_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    ),
    lease_set_id text NOT NULL CHECK (char_length(lease_set_id) BETWEEN 1 AND 500),
    lease_ttl_ms integer NOT NULL CHECK (lease_ttl_ms BETWEEN 60000 AND 86400000),
    publication_ref text NOT NULL CHECK (
        publication_ref = 'refs/forja/deliveries/' || delivery_id
    ),
    publication_previous_commit text CHECK (
        publication_previous_commit ~ '^[0-9a-f]{40}$'
    ),
    result_commit text NOT NULL CHECK (result_commit ~ '^[0-9a-f]{40}$'),
    authority_sha256 bytea NOT NULL CHECK (octet_length(authority_sha256)=32),
    receipt_sha256 bytea NOT NULL CHECK (octet_length(receipt_sha256)=32),
    intent_sha256 bytea NOT NULL CHECK (octet_length(intent_sha256)=32),
    receipt_bytes bytea NOT NULL CHECK (
        octet_length(receipt_bytes) BETWEEN 2 AND 4194304
    ),
    state text NOT NULL CHECK (
        state IN ('prepared', 'published', 'conflict', 'abandoned')
    ),
    observed_commit text CHECK (observed_commit ~ '^[0-9a-f]{40}$'),
    prepared_at timestamptz NOT NULL,
    published_at timestamptz,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, repository_id, delivery_id, attempt_id),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, lease_set_id)
        REFERENCES forja.lease_sets(
            tenant_id, repository_id, lease_set_id
        ) ON DELETE RESTRICT,
    CHECK (lease_set_id=attempt_id),
    CHECK (
        (state='published' AND published_at IS NOT NULL AND observed_commit=result_commit)
        OR (state<>'published' AND published_at IS NULL)
    )
);

CREATE UNIQUE INDEX delivery_publications_active_ref_idx
    ON forja.delivery_publications(tenant_id, repository_id, publication_ref)
    WHERE state IN ('prepared', 'published');

CREATE INDEX delivery_publications_recovery_idx
    ON forja.delivery_publications(tenant_id, repository_id, state, updated_at);
