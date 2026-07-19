CREATE TABLE forja.artifact_objects (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256)=32),
    object_key text NOT NULL CHECK (char_length(object_key) BETWEEN 1 AND 1024),
    size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 0 AND 4294967296),
    media_type text NOT NULL CHECK (char_length(media_type) BETWEEN 3 AND 120),
    state text NOT NULL CHECK (
        state IN ('reserved', 'uploading', 'verified', 'active', 'failed', 'tombstoned', 'purged')
    ),
    provider_checksum_sha256 text CHECK (provider_checksum_sha256 ~ '^[A-Za-z0-9+/]{43}=$'),
    provider_etag text CHECK (char_length(provider_etag) BETWEEN 1 AND 300),
    provider_version text CHECK (char_length(provider_version) BETWEEN 1 AND 300),
    failure_class text CHECK (
        failure_class IN ('retryable_provider', 'integrity', 'canonical_conflict', 'interrupted')
    ),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    verified_at timestamptz,
    activated_at timestamptz,
    tombstoned_at timestamptz,
    purged_at timestamptz,
    PRIMARY KEY (tenant_id, repository_id, content_sha256),
    UNIQUE (tenant_id, repository_id, object_key),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT,
    CHECK (updated_at >= created_at),
    CHECK (verified_at IS NULL OR verified_at >= created_at),
    CHECK (activated_at IS NULL OR activated_at >= COALESCE(verified_at, created_at)),
    CHECK (tombstoned_at IS NULL OR tombstoned_at >= created_at),
    CHECK (purged_at IS NULL OR purged_at >= COALESCE(tombstoned_at, created_at)),
    CHECK (
        object_key =
            'tenants/' || tenant_id::text ||
            '/repositories/' || repository_id::text ||
            '/sha256/' || substr(encode(content_sha256, 'hex'), 1, 2) ||
            '/' || substr(encode(content_sha256, 'hex'), 3)
    ),
    CHECK (
        (state IN ('reserved', 'uploading') AND verified_at IS NULL AND activated_at IS NULL
            AND tombstoned_at IS NULL AND purged_at IS NULL AND failure_class IS NULL)
        OR (state='verified' AND verified_at IS NOT NULL AND activated_at IS NULL
            AND tombstoned_at IS NULL AND purged_at IS NULL AND failure_class IS NULL)
        OR (state='active' AND verified_at IS NOT NULL AND activated_at IS NOT NULL
            AND tombstoned_at IS NULL AND purged_at IS NULL AND failure_class IS NULL)
        OR (state='failed' AND failure_class IS NOT NULL AND activated_at IS NULL
            AND tombstoned_at IS NULL AND purged_at IS NULL)
        OR (state='tombstoned' AND tombstoned_at IS NOT NULL AND purged_at IS NULL)
        OR (state='purged' AND tombstoned_at IS NOT NULL AND purged_at IS NOT NULL)
    )
);

CREATE INDEX artifact_objects_reconciliation_idx
    ON forja.artifact_objects (tenant_id, repository_id, state, updated_at);

CREATE TABLE forja.artifact_operations (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    operation_id text NOT NULL CHECK (
        operation_id ~ '^artifact_operation_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    ),
    artifact_id text NOT NULL CHECK (artifact_id ~ '^artifact_[A-Za-z0-9_-]+$'),
    content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256)=32),
    expected_size_bytes bigint NOT NULL CHECK (expected_size_bytes BETWEEN 0 AND 4294967296),
    expected_media_type text NOT NULL CHECK (char_length(expected_media_type) BETWEEN 3 AND 120),
    state text NOT NULL CHECK (
        state IN ('reserved', 'uploading', 'verified', 'active', 'failed', 'reconciliation_required')
    ),
    version integer NOT NULL CHECK (version > 0),
    failure_class text CHECK (
        failure_class IN ('retryable_provider', 'integrity', 'canonical_conflict', 'interrupted')
    ),
    created_by text NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 160),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, repository_id, operation_id),
    UNIQUE (tenant_id, repository_id, artifact_id),
    FOREIGN KEY (tenant_id, repository_id, content_sha256)
        REFERENCES forja.artifact_objects(tenant_id, repository_id, content_sha256)
        ON DELETE RESTRICT,
    CHECK (updated_at >= created_at),
    CHECK (
        (state='failed' AND failure_class IS NOT NULL)
        OR (state<>'failed' AND failure_class IS NULL)
    )
);

CREATE INDEX artifact_operations_recovery_idx
    ON forja.artifact_operations (tenant_id, repository_id, state, updated_at);

