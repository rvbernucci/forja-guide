ALTER TABLE forja.projection_dead_letters
    DROP CONSTRAINT projection_dead_letters_tenant_id_repository_id_outbox_id_fkey,
    ADD CONSTRAINT projection_dead_letters_outbox_id_fkey
    FOREIGN KEY (outbox_id)
    REFERENCES forja.outbox (outbox_id)
    ON DELETE RESTRICT;

ALTER TABLE forja.outbox
    DROP CONSTRAINT outbox_tenant_id_repository_id_outbox_id_key;

ALTER TABLE forja.attempts
    ALTER COLUMN created_at SET DEFAULT clock_timestamp(),
    ALTER COLUMN updated_at SET DEFAULT clock_timestamp();
