LOCK TABLE
    forja.leases,
    forja.lease_sets,
    forja.lease_set_members,
    forja.delivery_publications
IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM forja.lease_sets WHERE state='active') THEN
        RAISE EXCEPTION USING
            ERRCODE='55000',
            MESSAGE='migration 006 requires every lease set to release';
    END IF;
END
$$;

ALTER TABLE forja.lease_sets
    ADD COLUMN authorized_ttl_us bigint;

UPDATE forja.lease_sets
SET authorized_ttl_us = 1000;

ALTER TABLE forja.lease_sets
    ALTER COLUMN authorized_ttl_us SET NOT NULL,
    ADD CONSTRAINT lease_sets_authorized_ttl_us_check CHECK (
        authorized_ttl_us BETWEEN 1000 AND 86400000000
    );
