UPDATE forja.attempts
SET updated_at=created_at
WHERE version=1
  AND started_at IS NULL
  AND finished_at IS NULL
  AND updated_at IS DISTINCT FROM created_at;

ALTER TABLE forja.attempts
    ALTER COLUMN created_at SET DEFAULT statement_timestamp(),
    ALTER COLUMN updated_at SET DEFAULT statement_timestamp();

ALTER TABLE forja.outbox
    ADD CONSTRAINT outbox_tenant_id_repository_id_outbox_id_key
    UNIQUE (tenant_id, repository_id, outbox_id);

ALTER TABLE forja.projection_dead_letters
    DROP CONSTRAINT projection_dead_letters_outbox_id_fkey,
    ADD CONSTRAINT projection_dead_letters_tenant_id_repository_id_outbox_id_fkey
    FOREIGN KEY (tenant_id, repository_id, outbox_id)
    REFERENCES forja.outbox (tenant_id, repository_id, outbox_id)
    ON DELETE RESTRICT;
