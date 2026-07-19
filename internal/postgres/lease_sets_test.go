package postgres

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/persistence"
)

func TestLeaseSetValidationRejectsAmbiguousAuthority(t *testing.T) {
	t.Parallel()
	store := &Store{tenantID: DefaultTenantID, repositoryID: DefaultRepositoryID}
	fileKey := deliveryLeaseKey("file", "internal")
	for name, keys := range map[string][]persistence.LeaseKey{
		"empty":            {},
		"duplicate":        {fileKey, fileKey},
		"worker lease":     {deliveryLeaseKey("worker", "attempt")},
		"missing worktree": {deliveryLeaseKey("file", "internal")},
		"multiple worktrees": {
			deliveryLeaseKey("worktree", "delivery_test"),
			deliveryLeaseKey("worktree", "another-delivery"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := store.validateLeaseSetInput(
				"delivery_test", keys, "owner", time.Minute,
			); err == nil {
				t.Fatal("ambiguous lease set authority passed validation")
			}
		})
	}
}

func TestLeaseMemberDigestIsOrderIndependent(t *testing.T) {
	t.Parallel()
	store := &Store{tenantID: DefaultTenantID, repositoryID: DefaultRepositoryID}
	left, leftDigest, err := store.validateLeaseSetInput(
		"attempt_order", []persistence.LeaseKey{
			deliveryLeaseKey("worktree", "delivery_order"),
			deliveryLeaseKey("artifact", "evidence"),
		}, "owner", time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	right, rightDigest, err := store.validateLeaseSetInput(
		"attempt_order", []persistence.LeaseKey{
			deliveryLeaseKey("artifact", "evidence"),
			deliveryLeaseKey("worktree", "delivery_order"),
		}, "owner", time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(leftDigest) != string(rightDigest) || len(left) != len(right) {
		t.Fatal("canonical lease-set digest depends on caller ordering")
	}
}

func TestAtomicLeaseSetLifecycleAndNoExpansion(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	keys := []persistence.LeaseKey{
		deliveryLeaseKey("worktree", "delivery_lifecycle"),
		deliveryLeaseKey("file", "internal/delivery"),
		deliveryLeaseKey("artifact", "evidence"),
	}
	set, err := store.AcquireLeaseSet(
		t.Context(), "attempt_lifecycle_1", keys, "author-a", time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire lease set: %v", err)
	}
	if len(set.Leases) != 4 {
		t.Fatalf("lease count = %d, want 4 including the file ancestor", len(set.Leases))
	}
	member := set.Leases[0]
	if _, err := store.RenewLease(
		t.Context(), member.LeaseKey, member.OwnerID, member.FencingToken, time.Minute,
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("individual set-member renewal error = %v, want conflict", err)
	}
	if err := store.ReleaseLease(
		t.Context(), member.LeaseKey, member.OwnerID, member.FencingToken,
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("individual set-member release error = %v, want conflict", err)
	}
	replayed, err := store.AcquireLeaseSet(
		t.Context(), "attempt_lifecycle_1", append([]persistence.LeaseKey(nil), keys...),
		"author-a", time.Minute,
	)
	if err != nil {
		t.Fatalf("replay lease set: %v", err)
	}
	for index := range set.Leases {
		if replayed.Leases[index].FencingToken != set.Leases[index].FencingToken {
			t.Fatal("lease-set replay changed a fencing token")
		}
	}
	if _, err := store.AcquireLeaseSet(
		t.Context(), "attempt_lifecycle_1", append([]persistence.LeaseKey(nil), keys...),
		"author-a", 2*time.Minute,
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("lease-set TTL replay error = %v, want conflict", err)
	}
	expanded := append(append([]persistence.LeaseKey(nil), keys...), deliveryLeaseKey("file", "cmd"))
	if _, err := store.AcquireLeaseSet(
		t.Context(), "attempt_lifecycle_1", expanded, "author-a", time.Minute,
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("lease-set expansion error = %v, want conflict", err)
	}
	forged := set
	forged.Leases = append([]persistence.Lease(nil), set.Leases...)
	forged.Leases[0].OwnerID = "forged-owner"
	forged.Leases[0].AcquiredAt = time.Time{}
	if _, err := store.RenewLeaseSet(
		t.Context(), forged, 2*time.Minute,
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("lease-set TTL expansion error = %v, want conflict", err)
	}
	renewed, err := store.RenewLeaseSet(t.Context(), forged, time.Minute)
	if err != nil {
		t.Fatalf("renew lease set: %v", err)
	}
	if !renewed.ExpiresAt.After(set.ExpiresAt) {
		t.Fatal("lease-set renewal did not extend expiration")
	}
	if renewed.Leases[0].OwnerID != set.OwnerID || renewed.Leases[0].AcquiredAt.IsZero() {
		t.Fatal("lease-set renewal returned caller-forged metadata")
	}
	stale := renewed
	stale.Leases = append([]persistence.Lease(nil), renewed.Leases...)
	stale.Leases[0].FencingToken++
	if _, err := store.RenewLeaseSet(t.Context(), stale, time.Minute); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("stale renewal error = %v, want conflict", err)
	}
	if err := store.ReleaseLeaseSet(t.Context(), renewed); err != nil {
		t.Fatalf("release lease set: %v", err)
	}
	if err := store.ReleaseLeaseSet(t.Context(), renewed); err != nil {
		t.Fatalf("replay exact lease set release: %v", err)
	}
	if _, err := store.AcquireLeaseSet(
		t.Context(), "attempt_lifecycle_1", keys, "author-a", time.Minute,
	); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("retired set ID reuse error = %v, want conflict", err)
	}
	retryKeys := []persistence.LeaseKey{
		deliveryLeaseKey("worktree", "delivery_lifecycle"),
		deliveryLeaseKey("file", "internal/delivery"),
		deliveryLeaseKey("artifact", "evidence"),
	}
	next, err := store.AcquireLeaseSet(
		t.Context(), "attempt_lifecycle_2", retryKeys, "author-a", time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire retry lease set: %v", err)
	}
	previousTokens := make(map[string]int64, len(renewed.Leases))
	for _, lease := range renewed.Leases {
		previousTokens[lease.ResourceType+"\x00"+lease.ResourceID] = lease.FencingToken
	}
	for _, lease := range next.Leases {
		if previous, shared := previousTokens[lease.ResourceType+"\x00"+lease.ResourceID]; shared && lease.FencingToken <= previous {
			t.Fatal("retry did not advance a shared resource fencing token")
		}
	}
}

