package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/persistence"
)

var deliveryLeaseTypes = map[string]struct{}{
	"artifact": {},
	"file":     {},
	"worktree": {},
}

// AcquireLeaseSet atomically grants one immutable set of delivery resources.
// A committed set ID may only replay the byte-identical member set.
func (s *Store) AcquireLeaseSet(
	ctx context.Context,
	leaseSetID string,
	keys []persistence.LeaseKey,
	ownerID string,
	ttl time.Duration,
) (persistence.LeaseSet, error) {
	normalized, digest, err := s.validateLeaseSetInput(leaseSetID, keys, ownerID, ttl)
	if err != nil {
		return persistence.LeaseSet{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return persistence.LeaseSet{}, databaseError("postgres.AcquireLeaseSet.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.lockLeaseSetAndMembers(ctx, tx, leaseSetID, normalized); err != nil {
		return persistence.LeaseSet{}, err
	}

	existing, found, err := s.loadLeaseSetForUpdate(ctx, tx, leaseSetID)
	if err != nil {
		return persistence.LeaseSet{}, err
	}
	if found {
		if existing.OwnerID != ownerID || !bytes.Equal(existing.memberDigest, digest) {
			return persistence.LeaseSet{}, conflictError(
				"postgres.AcquireLeaseSet",
				"lease set ID is already bound to different authority",
			)
		}
		if existing.state != "active" || !existing.ExpiresAt.After(existing.databaseNow) {
			return persistence.LeaseSet{}, conflictError(
				"postgres.AcquireLeaseSet",
				"expired or released lease set IDs cannot be reused",
			)
		}
		leases, err := s.loadAndVerifyLeaseSetMembers(ctx, tx, leaseSetID, ownerID, normalized, nil)
		if err != nil {
			return persistence.LeaseSet{}, err
		}
		result := persistence.LeaseSet{
			LeaseSetID: leaseSetID,
			OwnerID:    ownerID,
			Leases:     leases,
			AcquiredAt: existing.AcquiredAt.UTC(),
			ExpiresAt:  existing.ExpiresAt.UTC(),
		}
		if err := tx.Commit(ctx); err != nil {
			return persistence.LeaseSet{}, databaseError("postgres.AcquireLeaseSet.replay_commit", err)
		}
		return result, nil
	}

	var acquiredAt, expiresAt time.Time
	if err := tx.QueryRow(
		ctx,
		"SELECT clock_timestamp(), clock_timestamp() + $1::interval",
		intervalString(ttl),
	).Scan(&acquiredAt, &expiresAt); err != nil {
		return persistence.LeaseSet{}, databaseError("postgres.AcquireLeaseSet.clock", err)
	}
	// Check every writable path before inserting any member. This lets one set
	// carry file and artifact fences for sibling scopes that share ancestors
	// without mistaking its own newly inserted rows for external conflicts.
	for _, key := range normalized {
		if key.ResourceType == "file" || key.ResourceType == "artifact" {
			active, err := hasActiveWritablePathLease(ctx, tx, key)
			if err != nil {
				return persistence.LeaseSet{}, err
			}
			if active {
				return persistence.LeaseSet{}, conflictError(
					"postgres.AcquireLeaseSet",
					fmt.Sprintf("writable path %s is already leased", key.ResourceID),
				)
			}
		}
	}
	leasing := make([]persistence.Lease, 0, len(normalized))
	for _, key := range normalized {
		current, active, found, err := getLeaseForUpdate(ctx, tx, key, s.repositoryID)
		if err != nil {
			return persistence.LeaseSet{}, err
		}
		if active {
			return persistence.LeaseSet{}, conflictError(
				"postgres.AcquireLeaseSet",
				fmt.Sprintf("resource %s/%s is already leased", key.ResourceType, key.ResourceID),
			)
		}
		lease := persistence.Lease{
			LeaseKey: key, OwnerID: ownerID,
			AcquiredAt: acquiredAt.UTC(), ExpiresAt: expiresAt.UTC(),
		}
		if !found {
			lease.FencingToken = 1
			_, err = tx.Exec(ctx, `
				INSERT INTO forja.leases (
					tenant_id, repository_id, resource_type, resource_id, owner_id,
					fencing_token, acquired_at, expires_at, updated_at
				) VALUES ($1, $2, $3, $4, $5, 1, $6, $7, $6)`,
				key.TenantID, s.repositoryID, key.ResourceType, key.ResourceID,
				ownerID, acquiredAt, expiresAt,
			)
		} else {
			lease.FencingToken = current.FencingToken + 1
			_, err = tx.Exec(ctx, `
				UPDATE forja.leases
				SET owner_id=$1, fencing_token=fencing_token+1,
				    acquired_at=$2, expires_at=$3, updated_at=$2
				WHERE tenant_id=$4 AND repository_id=$5
				  AND resource_type=$6 AND resource_id=$7`,
				ownerID, acquiredAt, expiresAt, key.TenantID, s.repositoryID,
				key.ResourceType, key.ResourceID,
			)
		}
		if err != nil {
			return persistence.LeaseSet{}, databaseError("postgres.AcquireLeaseSet.member", err)
		}
		leasing = append(leasing, lease)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO forja.lease_sets (
			tenant_id, repository_id, lease_set_id, owner_id, member_digest,
			state, acquired_at, expires_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, 'active', $6, $7, $6)`,
		s.tenantID, s.repositoryID, leaseSetID, ownerID, digest, acquiredAt, expiresAt,
	); err != nil {
		return persistence.LeaseSet{}, databaseError("postgres.AcquireLeaseSet.insert_set", err)
	}
	for _, lease := range leasing {
		if _, err := tx.Exec(ctx, `
			INSERT INTO forja.lease_set_members (
				tenant_id, repository_id, lease_set_id, resource_type,
				resource_id, fencing_token
			) VALUES ($1, $2, $3, $4, $5, $6)`,
			s.tenantID, s.repositoryID, leaseSetID, lease.ResourceType,
			lease.ResourceID, lease.FencingToken,
		); err != nil {
			return persistence.LeaseSet{}, databaseError("postgres.AcquireLeaseSet.insert_member", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return persistence.LeaseSet{}, databaseError("postgres.AcquireLeaseSet.commit", err)
	}
	return persistence.LeaseSet{
		LeaseSetID: leaseSetID, OwnerID: ownerID, Leases: leasing,
		AcquiredAt: acquiredAt.UTC(), ExpiresAt: expiresAt.UTC(),
	}, nil
}

// RenewLeaseSet atomically extends only the exact live fenced membership.
func (s *Store) RenewLeaseSet(
	ctx context.Context,
	leaseSet persistence.LeaseSet,
	ttl time.Duration,
) (persistence.LeaseSet, error) {
	keys, proofs, digest, err := s.validateLeaseSetProof(leaseSet, ttl)
	if err != nil {
		return persistence.LeaseSet{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return persistence.LeaseSet{}, databaseError("postgres.RenewLeaseSet.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.lockLeaseSetAndMembers(ctx, tx, leaseSet.LeaseSetID, keys); err != nil {
		return persistence.LeaseSet{}, err
	}
	canonical, err := s.verifyLeaseSetAuthority(ctx, tx, leaseSet, keys, proofs, digest)
	if err != nil {
		return persistence.LeaseSet{}, err
	}
	var databaseNow, expiresAt time.Time
	if err := tx.QueryRow(
		ctx,
		"SELECT clock_timestamp(), clock_timestamp() + $1::interval",
		intervalString(ttl),
	).Scan(&databaseNow, &expiresAt); err != nil {
		return persistence.LeaseSet{}, databaseError("postgres.RenewLeaseSet.clock", err)
	}
	for _, proof := range proofs {
		tag, err := tx.Exec(ctx, `
			UPDATE forja.leases
			SET expires_at=$1, updated_at=$2
			WHERE tenant_id=$3 AND repository_id=$4
			  AND resource_type=$5 AND resource_id=$6
			  AND owner_id=$7 AND fencing_token=$8
			  AND expires_at > $2`,
			expiresAt, databaseNow, s.tenantID, s.repositoryID,
			proof.ResourceType, proof.ResourceID, leaseSet.OwnerID, proof.FencingToken,
		)
		if err != nil {
			return persistence.LeaseSet{}, databaseError("postgres.RenewLeaseSet.member", err)
		}
		if tag.RowsAffected() != 1 {
			return persistence.LeaseSet{}, conflictError("postgres.RenewLeaseSet", "lease set member is stale")
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE forja.lease_sets
		SET expires_at=$1, updated_at=$2
		WHERE tenant_id=$3 AND repository_id=$4 AND lease_set_id=$5`,
		expiresAt, databaseNow, s.tenantID, s.repositoryID, leaseSet.LeaseSetID,
	); err != nil {
		return persistence.LeaseSet{}, databaseError("postgres.RenewLeaseSet.set", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return persistence.LeaseSet{}, databaseError("postgres.RenewLeaseSet.commit", err)
	}
	canonical.ExpiresAt = expiresAt.UTC()
	for index := range canonical.Leases {
		canonical.Leases[index].ExpiresAt = expiresAt.UTC()
	}
	return canonical, nil
}

// ReleaseLeaseSet atomically expires only the exact fenced membership and
// permanently retires the set ID.
func (s *Store) ReleaseLeaseSet(ctx context.Context, leaseSet persistence.LeaseSet) error {
	keys, proofs, digest, err := s.validateLeaseSetProof(leaseSet, time.Millisecond)
	if err != nil {
		return err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return databaseError("postgres.ReleaseLeaseSet.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.lockLeaseSetAndMembers(ctx, tx, leaseSet.LeaseSetID, keys); err != nil {
		return err
	}
	if _, err := s.verifyLeaseSetAuthority(ctx, tx, leaseSet, keys, proofs, digest); err != nil {
		return err
	}
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&databaseNow); err != nil {
		return databaseError("postgres.ReleaseLeaseSet.clock", err)
	}
	for _, proof := range proofs {
		tag, err := tx.Exec(ctx, `
			UPDATE forja.leases
			SET expires_at=GREATEST($1, acquired_at), updated_at=$1
			WHERE tenant_id=$2 AND repository_id=$3
			  AND resource_type=$4 AND resource_id=$5
			  AND owner_id=$6 AND fencing_token=$7
			  AND expires_at > $1`,
			databaseNow, s.tenantID, s.repositoryID, proof.ResourceType,
			proof.ResourceID, leaseSet.OwnerID, proof.FencingToken,
		)
		if err != nil {
			return databaseError("postgres.ReleaseLeaseSet.member", err)
		}
		if tag.RowsAffected() != 1 {
			return conflictError("postgres.ReleaseLeaseSet", "lease set member is stale")
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE forja.lease_sets
		SET state='released', expires_at=GREATEST($1, acquired_at), updated_at=$1
		WHERE tenant_id=$2 AND repository_id=$3 AND lease_set_id=$4`,
		databaseNow, s.tenantID, s.repositoryID, leaseSet.LeaseSetID,
	); err != nil {
		return databaseError("postgres.ReleaseLeaseSet.set", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return databaseError("postgres.ReleaseLeaseSet.commit", err)
	}
	return nil
}

type storedLeaseSet struct {
	persistence.LeaseSet
	memberDigest []byte
	state        string
	databaseNow  time.Time
}

func (s *Store) loadLeaseSetForUpdate(
	ctx context.Context,
	tx pgx.Tx,
	leaseSetID string,
) (storedLeaseSet, bool, error) {
	value := storedLeaseSet{}
	err := tx.QueryRow(ctx, `
		SELECT owner_id, member_digest, state, acquired_at, expires_at,
		       clock_timestamp()
		FROM forja.lease_sets
		WHERE tenant_id=$1 AND repository_id=$2 AND lease_set_id=$3
		FOR UPDATE`,
		s.tenantID, s.repositoryID, leaseSetID,
	).Scan(
		&value.OwnerID, &value.memberDigest, &value.state,
		&value.AcquiredAt, &value.ExpiresAt, &value.databaseNow,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storedLeaseSet{}, false, nil
	}
	if err != nil {
		return storedLeaseSet{}, false, databaseError("postgres.loadLeaseSetForUpdate", err)
	}
	value.LeaseSetID = leaseSetID
	return value, true, nil
}

func (s *Store) verifyLeaseSetAuthority(
	ctx context.Context,
	tx pgx.Tx,
	leaseSet persistence.LeaseSet,
	keys []persistence.LeaseKey,
	proofs []persistence.Lease,
	digest []byte,
) (persistence.LeaseSet, error) {
	stored, found, err := s.loadLeaseSetForUpdate(ctx, tx, leaseSet.LeaseSetID)
	if err != nil {
		return persistence.LeaseSet{}, err
	}
	if !found || stored.state != "active" || stored.OwnerID != leaseSet.OwnerID ||
		!bytes.Equal(stored.memberDigest, digest) || !stored.ExpiresAt.After(stored.databaseNow) {
		return persistence.LeaseSet{}, conflictError("postgres.verifyLeaseSetAuthority", "lease set is missing, stale, or replaced")
	}
	leases, err := s.loadAndVerifyLeaseSetMembers(
		ctx, tx, leaseSet.LeaseSetID, leaseSet.OwnerID, keys, proofs,
	)
	if err != nil {
		return persistence.LeaseSet{}, err
	}
	return persistence.LeaseSet{
		LeaseSetID: leaseSet.LeaseSetID,
		OwnerID:    stored.OwnerID,
		Leases:     leases,
		AcquiredAt: stored.AcquiredAt.UTC(),
		ExpiresAt:  stored.ExpiresAt.UTC(),
	}, nil
}

func (s *Store) loadAndVerifyLeaseSetMembers(
	ctx context.Context,
	tx pgx.Tx,
	leaseSetID string,
	ownerID string,
	keys []persistence.LeaseKey,
	expectedProofs []persistence.Lease,
) ([]persistence.Lease, error) {
	rows, err := tx.Query(ctx, `
		SELECT member.resource_type, member.resource_id, member.fencing_token,
		       lease.owner_id, lease.fencing_token, lease.acquired_at,
		       lease.expires_at, lease.expires_at > clock_timestamp()
		FROM forja.lease_set_members AS member
		JOIN forja.leases AS lease
		  ON lease.tenant_id=member.tenant_id
		 AND lease.repository_id=member.repository_id
		 AND lease.resource_type=member.resource_type
		 AND lease.resource_id=member.resource_id
		WHERE member.tenant_id=$1 AND member.repository_id=$2
		  AND member.lease_set_id=$3
		ORDER BY member.resource_type, member.resource_id`,
		s.tenantID, s.repositoryID, leaseSetID,
	)
	if err != nil {
		return nil, databaseError("postgres.loadAndVerifyLeaseSetMembers.query", err)
	}
	defer rows.Close()
	leases := make([]persistence.Lease, 0, len(keys))
	for rows.Next() {
		var lease persistence.Lease
		var memberToken, liveToken int64
		var liveOwner string
		var active bool
		if err := rows.Scan(
			&lease.ResourceType, &lease.ResourceID, &memberToken,
			&liveOwner, &liveToken, &lease.AcquiredAt, &lease.ExpiresAt, &active,
		); err != nil {
			return nil, databaseError("postgres.loadAndVerifyLeaseSetMembers.scan", err)
		}
		lease.TenantID = s.tenantID
		lease.RepositoryID = s.repositoryID
		lease.OwnerID = liveOwner
		lease.FencingToken = liveToken
		lease.AcquiredAt = lease.AcquiredAt.UTC()
		lease.ExpiresAt = lease.ExpiresAt.UTC()
		if !active || liveOwner != ownerID || liveToken != memberToken {
			return nil, conflictError("postgres.loadAndVerifyLeaseSetMembers", "lease set member is stale or replaced")
		}
		leases = append(leases, lease)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError("postgres.loadAndVerifyLeaseSetMembers.rows", err)
	}
	if len(leases) != len(keys) {
		return nil, conflictError("postgres.loadAndVerifyLeaseSetMembers", "lease set membership is incomplete")
	}
	// PostgreSQL text ordering follows the database collation, while lease-set
	// digests and proofs use byte ordering. Canonicalize after loading so
	// authority checks remain stable across supported database locales.
	slices.SortFunc(leases, func(left, right persistence.Lease) int {
		return compareLeaseKeys(left.LeaseKey, right.LeaseKey)
	})
	for index := range keys {
		if leases[index].LeaseKey != keys[index] {
			return nil, conflictError("postgres.loadAndVerifyLeaseSetMembers", "lease set membership disagrees with authority")
		}
		if expectedProofs != nil && leases[index].FencingToken != expectedProofs[index].FencingToken {
			return nil, conflictError("postgres.loadAndVerifyLeaseSetMembers", "lease set fencing proof is stale")
		}
	}
	return leases, nil
}

func (s *Store) lockLeaseSetAndMembers(
	ctx context.Context,
	tx pgx.Tx,
	leaseSetID string,
	keys []persistence.LeaseKey,
) error {
	if _, err := tx.Exec(ctx, "LOCK TABLE forja.leases IN ACCESS SHARE MODE"); err != nil {
		return databaseError("postgres.lockLeaseSetAndMembers.migration_barrier", err)
	}
	lockNames := []string{s.tenantID + "\x00" + s.repositoryID + "\x00lease-set\x00" + leaseSetID}
	for _, key := range keys {
		lockNames = append(lockNames,
			key.TenantID+"\x00"+s.repositoryID+"\x00"+key.ResourceType+"\x00"+key.ResourceID,
		)
		if key.ResourceType == "file" || key.ResourceType == "artifact" {
			lockNames = append(lockNames,
				key.TenantID+"\x00"+s.repositoryID+"\x00writable-path\x00"+key.ResourceID,
			)
		}
	}
	slices.Sort(lockNames)
	for _, lockName := range lockNames {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", advisoryLockKey(lockName)); err != nil {
			return databaseError("postgres.lockLeaseSetAndMembers", err)
		}
	}
	return nil
}

func (s *Store) validateLeaseSetInput(
	leaseSetID string,
	keys []persistence.LeaseKey,
	ownerID string,
	ttl time.Duration,
) ([]persistence.LeaseKey, []byte, error) {
	if utf8.RuneCountInString(leaseSetID) < 1 || utf8.RuneCountInString(leaseSetID) > 500 {
		return nil, nil, invalidLeaseSetError("lease set ID length must be between 1 and 500 characters")
	}
	if utf8.RuneCountInString(ownerID) < 1 || utf8.RuneCountInString(ownerID) > 500 {
		return nil, nil, invalidLeaseSetError("owner ID length must be between 1 and 500 characters")
	}
	if ttl < time.Millisecond || ttl > 24*time.Hour {
		return nil, nil, invalidLeaseSetError("lease TTL must be between one millisecond and 24 hours")
	}
	if len(keys) < 1 || len(keys) > 1024 {
		return nil, nil, invalidLeaseSetError("lease set must contain between 1 and 1024 resources")
	}
	normalized := make([]persistence.LeaseKey, 0, len(keys))
	seenInput := make(map[string]bool, len(keys))
	seenNormalized := make(map[string]bool, len(keys))
	declaredWritable := make([]persistence.LeaseKey, 0, len(keys))
	worktreeCount := 0
	for _, input := range keys {
		key := s.bindLeaseKey(input)
		if err := validateLeaseKey(key, s.tenantID, s.repositoryID); err != nil {
			return nil, nil, err
		}
		if _, ok := deliveryLeaseTypes[key.ResourceType]; !ok {
			return nil, nil, invalidLeaseSetError("lease sets may contain only file, worktree, and artifact resources")
		}
		if key.ResourceType == "worktree" {
			worktreeCount++
		}
		inputKey := key.ResourceType + "\x00" + key.ResourceID
		if seenInput[inputKey] {
			return nil, nil, invalidLeaseSetError("lease set resources must be unique")
		}
		seenInput[inputKey] = true
		resourceIDs := []string{key.ResourceID}
		if key.ResourceType == "file" || key.ResourceType == "artifact" {
			if err := validateLeaseScopePath(key.ResourceID); err != nil {
				return nil, nil, err
			}
			for _, previous := range declaredWritable {
				if previous.ResourceType != key.ResourceType && leaseScopesOverlap(previous.ResourceID, key.ResourceID) {
					return nil, nil, invalidLeaseSetError("file and artifact lease scopes must not overlap")
				}
			}
			declaredWritable = append(declaredWritable, key)
			resourceIDs = leaseScopeAndAncestors(key.ResourceID)
		}
		for _, resourceID := range resourceIDs {
			expanded := key
			expanded.ResourceID = resourceID
			expandedKey := expanded.ResourceType + "\x00" + expanded.ResourceID
			if !seenNormalized[expandedKey] {
				seenNormalized[expandedKey] = true
				normalized = append(normalized, expanded)
			}
		}
	}
	if worktreeCount != 1 {
		return nil, nil, invalidLeaseSetError("lease set requires exactly one worktree resource")
	}
	if len(normalized) > 1024 {
		return nil, nil, invalidLeaseSetError("expanded lease set exceeds 1024 resources")
	}
	slices.SortFunc(normalized, compareLeaseKeys)
	return normalized, leaseMemberDigest(normalized), nil
}

func hasActiveWritablePathLease(
	ctx context.Context,
	tx pgx.Tx,
	key persistence.LeaseKey,
) (bool, error) {
	var active bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM forja.leases
			WHERE tenant_id=$1 AND repository_id=$2
			  AND resource_type IN ('file', 'artifact')
			  AND (
				resource_id=$3
				OR substring(resource_id FROM 1 FOR char_length($3) + 1)=$3 || '/'
				OR substring($3 FROM 1 FOR char_length(resource_id) + 1)=resource_id || '/'
			  )
			  AND expires_at > clock_timestamp()
		)`,
		key.TenantID, key.RepositoryID, key.ResourceID,
	).Scan(&active); err != nil {
		return false, databaseError("postgres.hasActiveWritablePathLease", err)
	}
	return active, nil
}

func leaseScopesOverlap(left string, right string) bool {
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func validateLeaseScopePath(value string) error {
	if value == "" || value == "." || value == ".." || path.IsAbs(value) ||
		path.Clean(value) != value || strings.HasPrefix(value, "../") ||
		strings.Contains(value, "\\") || strings.ContainsRune(value, '\x00') {
		return invalidLeaseSetError("file and artifact resources must be canonical repository-relative paths")
	}
	return nil
}

func leaseScopeAndAncestors(scope string) []string {
	parts := strings.Split(scope, "/")
	values := make([]string, 0, len(parts))
	for index := range parts {
		values = append(values, strings.Join(parts[:index+1], "/"))
	}
	return values
}

func (s *Store) validateLeaseSetProof(
	leaseSet persistence.LeaseSet,
	ttl time.Duration,
) ([]persistence.LeaseKey, []persistence.Lease, []byte, error) {
	if utf8.RuneCountInString(leaseSet.LeaseSetID) < 1 || utf8.RuneCountInString(leaseSet.LeaseSetID) > 500 {
		return nil, nil, nil, invalidLeaseSetError("lease set ID length must be between 1 and 500 characters")
	}
	if utf8.RuneCountInString(leaseSet.OwnerID) < 1 || utf8.RuneCountInString(leaseSet.OwnerID) > 500 {
		return nil, nil, nil, invalidLeaseSetError("owner ID length must be between 1 and 500 characters")
	}
	if ttl < time.Millisecond || ttl > 24*time.Hour {
		return nil, nil, nil, invalidLeaseSetError("lease TTL must be between one millisecond and 24 hours")
	}
	if len(leaseSet.Leases) < 1 || len(leaseSet.Leases) > 1024 {
		return nil, nil, nil, invalidLeaseSetError("lease set proof must contain between 1 and 1024 resources")
	}
	keys := make([]persistence.LeaseKey, 0, len(leaseSet.Leases))
	proofByKey := make(map[string]persistence.Lease, len(leaseSet.Leases))
	worktreeCount := 0
	for _, lease := range leaseSet.Leases {
		key := s.bindLeaseKey(lease.LeaseKey)
		if err := validateLeaseKey(key, s.tenantID, s.repositoryID); err != nil {
			return nil, nil, nil, err
		}
		if _, ok := deliveryLeaseTypes[key.ResourceType]; !ok {
			return nil, nil, nil, invalidLeaseSetError("lease set proofs may contain only file, worktree, and artifact resources")
		}
		if key.ResourceType == "file" || key.ResourceType == "artifact" {
			if err := validateLeaseScopePath(key.ResourceID); err != nil {
				return nil, nil, nil, err
			}
		}
		if key.ResourceType == "worktree" {
			worktreeCount++
		}
		proofKey := key.ResourceType + "\x00" + key.ResourceID
		if _, duplicate := proofByKey[proofKey]; duplicate {
			return nil, nil, nil, invalidLeaseSetError("lease set proof resources must be unique")
		}
		keys = append(keys, key)
		proofByKey[proofKey] = lease
	}
	if worktreeCount != 1 {
		return nil, nil, nil, invalidLeaseSetError("lease set proof requires exactly one worktree resource")
	}
	slices.SortFunc(keys, compareLeaseKeys)
	proofs := make([]persistence.Lease, 0, len(keys))
	for _, key := range keys {
		proof := proofByKey[key.ResourceType+"\x00"+key.ResourceID]
		if proof.FencingToken < 1 {
			return nil, nil, nil, invalidLeaseSetError("all lease set fencing tokens must be positive")
		}
		proof.LeaseKey = key
		proofs = append(proofs, proof)
	}
	return keys, proofs, leaseMemberDigest(keys), nil
}

func compareLeaseKeys(left persistence.LeaseKey, right persistence.LeaseKey) int {
	leftKey := left.ResourceType + "\x00" + left.ResourceID
	rightKey := right.ResourceType + "\x00" + right.ResourceID
	if leftKey < rightKey {
		return -1
	}
	if leftKey > rightKey {
		return 1
	}
	return 0
}

func leaseMemberDigest(keys []persistence.LeaseKey) []byte {
	hash := sha256.New()
	for _, key := range keys {
		_, _ = hash.Write([]byte(key.ResourceType))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(key.ResourceID))
		_, _ = hash.Write([]byte{'\n'})
	}
	return hash.Sum(nil)
}

func invalidLeaseSetError(message string) error {
	return fault.New(fault.CodeInvalidArgument, "postgres.leaseSet", message)
}

func conflictError(operation string, message string) error {
	return fault.New(fault.CodeConflict, operation, message)
}