CREATE TABLE forja.artifacts (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    artifact_id text NOT NULL CHECK (artifact_id ~ '^artifact_[A-Za-z0-9_-]+$'),
    operation_id text NOT NULL,
    run_id text,
    kind text NOT NULL CHECK (kind IN (
        'sprint_plan', 'context_pack', 'patch', 'test_report', 'validation_report',
        'evidence_bundle', 'decision', 'conversation', 'memory', 'index_snapshot',
        'runtime_receipt'
    )),
    status text NOT NULL CHECK (status IN (
        'draft', 'active', 'validated', 'rejected', 'superseded', 'archived', 'quarantined'
    )),
    version integer NOT NULL CHECK (version > 0),
    content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256)=32),
    media_type text NOT NULL CHECK (char_length(media_type) BETWEEN 3 AND 120),
    size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 0 AND 4294967296),
    created_by text NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 160),
    provenance jsonb NOT NULL CHECK (jsonb_typeof(provenance)='object'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata)='object'),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    tombstoned_at timestamptz,
    PRIMARY KEY (tenant_id, repository_id, artifact_id),
    UNIQUE (tenant_id, repository_id, artifact_id, content_sha256),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, operation_id)
        REFERENCES forja.artifact_operations(tenant_id, repository_id, operation_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, content_sha256)
        REFERENCES forja.artifact_objects(tenant_id, repository_id, content_sha256)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, run_id)
        REFERENCES forja.runs(tenant_id, repository_id, run_id) ON DELETE RESTRICT,
    CHECK (updated_at >= created_at),
    CHECK (tombstoned_at IS NULL OR tombstoned_at >= created_at)
);

CREATE INDEX artifacts_status_idx
    ON forja.artifacts (tenant_id, repository_id, status, created_at);

CREATE TABLE forja.artifact_supersessions (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    artifact_id text NOT NULL,
    superseded_artifact_id text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, repository_id, artifact_id, superseded_artifact_id),
    FOREIGN KEY (tenant_id, repository_id, artifact_id)
        REFERENCES forja.artifacts(tenant_id, repository_id, artifact_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, superseded_artifact_id)
        REFERENCES forja.artifacts(tenant_id, repository_id, artifact_id) ON DELETE RESTRICT,
    CHECK (artifact_id <> superseded_artifact_id)
);

CREATE TABLE forja.artifact_bundle_manifests (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    manifest_id text NOT NULL CHECK (
        manifest_id ~ '^manifest_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    ),
    family text NOT NULL CHECK (
        family IN ('evidence', 'conversation_transcript', 'dataset', 'report', 'snapshot')
    ),
    total_size_bytes bigint NOT NULL CHECK (total_size_bytes BETWEEN 0 AND 17179869184),
    entry_count integer NOT NULL CHECK (entry_count BETWEEN 1 AND 4096),
    source_refs jsonb NOT NULL CHECK (jsonb_typeof(source_refs)='array'),
    created_by text NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 160),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, repository_id, manifest_id),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT
);

CREATE TABLE forja.artifact_bundle_entries (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    manifest_id text NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 4095),
    logical_path text NOT NULL CHECK (
        char_length(logical_path) BETWEEN 1 AND 4096
        AND logical_path ~ '^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$'
        AND logical_path <> '.'
        AND logical_path NOT LIKE '../%'
    ),
    artifact_id text NOT NULL,
    content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256)=32),
    size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 0 AND 4294967296),
    media_type text NOT NULL CHECK (char_length(media_type) BETWEEN 3 AND 120),
    PRIMARY KEY (tenant_id, repository_id, manifest_id, ordinal),
    UNIQUE (tenant_id, repository_id, manifest_id, logical_path),
    FOREIGN KEY (tenant_id, repository_id, manifest_id)
        REFERENCES forja.artifact_bundle_manifests(tenant_id, repository_id, manifest_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, artifact_id, content_sha256)
        REFERENCES forja.artifacts(tenant_id, repository_id, artifact_id, content_sha256)
        ON DELETE RESTRICT
);