func TestLeaseSetAllowsSiblingFileAndArtifactScopes(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	set, err := store.AcquireLeaseSet(
		t.Context(), "attempt_siblings", []persistence.LeaseKey{
			deliveryLeaseKey("artifact", "internal/evidence"),
			deliveryLeaseKey("file", "internal/code"),
			deliveryLeaseKey("worktree", "delivery_siblings"),
		}, "author-a", time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire sibling scopes: %v", err)
	}
	if len(set.Leases) != 5 {
		t.Fatalf("lease count = %d, want two sibling scopes, two ancestor fences, and one worktree", len(set.Leases))
	}
	if err := store.ReleaseLeaseSet(t.Context(), set); err != nil {
		t.Fatalf("release sibling scopes: %v", err)
	}
}

func TestMigrationSixRequiresEveryLegacyLeaseSetToDrain(t *testing.T) {
	pool := migratedPool(t)
	rollbackToMigrationVersion(t, pool, 4)
	leaseSetID := "attempt_upgrade_drain"
	seedMigrationFourLeaseSet(t, pool, leaseSetID, false)

	if err := Migrate(t.Context(), pool); err == nil {
		t.Fatal("migration 006 inferred authority for an active legacy lease set")
	}
	var appliedVersion int
	if err := pool.QueryRow(t.Context(), `
		SELECT max(version) FROM forja.schema_migrations`,
	).Scan(&appliedVersion); err != nil {
		t.Fatal(err)
	}
	if appliedVersion != 4 {
		t.Fatalf("failed migration committed version %d, want 4", appliedVersion)
	}

	releaseMigrationFourLeaseSet(t, pool, leaseSetID)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatalf("migrate after legacy lease-set drain: %v", err)
	}
	var authorizedTTLUS int64
	if err := pool.QueryRow(t.Context(), `
		SELECT authorized_ttl_us FROM forja.lease_sets
		WHERE tenant_id=$1 AND repository_id=$2 AND lease_set_id=$3`,
		DefaultTenantID, DefaultRepositoryID, leaseSetID,
	).Scan(&authorizedTTLUS); err != nil {
		t.Fatal(err)
	}
	if authorizedTTLUS != 1000 {
		t.Fatalf("released legacy lease-set TTL = %d, want non-renewable sentinel", authorizedTTLUS)
	}
}

