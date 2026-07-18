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
            MESSAGE='migration 006 rollback requires every lease set to release';
    END IF;
END
$$;

ALTER TABLE forja.lease_sets
    DROP COLUMN authorized_ttl_us;