CREATE TABLE forja.conversations (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    conversation_id text NOT NULL CHECK (
        conversation_id ~ '^conversation_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    ),
    status text NOT NULL CHECK (status IN ('active', 'closed', 'tombstoned')),
    version integer NOT NULL CHECK (version > 0),
    retention_class text NOT NULL CHECK (
        retention_class IN ('ephemeral', 'project', 'regulated', 'indefinite')
    ),
    created_by text NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 160),
    transcript_artifact_id text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    closed_at timestamptz,
    tombstoned_at timestamptz,
    PRIMARY KEY (tenant_id, repository_id, conversation_id),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, transcript_artifact_id)
        REFERENCES forja.artifacts(tenant_id, repository_id, artifact_id) ON DELETE RESTRICT,
    CHECK (updated_at >= created_at),
    CHECK (closed_at IS NULL OR closed_at >= created_at),
    CHECK (tombstoned_at IS NULL OR tombstoned_at >= COALESCE(closed_at, created_at)),
    CHECK (
        (status='active' AND transcript_artifact_id IS NULL AND closed_at IS NULL AND tombstoned_at IS NULL)
        OR (status='closed' AND transcript_artifact_id IS NOT NULL AND closed_at IS NOT NULL AND tombstoned_at IS NULL)
        OR (status='tombstoned' AND tombstoned_at IS NOT NULL
            AND ((transcript_artifact_id IS NULL AND closed_at IS NULL)
                OR (transcript_artifact_id IS NOT NULL AND closed_at IS NOT NULL)))
    )
);

CREATE TABLE forja.messages (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    message_id text NOT NULL CHECK (
        message_id ~ '^message_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    ),
    conversation_id text NOT NULL,
    sequence_number integer NOT NULL CHECK (sequence_number > 0),
    role text NOT NULL CHECK (role IN ('human', 'assistant', 'system', 'tool')),
    author_id text NOT NULL CHECK (char_length(author_id) BETWEEN 1 AND 160),
    content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256)=32),
    supersedes_message_id text,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, repository_id, message_id),
    UNIQUE (tenant_id, repository_id, conversation_id, sequence_number),
    FOREIGN KEY (tenant_id, repository_id, conversation_id)
        REFERENCES forja.conversations(tenant_id, repository_id, conversation_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, supersedes_message_id)
        REFERENCES forja.messages(tenant_id, repository_id, message_id)
        ON DELETE RESTRICT,
    CHECK (supersedes_message_id IS NULL OR supersedes_message_id <> message_id)
);

CREATE TABLE forja.message_parts (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    message_id text NOT NULL,
    part_id text NOT NULL CHECK (
        part_id ~ '^part_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    ),
    ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 63),
    kind text NOT NULL CHECK (kind IN (
        'text', 'json', 'code', 'image', 'audio', 'video', 'file', 'tool_call', 'tool_result'
    )),
    artifact_id text NOT NULL,
    content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256)=32),
    media_type text NOT NULL CHECK (char_length(media_type) BETWEEN 3 AND 120),
    size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 0 AND 4294967296),
    PRIMARY KEY (tenant_id, repository_id, message_id, part_id),
    UNIQUE (tenant_id, repository_id, message_id, ordinal),
    FOREIGN KEY (tenant_id, repository_id, message_id)
        REFERENCES forja.messages(tenant_id, repository_id, message_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, artifact_id, content_sha256)
        REFERENCES forja.artifacts(tenant_id, repository_id, artifact_id, content_sha256)
        ON DELETE RESTRICT
);

CREATE TABLE forja.message_citations (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    message_id text NOT NULL,
    citation_id text NOT NULL CHECK (
        citation_id ~ '^citation_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    ),
    ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 127),
    source_artifact_id text NOT NULL,
    source_content_sha256 bytea NOT NULL CHECK (octet_length(source_content_sha256)=32),
    locator_kind text NOT NULL CHECK (locator_kind IN (
        'whole', 'line_range', 'page_range', 'time_range', 'json_pointer', 'uri_fragment'
    )),
    locator_value text NOT NULL CHECK (char_length(locator_value) BETWEEN 1 AND 500),
    PRIMARY KEY (tenant_id, repository_id, message_id, citation_id),
    UNIQUE (tenant_id, repository_id, message_id, ordinal),
    FOREIGN KEY (tenant_id, repository_id, message_id)
        REFERENCES forja.messages(tenant_id, repository_id, message_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, source_artifact_id, source_content_sha256)
        REFERENCES forja.artifacts(tenant_id, repository_id, artifact_id, content_sha256)
        ON DELETE RESTRICT
);

