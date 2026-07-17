package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/persistence"
)

var leaseTypes = map[string]struct{}{
	"worker":    {},
	"scheduler": {},
	"file":      {},
	"worktree":  {},
}

// AcquireLease grants one active owner and increments the fencing token on
// every takeover.
func (s *Store) AcquireLease(
	ctx context.Context,
	key persistence.LeaseKey,
	ownerID string,
	ttl time.Duration,
) (persistence.Lease, error) {
	key = s.bindLeaseKey(key)
	if err := validateLeaseInput(
		key,
		ownerID,
		ttl,
		s.tenantID,
		s.repositoryID,
	); err != nil {
		return persistence.Lease{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return persistence.Lease{}, databaseError("postgres.AcquireLease.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock($1)",
		advisoryLockKey(
			key.TenantID+"\x00"+s.repositoryID+"\x00"+
				key.ResourceType+"\x00"+key.ResourceID,
		),
	); err != nil {
		return persistence.Lease{}, databaseError("postgres.AcquireLease.lock", err)
	}

	current, active, found, err := getLeaseForUpdate(
		ctx,
		tx,
		key,
		s.repositoryID,
	)
	if err != nil {
		return persistence.Lease{}, err
	}
	var lease persistence.Lease
	if !found {
		err = tx.QueryRow(ctx, `
				INSERT INTO forja.leases (
					tenant_id, repository_id, resource_type, resource_id, owner_id,
					fencing_token, acquired_at, expires_at, updated_at
				) VALUES (
					$1, $2, $3, $4, $5, 1, clock_timestamp(),
					clock_timestamp() + $6::interval, clock_timestamp()
				)
				RETURNING owner_id, fencing_token, acquired_at, expires_at`,
			key.TenantID,
			s.repositoryID,
			key.ResourceType,
			key.ResourceID,
			ownerID,
			intervalString(ttl),
		).Scan(
			&lease.OwnerID,
			&lease.FencingToken,
			&lease.AcquiredAt,
			&lease.ExpiresAt,
		)
	} else if active && current.OwnerID != ownerID {
		return persistence.Lease{}, fault.New(
			fault.CodeConflict,
			"postgres.AcquireLease",
			fmt.Sprintf("lease is owned by %s until %s", current.OwnerID, current.ExpiresAt.UTC()),
		)
	} else if active {
		lease = current
	} else {
		err = tx.QueryRow(ctx, `
			UPDATE forja.leases
				SET owner_id=$1, fencing_token=fencing_token+1,
				    acquired_at=clock_timestamp(),
				    expires_at=clock_timestamp() + $2::interval,
				    updated_at=clock_timestamp()
				WHERE tenant_id=$3 AND repository_id=$4
				  AND resource_type=$5 AND resource_id=$6
				RETURNING owner_id, fencing_token, acquired_at, expires_at`,
			ownerID,
			intervalString(ttl),
			key.TenantID,
			s.repositoryID,
			key.ResourceType,
			key.ResourceID,
		).Scan(
			&lease.OwnerID,
			&lease.FencingToken,
			&lease.AcquiredAt,
			&lease.ExpiresAt,
		)
	}
	if err != nil {
		return persistence.Lease{}, databaseError("postgres.AcquireLease.write", err)
	}
	lease.LeaseKey = key
	lease.AcquiredAt = lease.AcquiredAt.UTC()
	lease.ExpiresAt = lease.ExpiresAt.UTC()
	if err := tx.Commit(ctx); err != nil {
		return persistence.Lease{}, databaseError("postgres.AcquireLease.commit", err)
	}
	return lease, nil
}

// RenewLease extends only a live lease with the exact owner and fencing token.
func (s *Store) RenewLease(
	ctx context.Context,
	key persistence.LeaseKey,
	ownerID string,
	fencingToken int64,
	ttl time.Duration,
) (persistence.Lease, error) {
	key = s.bindLeaseKey(key)
	if err := validateLeaseInput(
		key,
		ownerID,
		ttl,
		s.tenantID,
		s.repositoryID,
	); err != nil {
		return persistence.Lease{}, err
	}
	if fencingToken < 1 {
		return persistence.Lease{}, fault.New(
			fault.CodeInvalidArgument,
			"postgres.RenewLease",
			"fencing token must be positive",
		)
	}
	lease := persistence.Lease{LeaseKey: key}
	err := s.pool.QueryRow(ctx, `
		UPDATE forja.leases
		SET expires_at=clock_timestamp() + $1::interval,
		    updated_at=clock_timestamp()
		WHERE tenant_id=$2 AND repository_id=$3
		  AND resource_type=$4 AND resource_id=$5
		  AND owner_id=$6 AND fencing_token=$7
		  AND expires_at > clock_timestamp()
		RETURNING owner_id, fencing_token, acquired_at, expires_at`,
		intervalString(ttl),
		key.TenantID,
		s.repositoryID,
		key.ResourceType,
		key.ResourceID,
		ownerID,
		fencingToken,
	).Scan(
		&lease.OwnerID,
		&lease.FencingToken,
		&lease.AcquiredAt,
		&lease.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistence.Lease{}, fault.New(
			fault.CodeConflict,
			"postgres.RenewLease",
			"lease is expired, replaced, or owned by another worker",
		)
	}
	if err != nil {
		return persistence.Lease{}, databaseError("postgres.RenewLease", err)
	}
	lease.AcquiredAt = lease.AcquiredAt.UTC()
	lease.ExpiresAt = lease.ExpiresAt.UTC()
	return lease, nil
}

// ReleaseLease expires only the exact fenced ownership grant.
func (s *Store) ReleaseLease(
	ctx context.Context,
	key persistence.LeaseKey,
	ownerID string,
	fencingToken int64,
) error {
	key = s.bindLeaseKey(key)
	if err := validateLeaseKey(key, s.tenantID, s.repositoryID); err != nil {
		return err
	}
	if ownerID == "" || fencingToken < 1 {
		return fault.New(
			fault.CodeInvalidArgument,
			"postgres.ReleaseLease",
			"owner ID and positive fencing token are required",
		)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE forja.leases
		SET expires_at=GREATEST(clock_timestamp(), acquired_at),
		    updated_at=clock_timestamp()
		WHERE tenant_id=$1 AND repository_id=$2
		  AND resource_type=$3 AND resource_id=$4
		  AND owner_id=$5 AND fencing_token=$6
		  AND expires_at > clock_timestamp()`,
		key.TenantID,
		s.repositoryID,
		key.ResourceType,
		key.ResourceID,
		ownerID,
		fencingToken,
	)
	if err != nil {
		return databaseError("postgres.ReleaseLease", err)
	}
	if tag.RowsAffected() != 1 {
		return fault.New(
			fault.CodeConflict,
			"postgres.ReleaseLease",
			"lease is expired, replaced, or owned by another worker",
		)
	}
	return nil
}

func getLeaseForUpdate(
	ctx context.Context,
	tx pgx.Tx,
	key persistence.LeaseKey,
	repositoryID string,
) (persistence.Lease, bool, bool, error) {
	lease := persistence.Lease{LeaseKey: key}
	var active bool
	err := tx.QueryRow(ctx, `
		SELECT owner_id, fencing_token, acquired_at, expires_at,
		       expires_at > clock_timestamp()
		FROM forja.leases
		WHERE tenant_id=$1 AND repository_id=$2
		  AND resource_type=$3 AND resource_id=$4
		FOR UPDATE`,
		key.TenantID,
		repositoryID,
		key.ResourceType,
		key.ResourceID,
	).Scan(
		&lease.OwnerID,
		&lease.FencingToken,
		&lease.AcquiredAt,
		&lease.ExpiresAt,
		&active,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistence.Lease{}, false, false, nil
	}
	if err != nil {
		return persistence.Lease{}, false, false, databaseError("postgres.getLeaseForUpdate", err)
	}
	return lease, active, true, nil
}

func validateLeaseInput(
	key persistence.LeaseKey,
	ownerID string,
	ttl time.Duration,
	tenantID string,
	repositoryID string,
) error {
	if err := validateLeaseKey(key, tenantID, repositoryID); err != nil {
		return err
	}
	if length := utf8.RuneCountInString(ownerID); length < 1 || length > 500 {
		return fault.New(
			fault.CodeInvalidArgument,
			"postgres.validateLeaseInput",
			"owner ID length must be between 1 and 500 characters",
		)
	}
	if ttl < time.Millisecond || ttl > 24*time.Hour {
		return fault.New(
			fault.CodeInvalidArgument,
			"postgres.validateLeaseInput",
			"lease TTL must be between one millisecond and 24 hours",
		)
	}
	return nil
}

func validateLeaseKey(
	key persistence.LeaseKey,
	tenantID string,
	repositoryID string,
) error {
	if key.TenantID == "" ||
		key.TenantID != tenantID ||
		key.RepositoryID == "" ||
		key.RepositoryID != repositoryID ||
		utf8.RuneCountInString(key.ResourceID) < 1 ||
		utf8.RuneCountInString(key.ResourceID) > 500 {
		return fault.New(
			fault.CodeInvalidArgument,
			"postgres.validateLeaseKey",
			"lease authority must match the store and resource ID must contain at most 500 characters",
		)
	}
	if _, ok := leaseTypes[key.ResourceType]; !ok {
		return fault.New(
			fault.CodeInvalidArgument,
			"postgres.validateLeaseKey",
			"resource type must be worker, scheduler, file, or worktree",
		)
	}
	return nil
}

func (s *Store) bindLeaseKey(key persistence.LeaseKey) persistence.LeaseKey {
	if key.RepositoryID == "" {
		key.RepositoryID = s.repositoryID
	}
	return key
}

func intervalString(duration time.Duration) string {
	seconds := duration / time.Second
	microseconds := (duration % time.Second) / time.Microsecond
	return fmt.Sprintf("%d seconds %d microseconds", seconds, microseconds)
}
