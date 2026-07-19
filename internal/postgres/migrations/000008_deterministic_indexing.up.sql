CREATE TABLE forja.index_snapshots (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    snapshot_id text NOT NULL CHECK (snapshot_id ~ '^snapshot_[a-f0-9]{64}$'),
    source_commit text NOT NULL CHECK (source_commit ~ '^[a-f0-9]{40,64}$'),
    source_tree text NOT NULL CHECK (source_tree ~ '^[a-f0-9]{40,64}$'),
    configuration_sha256 bytea NOT NULL CHECK (octet_length(configuration_sha256)=32),
    adapter_set_sha256 bytea NOT NULL CHECK (octet_length(adapter_set_sha256)=32),
    adapters jsonb NOT NULL CHECK (jsonb_typeof(adapters)='array' AND jsonb_array_length(adapters) BETWEEN 1 AND 16),
    request_sha256 bytea NOT NULL CHECK (octet_length(request_sha256)=32),
    status text NOT NULL CHECK (status IN ('proposed', 'extracting', 'validated', 'active', 'failed', 'superseded', 'invalidated')),
    version integer NOT NULL CHECK (version > 0),
    file_count integer NOT NULL CHECK (file_count BETWEEN 0 AND 1000000),
    symbol_count integer NOT NULL CHECK (symbol_count BETWEEN 0 AND 10000000),
    relation_count integer NOT NULL CHECK (relation_count BETWEEN 0 AND 50000000),
    diagnostic_count integer NOT NULL CHECK (diagnostic_count BETWEEN 0 AND 10000000),
    artifact_id text,
    artifact_content_sha256 bytea CHECK (artifact_content_sha256 IS NULL OR octet_length(artifact_content_sha256)=32),
    created_by text NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 160),
    created_at timestamptz NOT NULL,
    validated_at timestamptz,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, repository_id, snapshot_id),
    UNIQUE (tenant_id, repository_id, source_commit, configuration_sha256, adapter_set_sha256),
    FOREIGN KEY (tenant_id, repository_id)
        REFERENCES forja.repositories(tenant_id, repository_id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, repository_id, artifact_id, artifact_content_sha256)
        REFERENCES forja.artifacts(tenant_id, repository_id, artifact_id, content_sha256) ON DELETE RESTRICT,
    CHECK (updated_at >= created_at),
    CHECK (validated_at IS NULL OR validated_at >= created_at),
    CHECK (
        (status IN ('proposed', 'extracting', 'failed') AND artifact_id IS NULL AND artifact_content_sha256 IS NULL AND validated_at IS NULL)
        OR (status IN ('validated', 'active', 'superseded', 'invalidated') AND artifact_id IS NOT NULL AND artifact_content_sha256 IS NOT NULL AND validated_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX index_snapshots_one_active_idx
    ON forja.index_snapshots (tenant_id, repository_id)
    WHERE status='active';
CREATE INDEX index_snapshots_commit_idx
    ON forja.index_snapshots (tenant_id, repository_id, source_commit, status);

CREATE FUNCTION forja.enforce_index_snapshot_authority()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    artifact_kind text;
    artifact_status text;
    artifact_tombstoned_at timestamptz;
BEGIN
    IF NEW.status IN ('validated', 'active', 'superseded', 'invalidated') THEN
        SELECT kind, status, tombstoned_at
        INTO artifact_kind, artifact_status, artifact_tombstoned_at
        FROM forja.artifacts
        WHERE tenant_id=NEW.tenant_id
          AND repository_id=NEW.repository_id
          AND artifact_id=NEW.artifact_id
          AND content_sha256=NEW.artifact_content_sha256
        FOR KEY SHARE;
        IF NOT FOUND
           OR artifact_kind <> 'index_snapshot'
           OR artifact_status NOT IN ('active', 'validated')
           OR artifact_tombstoned_at IS NOT NULL THEN
            RAISE EXCEPTION USING
                ERRCODE='23514',
                MESSAGE='validated index snapshot requires an active canonical index_snapshot artifact';
        END IF;
    END IF;
    RETURN NEW;
END
$$;

CREATE FUNCTION forja.enforce_index_snapshot_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.tenant_id <> OLD.tenant_id
       OR NEW.repository_id <> OLD.repository_id
       OR NEW.snapshot_id <> OLD.snapshot_id
       OR NEW.source_commit <> OLD.source_commit
       OR NEW.source_tree <> OLD.source_tree
       OR NEW.configuration_sha256 <> OLD.configuration_sha256
       OR NEW.adapter_set_sha256 <> OLD.adapter_set_sha256
       OR NEW.adapters <> OLD.adapters
       OR NEW.request_sha256 <> OLD.request_sha256
       OR NEW.file_count <> OLD.file_count
       OR NEW.symbol_count <> OLD.symbol_count
       OR NEW.relation_count <> OLD.relation_count
       OR NEW.diagnostic_count <> OLD.diagnostic_count
       OR NEW.artifact_id IS DISTINCT FROM OLD.artifact_id
       OR NEW.artifact_content_sha256 IS DISTINCT FROM OLD.artifact_content_sha256
       OR NEW.created_by <> OLD.created_by
       OR NEW.created_at <> OLD.created_at
       OR NEW.validated_at IS DISTINCT FROM OLD.validated_at
       OR NEW.version <> OLD.version + 1
       OR NEW.updated_at < OLD.updated_at
       OR NOT (
           (OLD.status='proposed' AND NEW.status IN ('extracting', 'failed'))
           OR (OLD.status='extracting' AND NEW.status IN ('validated', 'failed'))
           OR (OLD.status='validated' AND NEW.status IN ('active', 'failed'))
           OR (OLD.status='active' AND NEW.status IN ('superseded', 'invalidated'))
       ) THEN
        RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='invalid index snapshot mutation or transition';
    END IF;
    RETURN NEW;
END
$$;

CREATE FUNCTION forja.protect_index_snapshot_artifact()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF (NEW.status NOT IN ('active', 'validated') OR NEW.tombstoned_at IS NOT NULL)
       AND EXISTS (
           SELECT 1
           FROM forja.index_snapshots AS snapshot
           WHERE snapshot.tenant_id=OLD.tenant_id
             AND snapshot.repository_id=OLD.repository_id
             AND snapshot.artifact_id=OLD.artifact_id
             AND snapshot.artifact_content_sha256=OLD.content_sha256
             AND snapshot.status IN ('validated', 'active')
       ) THEN
        RAISE EXCEPTION USING
            ERRCODE='55000',
            MESSAGE='artifact is authoritative evidence for a live index snapshot';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER index_snapshots_require_canonical_artifact
    BEFORE INSERT OR UPDATE OF artifact_id, artifact_content_sha256, status
    ON forja.index_snapshots
    FOR EACH ROW EXECUTE FUNCTION forja.enforce_index_snapshot_authority();
CREATE TRIGGER index_snapshot_transitions_are_guarded
    BEFORE UPDATE ON forja.index_snapshots
    FOR EACH ROW EXECUTE FUNCTION forja.enforce_index_snapshot_transition();
CREATE TRIGGER live_index_snapshots_protect_artifacts
    BEFORE UPDATE OF status, tombstoned_at ON forja.artifacts
    FOR EACH ROW EXECUTE FUNCTION forja.protect_index_snapshot_artifact();

CREATE TABLE forja.index_files (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    snapshot_id text NOT NULL,
    file_id text NOT NULL CHECK (file_id ~ '^file_[a-f0-9]{64}$'),
    path text NOT NULL CHECK (char_length(path) BETWEEN 1 AND 4096),
    git_blob_id text NOT NULL CHECK (git_blob_id ~ '^[a-f0-9]{40,64}$'),
    source_sha256 bytea NOT NULL CHECK (octet_length(source_sha256)=32),
    size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 0 AND 16777216),
    language text NOT NULL CHECK (language IN ('go', 'typescript', 'javascript', 'python', 'json', 'yaml', 'markdown', 'other')),
    generated boolean NOT NULL,
    diagnostics jsonb NOT NULL CHECK (jsonb_typeof(diagnostics)='array' AND jsonb_array_length(diagnostics) <= 4096),
    PRIMARY KEY (tenant_id, repository_id, snapshot_id, file_id),
    UNIQUE (tenant_id, repository_id, snapshot_id, path),
    FOREIGN KEY (tenant_id, repository_id, snapshot_id)
        REFERENCES forja.index_snapshots(tenant_id, repository_id, snapshot_id) ON DELETE RESTRICT
);

CREATE INDEX index_files_source_idx
    ON forja.index_files (tenant_id, repository_id, source_sha256);

CREATE TABLE forja.index_symbols (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    snapshot_id text NOT NULL,
    symbol_id text NOT NULL CHECK (symbol_id ~ '^symbol_[a-f0-9]{64}$'),
    file_id text NOT NULL,
    language text NOT NULL CHECK (language IN ('go', 'typescript', 'javascript', 'python')),
    kind text NOT NULL,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 512),
    qualified_name text NOT NULL CHECK (char_length(qualified_name) BETWEEN 1 AND 2048),
    signature text NOT NULL CHECK (char_length(signature) <= 8192),
    start_line integer NOT NULL CHECK (start_line > 0),
    start_column integer NOT NULL CHECK (start_column > 0),
    start_offset integer NOT NULL CHECK (start_offset >= 0),
    end_line integer NOT NULL CHECK (end_line >= start_line),
    end_column integer NOT NULL CHECK (end_column > 0),
    end_offset integer NOT NULL CHECK (end_offset >= start_offset),
    exported boolean NOT NULL,
    is_test boolean NOT NULL,
    is_route boolean NOT NULL,
    is_schema boolean NOT NULL,
    documentation_sha256 bytea CHECK (documentation_sha256 IS NULL OR octet_length(documentation_sha256)=32),
    PRIMARY KEY (tenant_id, repository_id, snapshot_id, symbol_id),
    FOREIGN KEY (tenant_id, repository_id, snapshot_id, file_id)
        REFERENCES forja.index_files(tenant_id, repository_id, snapshot_id, file_id) ON DELETE RESTRICT
);