CREATE TABLE forja.memory_candidates (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    candidate_id text NOT NULL CHECK (
        candidate_id ~ '^memory_candidate_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    ),
    conversation_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('fact', 'preference', 'decision', 'lesson')),
    proposed_artifact_id text NOT NULL,
    proposed_content_sha256 bytea NOT NULL CHECK (octet_length(proposed_content_sha256)=32),
    status text NOT NULL CHECK (status IN ('proposed', 'promoted', 'rejected', 'expired')),
    version integer NOT NULL CHECK (version > 0),
    proposed_by text NOT NULL CHECK (char_length(proposed_by) BETWEEN 1 AND 160),
    proposed_at timestamptz NOT NULL,
    expires_at timestamptz,
    memory_id text,
    resolved_by text CHECK (char_length(resolved_by) BETWEEN 1 AND 160),
    resolution_reason text CHECK (char_length(resolution_reason) BETWEEN 1 AND 2000),
    resolved_at timestamptz,
    PRIMARY KEY (tenant_id, repository_id, candidate_id),
    UNIQUE (tenant_id, repository_id, memory_id),
    FOREIGN KEY (tenant_id, repository_id, conversation_id)
        REFERENCES forja.conversations(tenant_id, repository_id, conversation_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, proposed_artifact_id, proposed_content_sha256)
        REFERENCES forja.artifacts(tenant_id, repository_id, artifact_id, content_sha256)
        ON DELETE RESTRICT,
    CHECK (expires_at IS NULL OR expires_at > proposed_at),
    CHECK (resolved_at IS NULL OR resolved_at >= proposed_at),
    CHECK (
        (status='proposed' AND memory_id IS NULL AND resolved_by IS NULL
            AND resolution_reason IS NULL AND resolved_at IS NULL)
        OR (status='promoted' AND memory_id IS NOT NULL AND resolved_by IS NOT NULL
            AND resolution_reason IS NOT NULL AND resolved_at IS NOT NULL)
        OR (status IN ('rejected', 'expired') AND memory_id IS NULL AND resolved_by IS NOT NULL
            AND resolution_reason IS NOT NULL AND resolved_at IS NOT NULL)
    )
);

CREATE TABLE forja.memory_candidate_sources (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    candidate_id text NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 127),
    message_id text NOT NULL,
    PRIMARY KEY (tenant_id, repository_id, candidate_id, ordinal),
    UNIQUE (tenant_id, repository_id, candidate_id, message_id),
    FOREIGN KEY (tenant_id, repository_id, candidate_id)
        REFERENCES forja.memory_candidates(tenant_id, repository_id, candidate_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, message_id)
        REFERENCES forja.messages(tenant_id, repository_id, message_id) ON DELETE RESTRICT
);

CREATE TABLE forja.memory_records (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    memory_id text NOT NULL CHECK (
        memory_id ~ '^memory_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    ),
    source_candidate_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('fact', 'preference', 'decision', 'lesson')),
    status text NOT NULL CHECK (status IN ('active', 'superseded', 'expired', 'tombstoned')),
    version integer NOT NULL CHECK (version > 0),
    content_artifact_id text NOT NULL,
    content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256)=32),
    authority_class text NOT NULL CHECK (authority_class IN ('human_approved', 'policy_approved')),
    promoted_by text NOT NULL CHECK (char_length(promoted_by) BETWEEN 1 AND 160),
    promotion_reason text NOT NULL CHECK (char_length(promotion_reason) BETWEEN 1 AND 2000),
    promoted_at timestamptz NOT NULL,
    expires_at timestamptz,
    superseded_by text,
    superseded_at timestamptz,
    expired_at timestamptz,
    tombstoned_at timestamptz,
    PRIMARY KEY (tenant_id, repository_id, memory_id),
    FOREIGN KEY (tenant_id, repository_id, source_candidate_id)
        REFERENCES forja.memory_candidates(tenant_id, repository_id, candidate_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, content_artifact_id, content_sha256)
        REFERENCES forja.artifacts(tenant_id, repository_id, artifact_id, content_sha256)
        ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, superseded_by)
        REFERENCES forja.memory_records(tenant_id, repository_id, memory_id) ON DELETE RESTRICT,
    CHECK (expires_at IS NULL OR expires_at > promoted_at),
    CHECK (superseded_at IS NULL OR superseded_at >= promoted_at),
    CHECK (expired_at IS NULL OR expired_at >= promoted_at),
    CHECK (tombstoned_at IS NULL OR tombstoned_at >= promoted_at),
    CHECK (superseded_by IS NULL OR superseded_by <> memory_id),
    CHECK (
        (status='active' AND superseded_by IS NULL AND superseded_at IS NULL
            AND expired_at IS NULL AND tombstoned_at IS NULL)
        OR (status='superseded' AND superseded_by IS NOT NULL AND superseded_at IS NOT NULL
            AND expired_at IS NULL AND tombstoned_at IS NULL)
        OR (status='expired' AND superseded_by IS NULL AND superseded_at IS NULL
            AND expired_at IS NOT NULL AND tombstoned_at IS NULL)
        OR (status='tombstoned' AND tombstoned_at IS NOT NULL)
    )
);

