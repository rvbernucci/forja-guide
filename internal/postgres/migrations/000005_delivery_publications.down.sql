LOCK TABLE
    forja.leases,
    forja.lease_sets,
    forja.lease_set_members,
    forja.delivery_publications
IN ACCESS EXCLUSIVE MODE NOWAIT;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM forja.delivery_publications) THEN
        RAISE EXCEPTION USING
            ERRCODE='55000',
            MESSAGE='migration 005 rollback requires an empty delivery publication journal';
    END IF;
END
$$;

DROP TABLE forja.delivery_publications;