CREATE INDEX index_symbols_lookup_idx
    ON forja.index_symbols (tenant_id, repository_id, snapshot_id, qualified_name, kind);

CREATE TABLE forja.index_relations (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    snapshot_id text NOT NULL,
    relation_id text NOT NULL CHECK (relation_id ~ '^relation_[a-f0-9]{64}$'),
    source_entity_id text NOT NULL CHECK (source_entity_id ~ '^(file|symbol)_[a-f0-9]{64}$'),
    kind text NOT NULL,
    resolution text NOT NULL CHECK (resolution IN ('resolved', 'unresolved')),
    target_entity_id text CHECK (target_entity_id IS NULL OR target_entity_id ~ '^(file|symbol|external)_[a-f0-9]{64}$'),
    unresolved_name text CHECK (unresolved_name IS NULL OR char_length(unresolved_name) BETWEEN 1 AND 2048),
    evidence_class text NOT NULL CHECK (evidence_class IN ('candidate_static', 'confirmed_static', 'confirmed_behavioral', 'runtime_observed')),
    source_file_id text NOT NULL,
    start_line integer NOT NULL CHECK (start_line > 0),
    start_column integer NOT NULL CHECK (start_column > 0),
    start_offset integer NOT NULL CHECK (start_offset >= 0),
    end_line integer NOT NULL CHECK (end_line >= start_line),
    end_column integer NOT NULL CHECK (end_column > 0),
    end_offset integer NOT NULL CHECK (end_offset >= start_offset),
    evidence_sha256 bytea NOT NULL CHECK (octet_length(evidence_sha256)=32),
    adapter jsonb NOT NULL CHECK (jsonb_typeof(adapter)='object'),
    PRIMARY KEY (tenant_id, repository_id, snapshot_id, relation_id),
    FOREIGN KEY (tenant_id, repository_id, snapshot_id, source_file_id)
        REFERENCES forja.index_files(tenant_id, repository_id, snapshot_id, file_id) ON DELETE RESTRICT,
    CHECK (
        (resolution='resolved' AND target_entity_id IS NOT NULL AND unresolved_name IS NULL)
        OR (resolution='unresolved' AND target_entity_id IS NULL AND unresolved_name IS NOT NULL)
    )
);