ALTER TABLE forja.memory_candidates
    ADD CONSTRAINT memory_candidates_promoted_memory_fkey
    FOREIGN KEY (tenant_id, repository_id, memory_id)
    REFERENCES forja.memory_records(tenant_id, repository_id, memory_id)
    ON DELETE RESTRICT
    DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX memory_records_active_idx
    ON forja.memory_records (tenant_id, repository_id, kind, promoted_at)
    WHERE status='active';

CREATE TABLE forja.memory_supersessions (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    memory_id text NOT NULL,
    superseded_memory_id text NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, repository_id, memory_id, superseded_memory_id),
    FOREIGN KEY (tenant_id, repository_id, memory_id)
        REFERENCES forja.memory_records(tenant_id, repository_id, memory_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, superseded_memory_id)
        REFERENCES forja.memory_records(tenant_id, repository_id, memory_id) ON DELETE RESTRICT,
    CHECK (memory_id <> superseded_memory_id)
);

CREATE FUNCTION forja.reject_knowledge_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'Forja knowledge evidence is append-only' USING ERRCODE='55000';
END
$$;

CREATE FUNCTION forja.enforce_artifact_activation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    exact_authority boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1
        FROM forja.artifact_operations AS operation
        JOIN forja.artifact_objects AS object
          ON object.tenant_id=operation.tenant_id
         AND object.repository_id=operation.repository_id
         AND object.content_sha256=operation.content_sha256
        WHERE operation.tenant_id=NEW.tenant_id
          AND operation.repository_id=NEW.repository_id
          AND operation.operation_id=NEW.operation_id
          AND operation.artifact_id=NEW.artifact_id
          AND operation.state='active'
          AND object.state='active'
          AND object.content_sha256=NEW.content_sha256
          AND operation.expected_size_bytes=NEW.size_bytes
          AND object.size_bytes=NEW.size_bytes
          AND operation.expected_media_type=NEW.media_type
          AND object.media_type=NEW.media_type
    ) INTO exact_authority;
    IF NOT exact_authority THEN
        RAISE EXCEPTION 'artifact activation lacks an exact active operation and object'
            USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END
$$;

CREATE CONSTRAINT TRIGGER zz_artifact_requires_active_object_at_commit
AFTER INSERT OR UPDATE ON forja.artifacts
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION forja.enforce_artifact_activation();

CREATE FUNCTION forja.enforce_artifact_bundle_totals()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    actual_count integer;
    actual_total bigint;
BEGIN
    SELECT count(*), COALESCE(sum(size_bytes), 0)
    INTO actual_count, actual_total
    FROM forja.artifact_bundle_entries
    WHERE tenant_id=NEW.tenant_id
      AND repository_id=NEW.repository_id
      AND manifest_id=NEW.manifest_id;
    IF actual_count <> NEW.entry_count OR actual_total <> NEW.total_size_bytes
       OR NOT EXISTS (
           SELECT 1
           FROM forja.artifact_bundle_entries
           WHERE tenant_id=NEW.tenant_id
             AND repository_id=NEW.repository_id
             AND manifest_id=NEW.manifest_id
           HAVING min(ordinal)=0 AND max(ordinal)=count(*)-1
       ) THEN
        RAISE EXCEPTION 'artifact bundle count or byte total does not match its entries'
            USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END
$$;

CREATE CONSTRAINT TRIGGER zz_artifact_bundle_totals_at_commit
AFTER INSERT ON forja.artifact_bundle_manifests
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION forja.enforce_artifact_bundle_totals();

CREATE FUNCTION forja.enforce_message_has_content()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM forja.message_parts AS part
        WHERE part.tenant_id=NEW.tenant_id
          AND part.repository_id=NEW.repository_id
          AND part.message_id=NEW.message_id
        HAVING count(*) BETWEEN 1 AND 64
           AND min(ordinal)=0
           AND max(ordinal)=count(*)-1
    ) THEN
        RAISE EXCEPTION 'message lacks a contiguous bounded content-part set'
            USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END
$$;

CREATE CONSTRAINT TRIGGER zz_message_has_content_at_commit
AFTER INSERT ON forja.messages
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION forja.enforce_message_has_content();

