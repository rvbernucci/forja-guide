package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/persistence"
)

const (
	maximumDeliveryReceiptBytes              = 4 << 20
	minimumPublicationAuthorityTimeRemaining = 40 * time.Second
)

var (
	publicationCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	publicationDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// GetDeliveryPublication loads one exact attempt journal record.
func (s *Store) GetDeliveryPublication(
	ctx context.Context,
	deliveryID string,
	attemptID string,
) (persistence.DeliveryPublication, bool, error) {
	record, found, err := loadDeliveryPublication(
		ctx, s.pool, s.tenantID, s.repositoryID, deliveryID, attemptID, false,
	)
	if err != nil {
		return persistence.DeliveryPublication{}, false, err
	}
	return record, found, nil
}

// PrepareDeliveryPublication durably records an exact intent while every
// member of the supplied lease set is still live and fenced. An immutable,
// exact published record remains replayable after that authority is released.
func (s *Store) PrepareDeliveryPublication(
	ctx context.Context,
	intent persistence.DeliveryPublicationIntent,
	leaseSet persistence.LeaseSet,
) (persistence.DeliveryPublication, error) {
	keys, proofs, digest, err := s.validatePublicationAuthority(intent, leaseSet)
	if err != nil {
		return persistence.DeliveryPublication{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return persistence.DeliveryPublication{}, databaseError("postgres.PrepareDeliveryPublication.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.lockPublicationAndLeaseSet(ctx, tx, intent, keys); err != nil {
		return persistence.DeliveryPublication{}, err
	}
	existing, found, err := loadDeliveryPublication(
		ctx, tx, s.tenantID, s.repositoryID,
		intent.DeliveryID, intent.AttemptID, true,
	)
	if err != nil {
		return persistence.DeliveryPublication{}, err
	}
	if found {
		if err := exactPublicationIntent(existing.Intent, intent); err != nil {
			return persistence.DeliveryPublication{}, err
		}
		// A published record is immutable authority and may be replayed after
		// the original lease was released. Every nonterminal intent still
		// requires the exact live fence below.
		if existing.State == "published" {
			if err := tx.Commit(ctx); err != nil {
				return persistence.DeliveryPublication{}, databaseError("postgres.PrepareDeliveryPublication.published_replay_commit", err)
			}
			return existing, nil
		}
	}
	if _, err := s.verifyLeaseSetAuthority(ctx, tx, leaseSet, keys, proofs, digest); err != nil {
		return persistence.DeliveryPublication{}, err
	}
	if found {
		if err := tx.Commit(ctx); err != nil {
			return persistence.DeliveryPublication{}, databaseError("postgres.PrepareDeliveryPublication.replay_commit", err)
		}
		return existing, nil
	}
	authorityDigest, _ := hex.DecodeString(intent.AuthoritySHA256)
	receiptDigest, _ := hex.DecodeString(intent.ReceiptSHA256)
	intentDigest, _ := hex.DecodeString(intent.IntentSHA256)
	var preparedAt time.Time
	if err := tx.QueryRow(ctx, `
		INSERT INTO forja.delivery_publications (
			tenant_id, repository_id, delivery_id, attempt_id, lease_set_id,
			publication_ref, publication_previous_commit, result_commit,
			authority_sha256, receipt_sha256, intent_sha256, receipt_bytes,
			state, prepared_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
			'prepared', clock_timestamp(), clock_timestamp()
		)
		RETURNING prepared_at`,
		s.tenantID, s.repositoryID, intent.DeliveryID, intent.AttemptID,
		intent.LeaseSetID, intent.PublicationRef, intent.PublicationPreviousCommit,
		intent.ResultCommit, authorityDigest, receiptDigest, intentDigest,
		intent.ReceiptJSON,
	).Scan(&preparedAt); err != nil {
		return persistence.DeliveryPublication{}, databaseError("postgres.PrepareDeliveryPublication.insert", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return persistence.DeliveryPublication{}, databaseError("postgres.PrepareDeliveryPublication.commit", err)
	}
	preparedAt = preparedAt.UTC()
	return persistence.DeliveryPublication{
		Intent: clonePublicationIntent(intent), State: "prepared",
		PreparedAt: preparedAt, UpdatedAt: preparedAt,
	}, nil
}

// CompleteDeliveryPublication persists the published receipt only while the
// exact publication lease set remains live. An immutable, exact published
// record remains replayable after that authority is released.
func (s *Store) CompleteDeliveryPublication(
	ctx context.Context,
	intent persistence.DeliveryPublicationIntent,
	leaseSet persistence.LeaseSet,
	revalidate func(context.Context) error,
	apply func(context.Context) error,
) (persistence.DeliveryPublication, error) {
	if revalidate == nil || apply == nil {
		return persistence.DeliveryPublication{}, invalidPublicationError("publication revalidation and apply callbacks are required")
	}
	keys, proofs, digest, err := s.validatePublicationAuthority(intent, leaseSet)
	if err != nil {
		return persistence.DeliveryPublication{}, err
	}
	publicationAt, err := publicationReceiptTime(intent)
	if err != nil {
		return persistence.DeliveryPublication{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return persistence.DeliveryPublication{}, databaseError("postgres.CompleteDeliveryPublication.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.lockPublicationAndLeaseSet(ctx, tx, intent, keys); err != nil {
		return persistence.DeliveryPublication{}, err
	}
	record, found, err := loadDeliveryPublication(
		ctx, tx, s.tenantID, s.repositoryID,
		intent.DeliveryID, intent.AttemptID, true,
	)
	if err != nil {
		return persistence.DeliveryPublication{}, err
	}
	if found {
		if err := exactPublicationIntent(record.Intent, intent); err != nil {
			return persistence.DeliveryPublication{}, err
		}
		// Completion can race with another exact publisher that has already
		// committed the receipt and released the lease. The immutable published
		// row is sufficient for exact replay; prepared work remains fenced.
		if record.State == "published" {
			if err := tx.Commit(ctx); err != nil {
				return persistence.DeliveryPublication{}, databaseError("postgres.CompleteDeliveryPublication.replay_commit", err)
			}
			return record, nil
		}
	}
	if _, err := s.verifyLeaseSetAuthorityWithMinimum(
		ctx, tx, leaseSet, keys, proofs, digest,
		minimumPublicationAuthorityTimeRemaining,
	); err != nil {
		return persistence.DeliveryPublication{}, err
	}
	if !found {
		return persistence.DeliveryPublication{}, fault.New(
			fault.CodeNotFound, "postgres.CompleteDeliveryPublication", "publication intent was not prepared",
		)
	}
	if record.State != "prepared" {
		return persistence.DeliveryPublication{}, conflictError(
			"postgres.CompleteDeliveryPublication", "publication intent is not prepared",
		)
	}
	// The transaction holds every lease-set/resource advisory lock and the
	// publication lock across the bounded Git CAS. No compliant writer can
	// replace the fence until the immutable receipt is committed or this
	// transaction rolls back.
	if err := revalidate(ctx); err != nil {
		return persistence.DeliveryPublication{}, err
	}
	// Revalidate the minimum authority horizon after the potentially expensive
	// evidence read and immediately before the bounded Git compare-and-swap.
	if _, err := s.verifyLeaseSetAuthorityWithMinimum(
		ctx, tx, leaseSet, keys, proofs, digest,
		minimumPublicationAuthorityTimeRemaining,
	); err != nil {
		return persistence.DeliveryPublication{}, err
	}
	if err := apply(ctx); err != nil {
		return persistence.DeliveryPublication{}, err
	}
	var publishedAt, updatedAt time.Time
	if err := tx.QueryRow(ctx, `
		UPDATE forja.delivery_publications
		SET state='published', observed_commit=result_commit,
		    published_at=$1, updated_at=clock_timestamp()
		WHERE tenant_id=$2 AND repository_id=$3
		  AND delivery_id=$4 AND attempt_id=$5 AND state='prepared'
		RETURNING published_at, updated_at`,
		publicationAt.Truncate(time.Microsecond), s.tenantID, s.repositoryID,
		intent.DeliveryID, intent.AttemptID,
	).Scan(&publishedAt, &updatedAt); err != nil {
		return persistence.DeliveryPublication{}, databaseError("postgres.CompleteDeliveryPublication.update", err)
	}
	if !publishedAt.UTC().Equal(publicationAt.Truncate(time.Microsecond)) {
		return persistence.DeliveryPublication{}, invalidPublicationError("database publication timestamp disagrees with receipt")
	}
	if err := tx.Commit(ctx); err != nil {
		return persistence.DeliveryPublication{}, databaseError("postgres.CompleteDeliveryPublication.commit", err)
	}
	publishedAt = publicationAt
	updatedAt = updatedAt.UTC()
	record.State = "published"
	record.ObservedCommit = cloneOptionalString(&intent.ResultCommit)
	record.PublishedAt = &publishedAt
	record.UpdatedAt = updatedAt
	return record, nil
}

// RecoverDeliveryPublication closes the crash window only when a trusted Git
// observer supplies the exact result commit already bound to the intent.
func (s *Store) RecoverDeliveryPublication(
	ctx context.Context,
	intent persistence.DeliveryPublicationIntent,
	observedCommit string,
) (persistence.DeliveryPublication, error) {
	if err := validatePublicationIntent(intent); err != nil {
		return persistence.DeliveryPublication{}, err
	}
	if err := s.validatePublicationScope(intent); err != nil {
		return persistence.DeliveryPublication{}, err
	}
	if observedCommit != intent.ResultCommit {
		return persistence.DeliveryPublication{}, fault.New(
			fault.CodeConflict, "postgres.RecoverDeliveryPublication",
			"observed ref does not equal the prepared result commit",
		)
	}
	return s.transitionPublicationWithoutLease(ctx, intent, "published", &observedCommit)
}

// ConflictDeliveryPublication retires a prepared attempt when the trusted Git
// observer finds an unapproved ref state.
func (s *Store) ConflictDeliveryPublication(
	ctx context.Context,
	intent persistence.DeliveryPublicationIntent,
	observedCommit *string,
) (persistence.DeliveryPublication, error) {
	if err := validatePublicationIntent(intent); err != nil {
		return persistence.DeliveryPublication{}, err
	}
	if err := s.validatePublicationScope(intent); err != nil {
		return persistence.DeliveryPublication{}, err
	}
	if observedCommit != nil && !publicationCommitPattern.MatchString(*observedCommit) {
		return persistence.DeliveryPublication{}, fault.New(
			fault.CodeInvalidArgument, "postgres.ConflictDeliveryPublication",
			"observed commit is not canonical",
		)
	}
	return s.transitionPublicationWithoutLease(ctx, intent, "conflict", observedCommit)
}

func (s *Store) transitionPublicationWithoutLease(
	ctx context.Context,
	intent persistence.DeliveryPublicationIntent,
	state string,
	observedCommit *string,
) (persistence.DeliveryPublication, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return persistence.DeliveryPublication{}, databaseError("postgres.transitionPublicationWithoutLease.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.lockPublication(ctx, tx, intent); err != nil {
		return persistence.DeliveryPublication{}, err
	}
	record, found, err := loadDeliveryPublication(
		ctx, tx, s.tenantID, s.repositoryID,
		intent.DeliveryID, intent.AttemptID, true,
	)
	if err != nil {
		return persistence.DeliveryPublication{}, err
	}
	if !found {
		return persistence.DeliveryPublication{}, fault.New(
			fault.CodeNotFound, "postgres.transitionPublicationWithoutLease", "publication intent was not prepared",
		)
	}
	if err := exactPublicationIntent(record.Intent, intent); err != nil {
		return persistence.DeliveryPublication{}, err
	}
	if record.State == state && optionalStringEqual(record.ObservedCommit, observedCommit) {
		if err := tx.Commit(ctx); err != nil {
			return persistence.DeliveryPublication{}, databaseError("postgres.transitionPublicationWithoutLease.replay_commit", err)
		}
		return record, nil
	}
	if record.State != "prepared" {
		return persistence.DeliveryPublication{}, conflictError(
			"postgres.transitionPublicationWithoutLease", "publication is no longer prepared",
		)
	}
	var changedAt, publishedAt time.Time
	if state == "published" {
		publishedAt, err = publicationReceiptTime(intent)
		if err != nil {
			return persistence.DeliveryPublication{}, err
		}
		err = tx.QueryRow(ctx, `
			UPDATE forja.delivery_publications
			SET state='published', observed_commit=$1,
			    published_at=$2, updated_at=clock_timestamp()
			WHERE tenant_id=$3 AND repository_id=$4
			  AND delivery_id=$5 AND attempt_id=$6 AND state='prepared'
			RETURNING updated_at`,
			observedCommit, publishedAt.Truncate(time.Microsecond), s.tenantID, s.repositoryID,
			intent.DeliveryID, intent.AttemptID,
		).Scan(&changedAt)
	} else {
		err = tx.QueryRow(ctx, `
			UPDATE forja.delivery_publications
			SET state='conflict', observed_commit=$1, updated_at=clock_timestamp()
			WHERE tenant_id=$2 AND repository_id=$3
			  AND delivery_id=$4 AND attempt_id=$5 AND state='prepared'
			RETURNING updated_at`,
			observedCommit, s.tenantID, s.repositoryID,
			intent.DeliveryID, intent.AttemptID,
		).Scan(&changedAt)
	}
	if err != nil {
		return persistence.DeliveryPublication{}, databaseError("postgres.transitionPublicationWithoutLease.update", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return persistence.DeliveryPublication{}, databaseError("postgres.transitionPublicationWithoutLease.commit", err)
	}
	changedAt = changedAt.UTC()
	record.State = state
	record.ObservedCommit = cloneOptionalString(observedCommit)
	record.UpdatedAt = changedAt
	if state == "published" {
		publishedAt = publishedAt.UTC()
		record.PublishedAt = &publishedAt
	}
	return record, nil
}

func (s *Store) validatePublicationAuthority(
	intent persistence.DeliveryPublicationIntent,
	leaseSet persistence.LeaseSet,
) ([]persistence.LeaseKey, []persistence.Lease, []byte, error) {
	if err := validatePublicationIntent(intent); err != nil {
		return nil, nil, nil, err
	}
	if err := s.validatePublicationScope(intent); err != nil {
		return nil, nil, nil, err
	}
	if leaseSet.LeaseSetID != intent.LeaseSetID {
		return nil, nil, nil, invalidPublicationError("lease set ID disagrees with publication intent")
	}
	keys, proofs, digest, err := s.validateLeaseSetProof(leaseSet, time.Millisecond)
	if err != nil {
		return nil, nil, nil, err
	}
	var receipt contracts.DeliveryReceipt
	if err := json.Unmarshal(intent.ReceiptJSON, &receipt); err != nil {
		return nil, nil, nil, invalidPublicationError("receipt bytes are not valid JSON")
	}
	if len(receipt.LeaseFences) != len(proofs) {
		return nil, nil, nil, invalidPublicationError("receipt lease fences disagree with lease set")
	}
	for index, proof := range proofs {
		fence := receipt.LeaseFences[index]
		if fence.ResourceType != proof.ResourceType || fence.ResourceID != proof.ResourceID ||
			fence.OwnerID != proof.OwnerID || fence.FencingToken != proof.FencingToken {
			return nil, nil, nil, invalidPublicationError("receipt lease fences disagree with lease set")
		}
	}
	return keys, proofs, digest, nil
}

func validatePublicationIntent(intent persistence.DeliveryPublicationIntent) error {
	if err := contracts.ValidateRepositoryStorageIdentity(intent.TenantID, intent.RepositoryID); err != nil {
		return invalidPublicationError("publication intent has an invalid repository identity")
	}
	if intent.LeaseSetID != intent.AttemptID || intent.DeliveryID == "" || intent.AttemptID == "" ||
		intent.PublicationRef != "refs/forja/deliveries/"+intent.DeliveryID ||
		!publicationCommitPattern.MatchString(intent.ResultCommit) ||
		(intent.PublicationPreviousCommit != nil && !publicationCommitPattern.MatchString(*intent.PublicationPreviousCommit)) ||
		!publicationDigestPattern.MatchString(intent.AuthoritySHA256) ||
		!publicationDigestPattern.MatchString(intent.ReceiptSHA256) ||
		!publicationDigestPattern.MatchString(intent.IntentSHA256) ||
		len(intent.ReceiptJSON) < 2 || len(intent.ReceiptJSON) > maximumDeliveryReceiptBytes {
		return invalidPublicationError("publication intent is not canonical")
	}
	receiptDigest := sha256.Sum256(intent.ReceiptJSON)
	if intent.ReceiptSHA256 != fmt.Sprintf("%x", receiptDigest) {
		return invalidPublicationError("receipt digest disagrees with receipt bytes")
	}
	var receipt contracts.DeliveryReceipt
	if err := json.Unmarshal(intent.ReceiptJSON, &receipt); err != nil {
		return invalidPublicationError("receipt bytes are not valid JSON")
	}
	if receipt.SchemaVersion != contracts.DeliverySchemaVersion || receipt.Status != "published" {
		return invalidPublicationError("receipt contract version or state is unsupported")
	}
	receiptTenantID, receiptRepositoryID, err := contracts.RepositoryStorageIdentity(
		receipt.TenantID, receipt.RepositoryID,
	)
	if err != nil {
		return invalidPublicationError("receipt repository identity is not canonical")
	}
	canonical, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(canonical, intent.ReceiptJSON) {
		return invalidPublicationError("receipt bytes are not canonical")
	}
	if receipt.DeliveryID != intent.DeliveryID || receiptTenantID != intent.TenantID ||
		receiptRepositoryID != intent.RepositoryID || receipt.PublicationRef != intent.PublicationRef ||
		receipt.ResultCommit != intent.ResultCommit ||
		!optionalStringEqual(receipt.PublicationPreviousCommit, intent.PublicationPreviousCommit) {
		return invalidPublicationError("receipt identity disagrees with publication intent")
	}
	identityDocument := struct {
		DeliveryID      string `json:"delivery_id"`
		TenantID        string `json:"tenant_id"`
		RepositoryID    string `json:"repository_id"`
		AttemptID       string `json:"attempt_id"`
		LeaseSetID      string `json:"lease_set_id"`
		AuthoritySHA256 string `json:"authority_sha256"`
		ReceiptSHA256   string `json:"receipt_sha256"`
	}{
		intent.DeliveryID, intent.TenantID, intent.RepositoryID,
		intent.AttemptID, intent.LeaseSetID,
		intent.AuthoritySHA256, intent.ReceiptSHA256,
	}
	identityJSON, err := json.Marshal(identityDocument)
	if err != nil {
		return invalidPublicationError("publication intent identity cannot be encoded")
	}
	identityDigest := sha256.Sum256(identityJSON)
	if intent.IntentSHA256 != fmt.Sprintf("%x", identityDigest) {
		return invalidPublicationError("intent digest disagrees with publication identity")
	}
	return nil
}

func publicationReceiptTime(intent persistence.DeliveryPublicationIntent) (time.Time, error) {
	var receipt contracts.DeliveryReceipt
	if err := json.Unmarshal(intent.ReceiptJSON, &receipt); err != nil {
		return time.Time{}, invalidPublicationError("receipt bytes are not valid JSON")
	}
	if receipt.CreatedAt.IsZero() || receipt.PublishedAt.IsZero() ||
		receipt.PublishedAt.Before(receipt.CreatedAt) {
		return time.Time{}, invalidPublicationError("receipt operation timestamps are inconsistent")
	}
	return receipt.PublishedAt.UTC(), nil
}

func (s *Store) validatePublicationScope(intent persistence.DeliveryPublicationIntent) error {
	if intent.TenantID != s.tenantID || intent.RepositoryID != s.repositoryID {
		return invalidPublicationError("publication intent is outside the store repository scope")
	}
	return nil
}

func (s *Store) lockPublicationAndLeaseSet(
	ctx context.Context,
	tx pgx.Tx,
	intent persistence.DeliveryPublicationIntent,
	keys []persistence.LeaseKey,
) error {
	if err := s.lockLeaseSetAndMembers(ctx, tx, intent.LeaseSetID, keys); err != nil {
		return err
	}
	return s.lockPublicationAdvisory(ctx, tx, intent)
}

func (s *Store) lockPublication(
	ctx context.Context,
	tx pgx.Tx,
	intent persistence.DeliveryPublicationIntent,
) error {
	// Every publication writer crosses the lease relation so incremental
	// migrations can quiesce old and new runtime versions with one barrier.
	if _, err := tx.Exec(ctx, "LOCK TABLE forja.leases IN ACCESS SHARE MODE"); err != nil {
		return databaseError("postgres.lockPublication.migration_barrier", err)
	}
	return s.lockPublicationAdvisory(ctx, tx, intent)
}

func (s *Store) lockPublicationAdvisory(
	ctx context.Context,
	tx pgx.Tx,
	intent persistence.DeliveryPublicationIntent,
) error {
	name := s.tenantID + "\x00" + s.repositoryID + "\x00delivery-publication\x00" +
		intent.DeliveryID + "\x00" + intent.AttemptID
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", advisoryLockKey(name)); err != nil {
		return databaseError("postgres.lockPublicationAdvisory", err)
	}
	return nil
}

type publicationQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadDeliveryPublication(
	ctx context.Context,
	querier publicationQuerier,
	tenantID string,
	repositoryID string,
	deliveryID string,
	attemptID string,
	forUpdate bool,
) (persistence.DeliveryPublication, bool, error) {
	query := `
		SELECT lease_set_id, publication_ref, publication_previous_commit,
		       result_commit, encode(authority_sha256, 'hex'),
		       encode(receipt_sha256, 'hex'), encode(intent_sha256, 'hex'),
		       receipt_bytes, state, observed_commit, prepared_at,
		       published_at, updated_at
		FROM forja.delivery_publications
		WHERE tenant_id=$1 AND repository_id=$2
		  AND delivery_id=$3 AND attempt_id=$4`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var record persistence.DeliveryPublication
	var publishedAt *time.Time
	err := querier.QueryRow(
		ctx, query, tenantID, repositoryID, deliveryID, attemptID,
	).Scan(
		&record.Intent.LeaseSetID, &record.Intent.PublicationRef,
		&record.Intent.PublicationPreviousCommit, &record.Intent.ResultCommit,
		&record.Intent.AuthoritySHA256, &record.Intent.ReceiptSHA256,
		&record.Intent.IntentSHA256, &record.Intent.ReceiptJSON,
		&record.State, &record.ObservedCommit, &record.PreparedAt,
		&publishedAt, &record.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistence.DeliveryPublication{}, false, nil
	}
	if err != nil {
		return persistence.DeliveryPublication{}, false, databaseError("postgres.loadDeliveryPublication", err)
	}
	record.Intent.DeliveryID = deliveryID
	record.Intent.TenantID = tenantID
	record.Intent.RepositoryID = repositoryID
	record.Intent.AttemptID = attemptID
	record.Intent.ReceiptJSON = bytes.Clone(record.Intent.ReceiptJSON)
	record.PreparedAt = record.PreparedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	if publishedAt != nil {
		value, timestampErr := publicationReceiptTime(record.Intent)
		if timestampErr != nil {
			return persistence.DeliveryPublication{}, false, timestampErr
		}
		record.PublishedAt = &value
	}
	return record, true, nil
}

func exactPublicationIntent(
	stored persistence.DeliveryPublicationIntent,
	supplied persistence.DeliveryPublicationIntent,
) error {
	if stored.DeliveryID != supplied.DeliveryID || stored.TenantID != supplied.TenantID ||
		stored.RepositoryID != supplied.RepositoryID || stored.AttemptID != supplied.AttemptID ||
		stored.LeaseSetID != supplied.LeaseSetID || stored.PublicationRef != supplied.PublicationRef ||
		!optionalStringEqual(stored.PublicationPreviousCommit, supplied.PublicationPreviousCommit) ||
		stored.ResultCommit != supplied.ResultCommit || stored.AuthoritySHA256 != supplied.AuthoritySHA256 ||
		stored.ReceiptSHA256 != supplied.ReceiptSHA256 || stored.IntentSHA256 != supplied.IntentSHA256 ||
		!bytes.Equal(stored.ReceiptJSON, supplied.ReceiptJSON) {
		return conflictError("postgres.exactPublicationIntent", "publication attempt was replayed with different authority")
	}
	return nil
}

func clonePublicationIntent(
	value persistence.DeliveryPublicationIntent,
) persistence.DeliveryPublicationIntent {
	value.PublicationPreviousCommit = cloneOptionalString(value.PublicationPreviousCommit)
	value.ReceiptJSON = bytes.Clone(value.ReceiptJSON)
	return value
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func optionalStringEqual(left *string, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func invalidPublicationError(message string) error {
	return fault.New(fault.CodeInvalidArgument, "postgres.deliveryPublication", message)
}
