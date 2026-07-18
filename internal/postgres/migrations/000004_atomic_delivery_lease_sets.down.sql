-- Match the runtime writer order: writers cross leases before reading either
-- lease-set relation. Acquiring these locks in the opposite order can deadlock
-- with a writer that is waiting on lease_sets while holding the leases barrier.
LOCK TABLE
	forja.leases,
	forja.lease_sets,
	forja.lease_set_members
IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM forja.lease_sets AS lease_set
        WHERE lease_set.state='active'
          AND (
              lease_set.expires_at > clock_timestamp()
              OR EXISTS (
                  SELECT 1
                  FROM forja.lease_set_members AS member
                  JOIN forja.leases AS lease
                    ON lease.tenant_id=member.tenant_id
                   AND lease.repository_id=member.repository_id
                   AND lease.resource_type=member.resource_type
                   AND lease.resource_id=member.resource_id
                   AND lease.fencing_token=member.fencing_token
                  WHERE member.tenant_id=lease_set.tenant_id
                    AND member.repository_id=lease_set.repository_id
                    AND member.lease_set_id=lease_set.lease_set_id
                    AND lease.expires_at > clock_timestamp()
              )
          )
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE='55000',
            MESSAGE='migration 004 rollback requires every lease set to expire or release';
    END IF;
END
$$;

DROP TABLE forja.lease_set_members;
DROP TABLE forja.lease_sets;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM forja.leases
        WHERE resource_type='artifact'
          AND expires_at > clock_timestamp()
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE='55000',
            MESSAGE='migration 004 rollback requires every artifact lease to expire';
    END IF;
END
$$;

DELETE FROM forja.leases
WHERE resource_type='artifact';

ALTER TABLE forja.leases
    DROP CONSTRAINT leases_resource_type_check;

ALTER TABLE forja.leases
    ADD CONSTRAINT leases_resource_type_check CHECK (
        resource_type IN ('worker', 'scheduler', 'file', 'worktree')
    );
