CREATE VIEW forja.alpha_v_source_coverage AS
SELECT
    so.tenant_id,
    so.repository_id,
    so.source_object_id,
    so.source_system_id,
    ss.source_key,
    ss.display_name AS source_name,
    ss.data_family,
    so.ingestion_run_id,
    ir.state AS ingestion_state,
    ir.code_version,
    ir.object_count,
    ir.row_count,
    so.object_key,
    so.content_sha256,
    so.size_bytes,
    so.media_type,
    so.source_uri_fingerprint,
    so.published_at,
    so.available_at,
    so.ingested_at,
    so.lifecycle,
    so.metadata
FROM forja.alpha_source_objects AS so
JOIN forja.alpha_source_systems AS ss
  ON ss.tenant_id = so.tenant_id
 AND ss.repository_id = so.repository_id
 AND ss.source_system_id = so.source_system_id
JOIN forja.alpha_ingestion_runs AS ir
  ON ir.tenant_id = so.tenant_id
 AND ir.repository_id = so.repository_id
 AND ir.ingestion_run_id = so.ingestion_run_id
WHERE so.lifecycle <> 'quarantined';

COMMENT ON VIEW forja.alpha_v_source_coverage IS
    'Read-only Alpha source coverage inventory for UI, audit, and agent context. Research tools must still enforce as_of and permission policies.';