CREATE FUNCTION forja.enforce_memory_candidate_sources()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM forja.memory_candidate_sources AS source
        WHERE source.tenant_id=NEW.tenant_id
          AND source.repository_id=NEW.repository_id
          AND source.candidate_id=NEW.candidate_id
        HAVING count(*) BETWEEN 1 AND 128
           AND min(ordinal)=0
           AND max(ordinal)=count(*)-1
    ) THEN
        RAISE EXCEPTION 'memory candidate lacks contiguous canonical message sources'
            USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END
$$;

CREATE CONSTRAINT TRIGGER zz_memory_candidate_sources_at_commit
AFTER INSERT ON forja.memory_candidates
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION forja.enforce_memory_candidate_sources();

CREATE FUNCTION forja.enforce_memory_promotion()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    exact_candidate boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1
        FROM forja.memory_candidates AS candidate
        WHERE candidate.tenant_id=NEW.tenant_id
          AND candidate.repository_id=NEW.repository_id
          AND candidate.candidate_id=NEW.source_candidate_id
          AND candidate.status='promoted'
          AND candidate.memory_id=NEW.memory_id
          AND candidate.kind=NEW.kind
          AND candidate.proposed_artifact_id=NEW.content_artifact_id
          AND candidate.proposed_content_sha256=NEW.content_sha256
          AND candidate.resolved_by=NEW.promoted_by
          AND candidate.resolution_reason=NEW.promotion_reason
          AND candidate.resolved_at=NEW.promoted_at
          AND EXISTS (
              SELECT 1
              FROM forja.memory_candidate_sources AS source
              WHERE source.tenant_id=candidate.tenant_id
                AND source.repository_id=candidate.repository_id
                AND source.candidate_id=candidate.candidate_id
          )
    ) INTO exact_candidate;
    IF NOT exact_candidate THEN
        RAISE EXCEPTION 'memory promotion lacks an exact resolved candidate and source'
            USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END
$$;

CREATE CONSTRAINT TRIGGER zz_memory_requires_promoted_candidate_at_commit
AFTER INSERT ON forja.memory_records
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION forja.enforce_memory_promotion();

CREATE TRIGGER messages_are_append_only
BEFORE UPDATE OR DELETE ON forja.messages
FOR EACH ROW EXECUTE FUNCTION forja.reject_knowledge_immutable_mutation();

CREATE TRIGGER message_parts_are_append_only
BEFORE UPDATE OR DELETE ON forja.message_parts
FOR EACH ROW EXECUTE FUNCTION forja.reject_knowledge_immutable_mutation();

CREATE TRIGGER message_citations_are_append_only
BEFORE UPDATE OR DELETE ON forja.message_citations
FOR EACH ROW EXECUTE FUNCTION forja.reject_knowledge_immutable_mutation();

CREATE TRIGGER memory_candidate_sources_are_append_only
BEFORE UPDATE OR DELETE ON forja.memory_candidate_sources
FOR EACH ROW EXECUTE FUNCTION forja.reject_knowledge_immutable_mutation();

CREATE TRIGGER artifact_supersessions_are_append_only
BEFORE UPDATE OR DELETE ON forja.artifact_supersessions
FOR EACH ROW EXECUTE FUNCTION forja.reject_knowledge_immutable_mutation();

CREATE TRIGGER memory_supersessions_are_append_only
BEFORE UPDATE OR DELETE ON forja.memory_supersessions
FOR EACH ROW EXECUTE FUNCTION forja.reject_knowledge_immutable_mutation();

CREATE TRIGGER artifact_bundle_manifests_are_append_only
BEFORE UPDATE OR DELETE ON forja.artifact_bundle_manifests
FOR EACH ROW EXECUTE FUNCTION forja.reject_knowledge_immutable_mutation();

CREATE TRIGGER artifact_bundle_entries_are_append_only
BEFORE UPDATE OR DELETE ON forja.artifact_bundle_entries
FOR EACH ROW EXECUTE FUNCTION forja.reject_knowledge_immutable_mutation();

ALTER TABLE forja.events
    DROP CONSTRAINT events_aggregate_type_check,
    ADD CONSTRAINT events_aggregate_type_check CHECK (
        aggregate_type IN (
            'sprint', 'task', 'run', 'attempt', 'approval', 'decision', 'audit',
            'artifact', 'artifact_operation', 'artifact_manifest', 'conversation',
            'message', 'memory_candidate', 'memory', 'projection'
        )
    );