CREATE INDEX index_relations_source_idx
    ON forja.index_relations (tenant_id, repository_id, snapshot_id, source_entity_id, kind);
CREATE INDEX index_relations_target_idx
    ON forja.index_relations (tenant_id, repository_id, snapshot_id, target_entity_id, kind)
    WHERE target_entity_id IS NOT NULL;

CREATE TABLE forja.index_adapter_runs (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    snapshot_id text NOT NULL,
    adapter_name text NOT NULL CHECK (adapter_name IN ('go', 'typescript', 'python')),
    adapter_version text NOT NULL CHECK (char_length(adapter_version) BETWEEN 1 AND 80),
    configuration_sha256 bytea NOT NULL CHECK (octet_length(configuration_sha256)=32),
    capability_sha256 bytea NOT NULL CHECK (octet_length(capability_sha256)=32),
    status text NOT NULL CHECK (status IN ('passed', 'failed')),
    diagnostic_count integer NOT NULL CHECK (diagnostic_count >= 0),
    PRIMARY KEY (tenant_id, repository_id, snapshot_id, adapter_name),
    FOREIGN KEY (tenant_id, repository_id, snapshot_id)
        REFERENCES forja.index_snapshots(tenant_id, repository_id, snapshot_id) ON DELETE RESTRICT
);