func TestMigrationSixFailsFastBehindLeaseSetReader(t *testing.T) {
	pool := migratedPool(t)
	rollbackToMigrationVersion(t, pool, 6)
	if err := RollbackLast(t.Context(), pool); err != nil {
		t.Fatalf("prepare migration 006 retry: %v", err)
	}
	blocker, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	blockerOpen := true
	defer func() {
		if blockerOpen {
			_ = blocker.Rollback(t.Context())
		}
	}()
	if _, err := blocker.Exec(
		t.Context(), "LOCK TABLE forja.lease_sets IN ACCESS SHARE MODE",
	); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	migrationErr := Migrate(t.Context(), pool)
	var databaseErr *pgconn.PgError
	if !errors.As(migrationErr, &databaseErr) || databaseErr.Code != "55P03" {
		t.Fatalf("contended migration error = %v, want lock_not_available", migrationErr)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("contended migration failed after %s, want fail-fast", elapsed)
	}
	if err := blocker.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	blockerOpen = false
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatalf("migration 006 retry after reader drained: %v", err)
	}
}

func TestMigrationFiveRollbackFailsFastBehindPublicationReader(t *testing.T) {
	pool := migratedPool(t)
	rollbackToMigrationVersion(t, pool, 6)
	if err := RollbackLast(t.Context(), pool); err != nil {
		t.Fatalf("rollback migration 006: %v", err)
	}
	blocker, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(t.Context()) }()
	if _, err := blocker.Exec(
		t.Context(), "LOCK TABLE forja.delivery_publications IN ACCESS SHARE MODE",
	); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	rollbackErr := RollbackLast(t.Context(), pool)
	var databaseErr *pgconn.PgError
	if !errors.As(rollbackErr, &databaseErr) || databaseErr.Code != "55P03" {
		t.Fatalf("contended rollback error = %v, want lock_not_available", rollbackErr)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("contended rollback failed after %s, want fail-fast", elapsed)
	}
}

