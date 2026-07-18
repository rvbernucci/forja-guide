LOCK TABLE
    forja.leases,
    forja.lease_sets,
    forja.lease_set_members,
    forja.delivery_publications
IN ACCESS EXCLUSIVE MODE;

ALTER TABLE forja.lease_sets
    ADD COLUMN authorized_ttl_us bigint;

UPDATE forja.lease_sets
SET authorized_ttl_us = CASE
    WHEN state='active' THEN
        (extract(epoch FROM (expires_at - updated_at)) * 1000000)::bigint
    ELSE 1000
END;

ALTER TABLE forja.lease_sets
    ALTER COLUMN authorized_ttl_us SET NOT NULL,
    ADD CONSTRAINT lease_sets_authorized_ttl_us_check CHECK (
        authorized_ttl_us BETWEEN 1000 AND 86400000000
    );