CREATE TABLE forja.index_deltas (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    snapshot_id text NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    change_kind text NOT NULL CHECK (change_kind IN ('added', 'modified', 'deleted', 'renamed', 'reused')),
    entity_kind text NOT NULL CHECK (entity_kind IN ('file', 'symbol', 'relation')),
    entity_id text NOT NULL,
    previous_entity_id text,
    PRIMARY KEY (tenant_id, repository_id, snapshot_id, ordinal),
    FOREIGN KEY (tenant_id, repository_id, snapshot_id)
        REFERENCES forja.index_snapshots(tenant_id, repository_id, snapshot_id) ON DELETE RESTRICT
);

CREATE TABLE forja.index_invalidations (
    tenant_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    snapshot_id text NOT NULL,
    entity_id text NOT NULL,
    reason text NOT NULL CHECK (reason IN ('source_changed', 'dependency_changed', 'adapter_changed', 'configuration_changed', 'deleted')),
    source_sha256 bytea CHECK (source_sha256 IS NULL OR octet_length(source_sha256)=32),
    PRIMARY KEY (tenant_id, repository_id, snapshot_id, entity_id, reason),
    FOREIGN KEY (tenant_id, repository_id, snapshot_id)
        REFERENCES forja.index_snapshots(tenant_id, repository_id, snapshot_id) ON DELETE RESTRICT
);

CREATE FUNCTION forja.reject_index_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION USING ERRCODE='55000', MESSAGE='canonical index evidence is immutable';
END
$$;

CREATE TRIGGER index_files_are_append_only
    BEFORE UPDATE OR DELETE ON forja.index_files
    FOR EACH ROW EXECUTE FUNCTION forja.reject_index_immutable_mutation();
CREATE TRIGGER index_symbols_are_append_only
    BEFORE UPDATE OR DELETE ON forja.index_symbols
    FOR EACH ROW EXECUTE FUNCTION forja.reject_index_immutable_mutation();
CREATE TRIGGER index_relations_are_append_only
    BEFORE UPDATE OR DELETE ON forja.index_relations
    FOR EACH ROW EXECUTE FUNCTION forja.reject_index_immutable_mutation();
CREATE TRIGGER index_adapter_runs_are_append_only
    BEFORE UPDATE OR DELETE ON forja.index_adapter_runs
    FOR EACH ROW EXECUTE FUNCTION forja.reject_index_immutable_mutation();
CREATE TRIGGER index_deltas_are_append_only
    BEFORE UPDATE OR DELETE ON forja.index_deltas
    FOR EACH ROW EXECUTE FUNCTION forja.reject_index_immutable_mutation();
CREATE TRIGGER index_invalidations_are_append_only
    BEFORE UPDATE OR DELETE ON forja.index_invalidations
    FOR EACH ROW EXECUTE FUNCTION forja.reject_index_immutable_mutation();

ALTER TABLE forja.events
    DROP CONSTRAINT events_aggregate_type_check,
    ADD CONSTRAINT events_aggregate_type_check CHECK (
        aggregate_type IN (
            'sprint', 'task', 'run', 'attempt', 'approval', 'decision',
            'audit', 'artifact', 'artifact_operation', 'artifact_manifest',
            'conversation', 'message', 'memory_candidate', 'memory',
            'index_snapshot', 'projection'
        )
    );