func TestMigrationFourRollbackRequiresExpiredArtifactLeases(t *testing.T) {
	pool := migratedPool(t)
	rollbackToMigrationVersion(t, pool, 4)
	leaseSetID := "delivery_rollback"
	seedMigrationFourLeaseSet(t, pool, leaseSetID, true)
	if err := RollbackLast(t.Context(), pool); err == nil {
		t.Fatal("migration rollback accepted an active file/worktree lease set")
	}
	releaseMigrationFourLeaseSet(t, pool, leaseSetID)
	if err := RollbackLast(t.Context(), pool); err != nil {
		t.Fatalf("rollback after artifact lease expiration: %v", err)
	}
	var artifactRows int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*) FROM forja.leases WHERE resource_type='artifact'`,
	).Scan(&artifactRows); err != nil {
		t.Fatal(err)
	}
	if artifactRows != 0 {
		t.Fatal("rollback retained expired artifact lease rows")
	}
}

func TestMigrationFourRollbackFollowsLeaseWriterLockOrder(t *testing.T) {
	pool := migratedPool(t)
	rollbackToMigrationVersion(t, pool, 4)
	leaseSetID := "attempt_rollback_order"
	seedMigrationFourLeaseSet(t, pool, leaseSetID, false)

	writer, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	writerOpen := true
	defer func() {
		if writerOpen {
			_ = writer.Rollback(t.Context())
		}
	}()
	if _, err := writer.Exec(
		t.Context(),
		"LOCK TABLE forja.leases IN ACCESS SHARE MODE",
	); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	rollbackErr := RollbackLast(t.Context(), pool)
	var databaseErr *pgconn.PgError
	if !errors.As(rollbackErr, &databaseErr) || databaseErr.Code != "55P03" {
		t.Fatalf("contended rollback error = %v, want lock_not_available", rollbackErr)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("contended rollback failed after %s, want fail-fast", elapsed)
	}

	// The fail-fast writer barrier does not retain partial relation locks, so
	// the active writer can finish without deadlocking behind the rollback.
	if _, err := writer.Exec(t.Context(), `
		SELECT 1 FROM forja.lease_sets
		WHERE tenant_id=$1 AND repository_id=$2 AND lease_set_id=$3
		FOR UPDATE`, DefaultTenantID, DefaultRepositoryID, leaseSetID); err != nil {
		t.Fatalf("writer deadlocked behind rollback lease-set locks: %v", err)
	}
	if err := writer.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	writerOpen = false
	rollbackErr = RollbackLast(t.Context(), pool)
	if !errors.As(rollbackErr, &databaseErr) || databaseErr.Code != "55000" {
		t.Fatalf("rollback result = %v, want live-set safety refusal without deadlock", rollbackErr)
	}
	releaseMigrationFourLeaseSet(t, pool, leaseSetID)
	if err := RollbackLast(t.Context(), pool); err != nil {
		t.Fatalf("rollback after ordered writer drain: %v", err)
	}
}

func seedMigrationFourLeaseSet(
	t *testing.T,
	pool *pgxpool.Pool,
	leaseSetID string,
	withArtifact bool,
) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.lease_sets (
			tenant_id, repository_id, lease_set_id, owner_id, member_digest,
			state, acquired_at, expires_at, updated_at
		) VALUES (
			$1, $2, $3, 'delivery-writer', decode(repeat('ab', 32), 'hex'),
			'active', clock_timestamp(), clock_timestamp() + interval '1 minute',
			clock_timestamp()
		)`, DefaultTenantID, DefaultRepositoryID, leaseSetID); err != nil {
		t.Fatalf("seed migration 004 lease set: %v", err)
	}
	if !withArtifact {
		return
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.leases (
			tenant_id, repository_id, resource_type, resource_id, owner_id,
			fencing_token, acquired_at, expires_at, updated_at
		) VALUES (
			$1, $2, 'artifact', 'evidence/rollback', 'delivery-writer',
			1, clock_timestamp(), clock_timestamp() + interval '1 minute',
			clock_timestamp()
		)`, DefaultTenantID, DefaultRepositoryID); err != nil {
		t.Fatalf("seed migration 004 artifact lease: %v", err)
	}
}

func releaseMigrationFourLeaseSet(
	t *testing.T,
	pool *pgxpool.Pool,
	leaseSetID string,
) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `
		UPDATE forja.lease_sets
		SET state='released', expires_at=acquired_at, updated_at=clock_timestamp()
		WHERE tenant_id=$1 AND repository_id=$2 AND lease_set_id=$3`,
		DefaultTenantID, DefaultRepositoryID, leaseSetID,
	); err != nil {
		t.Fatalf("release migration 004 lease set: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		UPDATE forja.leases
		SET expires_at=acquired_at, updated_at=clock_timestamp()
		WHERE tenant_id=$1 AND repository_id=$2
		  AND resource_type='artifact' AND owner_id='delivery-writer'`,
		DefaultTenantID, DefaultRepositoryID,
	); err != nil {
		t.Fatalf("expire migration 004 artifact lease: %v", err)
	}
}

