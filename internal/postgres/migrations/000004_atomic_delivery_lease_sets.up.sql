ALTER TABLE forja.leases
    DROP CONSTRAINT leases_resource_type_check;

ALTER TABLE forja.leases
    ADD CONSTRAINT leases_resource_type_check CHECK (
        resource_type IN ('worker', 'scheduler', 'file', 'worktree', 'artifact')
    );

CREATE TABLE forja.lease_sets (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    lease_set_id text NOT NULL CHECK (char_length(lease_set_id) BETWEEN 1 AND 500),
    owner_id text NOT NULL CHECK (char_length(owner_id) BETWEEN 1 AND 500),
    member_digest bytea NOT NULL CHECK (octet_length(member_digest)=32),
    state text NOT NULL CHECK (state IN ('active', 'released')),
    acquired_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, repository_id, lease_set_id),
    CHECK (expires_at >= acquired_at),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT
);

CREATE TABLE forja.lease_set_members (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    lease_set_id text NOT NULL,
    resource_type text NOT NULL CHECK (
        resource_type IN ('file', 'worktree', 'artifact')
    ),
    resource_id text NOT NULL CHECK (char_length(resource_id) BETWEEN 1 AND 500),
    fencing_token bigint NOT NULL CHECK (fencing_token > 0),
    PRIMARY KEY (
        tenant_id, repository_id, lease_set_id, resource_type, resource_id
    ),
    FOREIGN KEY (tenant_id, repository_id, lease_set_id)
        REFERENCES forja.lease_sets(
            tenant_id, repository_id, lease_set_id
        ) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, resource_type, resource_id)
        REFERENCES forja.leases(
            tenant_id, repository_id, resource_type, resource_id
        ) ON DELETE RESTRICT
);

CREATE INDEX lease_set_members_resource_idx
    ON forja.lease_set_members(
        tenant_id, repository_id, resource_type, resource_id
    );