func TestLegacyStandaloneDeliveryLeaseCanBeCleanedUpAfterUpgrade(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	key := deliveryLeaseKey("file", "legacy/path")
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO forja.leases (
			tenant_id, repository_id, resource_type, resource_id, owner_id,
			fencing_token, acquired_at, expires_at, updated_at
		) VALUES (
			$1, $2, $3, $4, 'legacy-owner', 1, clock_timestamp(),
			clock_timestamp() + interval '1 hour', clock_timestamp()
		)`,
		key.TenantID, key.RepositoryID, key.ResourceType, key.ResourceID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireLease(
		t.Context(), key, "new-owner", time.Minute,
	); !isFaultCode(err, fault.CodeInvalidArgument) {
		t.Fatalf("new standalone delivery grant error = %v, want invalid argument", err)
	}
	if err := store.ReleaseLease(t.Context(), key, "legacy-owner", 1); err != nil {
		t.Fatalf("clean up legacy standalone delivery lease: %v", err)
	}
}

func TestLeaseSetsRejectAncestorDescendantAndCrossKindWriters(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	first, err := store.AcquireLeaseSet(
		t.Context(), "delivery_parent", []persistence.LeaseKey{
			deliveryLeaseKey("file", "internal"),
			deliveryLeaseKey("worktree", "delivery_parent"),
		}, "author-a", time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	for name, resourceType := range map[string]string{
		"descendant file": "file",
		"artifact alias":  "artifact",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := store.AcquireLeaseSet(
				t.Context(), "delivery_"+resourceType, []persistence.LeaseKey{
					deliveryLeaseKey(resourceType, "internal/delivery"),
					deliveryLeaseKey("worktree", "delivery_"+resourceType),
				}, "author-b", time.Minute,
			)
			if !isFaultCode(err, fault.CodeConflict) {
				t.Fatalf("hierarchical overlap error = %v, want conflict", err)
			}
		})
	}
	if err := store.ReleaseLeaseSet(t.Context(), first); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseSetConflictRollsBackEveryPartialGrant(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	blocking, err := store.AcquireLeaseSet(
		t.Context(), "delivery_blocker", []persistence.LeaseKey{
			deliveryLeaseKey("file", "internal"),
			deliveryLeaseKey("worktree", "delivery_blocker"),
		}, "author-a", time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AcquireLeaseSet(
		t.Context(), "delivery_conflict", []persistence.LeaseKey{
			deliveryLeaseKey("artifact", "evidence/conflict"),
			deliveryLeaseKey("file", "internal"),
			deliveryLeaseKey("worktree", "delivery_conflict"),
		}, "author-b", time.Minute,
	)
	if !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("overlap error = %v, want conflict", err)
	}
	var partialRows int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*) FROM forja.leases
		WHERE tenant_id=$1 AND repository_id=$2
		  AND resource_type='artifact' AND resource_id='evidence/conflict'`,
		DefaultTenantID, DefaultRepositoryID,
	).Scan(&partialRows); err != nil {
		t.Fatal(err)
	}
	if partialRows != 0 {
		t.Fatal("conflicting lease set left a partial artifact grant")
	}
	if err := store.ReleaseLeaseSet(t.Context(), blocking); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentOverlappingLeaseSetsHaveOneWinner(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, contender := range []string{"alpha", "beta"} {
		contender := contender
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := store.AcquireLeaseSet(
				t.Context(), "delivery_"+contender,
				[]persistence.LeaseKey{
					deliveryLeaseKey("file", "internal/shared"),
					deliveryLeaseKey("worktree", "delivery_"+contender),
				}, "author-"+contender, time.Minute,
			)
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case isFaultCode(err, fault.CodeConflict):
			conflicts++
		default:
			t.Fatalf("unexpected contender error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want one of each", successes, conflicts)
	}
}

func TestLeaseSetReleaseIsAtomicWhenOneFenceIsStale(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	set, err := store.AcquireLeaseSet(
		t.Context(), "delivery_stale", []persistence.LeaseKey{
			deliveryLeaseKey("file", "internal/stale"),
			deliveryLeaseKey("worktree", "delivery_stale"),
		}, "author-a", time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		UPDATE forja.leases SET fencing_token=fencing_token+1
		WHERE tenant_id=$1 AND repository_id=$2
		  AND resource_type=$3 AND resource_id=$4`,
		DefaultTenantID, DefaultRepositoryID,
		set.Leases[0].ResourceType, set.Leases[0].ResourceID,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseLeaseSet(t.Context(), set); !isFaultCode(err, fault.CodeConflict) {
		t.Fatalf("stale release error = %v, want conflict", err)
	}
	var activeMembers int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*) FROM forja.leases
		WHERE tenant_id=$1 AND repository_id=$2
		  AND owner_id='author-a' AND expires_at > clock_timestamp()`,
		DefaultTenantID, DefaultRepositoryID,
	).Scan(&activeMembers); err != nil {
		t.Fatal(err)
	}
	if activeMembers != len(set.Leases) {
		t.Fatalf("active members=%d, want atomic rollback preserving %d", activeMembers, len(set.Leases))
	}
}

func TestExpiredLeaseSetReleaseDoesNotTouchReplacementAuthority(t *testing.T) {
	pool := migratedPool(t)
	store := newIntegrationStore(t, pool)
	keys := []persistence.LeaseKey{
		deliveryLeaseKey("worktree", "delivery_expired_release"),
		deliveryLeaseKey("file", "internal/expired-release"),
		deliveryLeaseKey("file", "internal/zeta"),
		deliveryLeaseKey("file", "internal/éclair"),
	}
	first, err := store.AcquireLeaseSet(
		t.Context(), "attempt_expired_release_first", keys, "first-owner", time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire first release authority: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		UPDATE forja.leases
		SET expires_at=acquired_at
		WHERE tenant_id=$1 AND repository_id=$2 AND owner_id=$3`,
		DefaultTenantID, DefaultRepositoryID, first.OwnerID,
	); err != nil {
		t.Fatalf("expire first release members: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		UPDATE forja.lease_sets
		SET expires_at=acquired_at
		WHERE tenant_id=$1 AND repository_id=$2 AND lease_set_id=$3`,
		DefaultTenantID, DefaultRepositoryID, first.LeaseSetID,
	); err != nil {
		t.Fatalf("expire first release set: %v", err)
	}
	second, err := store.AcquireLeaseSet(
		t.Context(), "attempt_expired_release_second", keys, "second-owner", time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire replacement release authority: %v", err)
	}
	if err := store.ReleaseLeaseSet(t.Context(), first); err != nil {
		t.Fatalf("retire exact expired authority: %v", err)
	}
	if _, err := store.RenewLeaseSet(t.Context(), second, time.Minute); err != nil {
		t.Fatalf("historical release damaged replacement authority: %v", err)
	}
	var firstState string
	if err := pool.QueryRow(t.Context(), `
		SELECT state FROM forja.lease_sets
		WHERE tenant_id=$1 AND repository_id=$2 AND lease_set_id=$3`,
		DefaultTenantID, DefaultRepositoryID, first.LeaseSetID,
	).Scan(&firstState); err != nil {
		t.Fatal(err)
	}
	if firstState != "released" {
		t.Fatalf("expired historical set state = %q, want released", firstState)
	}
}

func deliveryLeaseKey(resourceType string, resourceID string) persistence.LeaseKey {
	return persistence.LeaseKey{
		TenantID: DefaultTenantID, RepositoryID: DefaultRepositoryID,
		ResourceType: resourceType, ResourceID: resourceID,
	}
}

func isFaultCode(err error, code fault.Code) bool {
	var typed *fault.Error
	return errors.As(err, &typed) && typed.Code == code
}
