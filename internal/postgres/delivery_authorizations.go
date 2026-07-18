package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

var (
	deliveryAuthorizationContractsOnce sync.Once
	deliveryAuthorizationContracts     *contracts.Registry
	deliveryAuthorizationContractsErr  error
)

// AuthorizeDelivery records the exact request as an immutable approval event.
// The transaction proves the Sprint decision, queued Run, queued attempt, and
// original scheduler fence are all live before accepting human approval.
func (s *Store) AuthorizeDelivery(
	ctx context.Context,
	request contracts.DeliveryRequest,
	metadata runstate.CommandMetadata,
) (persistence.DeliveryAuthorization, error) {
	if err := validateDeliveryAuthorizationRequest(request); err != nil {
		return persistence.DeliveryAuthorization{}, err
	}
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return persistence.DeliveryAuthorization{}, err
	}
	if metadata.ActorType != "human" || metadata.ActorID == request.AuthorID ||
		metadata.ActorID == request.ValidatorID {
		return persistence.DeliveryAuthorization{}, fault.New(
			fault.CodePermissionDenied,
			"postgres.AuthorizeDelivery",
			"delivery approval requires an independent human actor",
		)
	}
	storageTenant, storageRepository, err := contracts.RepositoryStorageIdentity(
		request.TenantID, request.RepositoryID,
	)
	if err != nil {
		return persistence.DeliveryAuthorization{}, err
	}
	if storageTenant != s.tenantID || storageRepository != s.repositoryID {
		return persistence.DeliveryAuthorization{}, fault.New(
			fault.CodePermissionDenied,
			"postgres.AuthorizeDelivery",
			"delivery authority differs from the bound repository",
		)
	}
	_, requestDigest, err := encodedDeliveryRequest(request)
	if err != nil {
		return persistence.DeliveryAuthorization{}, err
	}
	scope := "authorize_delivery:" + s.repositoryID + ":" +
		request.DeliveryID + ":" + request.AttemptID
	commandHash := hashCommand(metadata, scope, requestDigest)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return persistence.DeliveryAuthorization{}, databaseError("postgres.AuthorizeDelivery.begin", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockIdempotency(ctx, tx, s.tenantID, scope, metadata.IdempotencyKey); err != nil {
		return persistence.DeliveryAuthorization{}, err
	}
	if replay, found, err := loadControlReplay[persistence.DeliveryAuthorization](
		ctx, tx, s.tenantID, scope, metadata.IdempotencyKey, commandHash,
	); err != nil {
		return persistence.DeliveryAuthorization{}, err
	} else if found {
		if err := s.appendSuccessToolAudit(
			ctx, tx, metadata, scope, postgresTimestamp(s.clock.Now()), true,
		); err != nil {
			return persistence.DeliveryAuthorization{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return persistence.DeliveryAuthorization{}, databaseError("postgres.AuthorizeDelivery.replay_commit", err)
		}
		return replay, nil
	}

	var objective, runState, attemptStatus string
	var leaseResourceType, leaseResourceID, workerID string
	var fencingToken int64
	var attemptOrdinal int
	var approvedDecision, liveFence bool
	err = tx.QueryRow(ctx, `
		SELECT r.objective, r.state, a.ordinal, a.status,
		       a.lease_resource_type, a.lease_resource_id,
		       a.worker_id, a.fencing_token,
		       EXISTS (
		         SELECT 1 FROM forja.decisions AS d
		         WHERE d.tenant_id=r.tenant_id AND d.repository_id=r.repository_id
		           AND d.run_id=r.run_id AND d.action='submit_sprint'
		           AND d.status='approved'
		       ),
		       EXISTS (
		         SELECT 1 FROM forja.leases AS l
		         WHERE l.tenant_id=a.tenant_id AND l.repository_id=r.repository_id
		           AND l.resource_type=a.lease_resource_type
		           AND l.resource_id=a.lease_resource_id
		           AND l.owner_id=a.worker_id
		           AND l.fencing_token=a.fencing_token
		           AND l.expires_at > clock_timestamp()
		       )
		FROM forja.runs AS r
		JOIN forja.attempts AS a
		  ON a.tenant_id=r.tenant_id AND a.run_id=r.run_id
		WHERE r.tenant_id=$1 AND r.repository_id=$2 AND r.run_id=$3
		  AND a.attempt_id=$4
		FOR UPDATE OF r, a`,
		s.tenantID, s.repositoryID, request.RunID, request.AttemptID,
	).Scan(
		&objective, &runState, &attemptOrdinal, &attemptStatus,
		&leaseResourceType, &leaseResourceID, &workerID, &fencingToken,
		&approvedDecision, &liveFence,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistence.DeliveryAuthorization{}, fault.New(
			fault.CodeNotFound,
			"postgres.AuthorizeDelivery",
			"delivery Run or attempt was not found",
		)
	}
	if err != nil {
		return persistence.DeliveryAuthorization{}, databaseError("postgres.AuthorizeDelivery.authority", err)
	}
	if objective != request.Objective || runState != "queued" ||
		attemptOrdinal != request.AttemptOrdinal || attemptStatus != "queued" ||
		!approvedDecision || !liveFence {
		return persistence.DeliveryAuthorization{}, fault.New(
			fault.CodeConflict,
			"postgres.AuthorizeDelivery",
			"delivery does not match a human-approved queued Run and live queued attempt",
		)
	}

	approvedAt := postgresTimestamp(s.clock.Now())
	authorization := persistence.DeliveryAuthorization{
		Request: request, RequestSHA256: requestDigest,
		ApprovedBy: metadata.ActorID, ApprovedAt: approvedAt,
	}
	payload, err := json.Marshal(authorization)
	if err != nil {
		return persistence.DeliveryAuthorization{}, fault.Wrap(
			fault.CodeInternal, "postgres.AuthorizeDelivery", "encode authorization", err,
		)
	}
	if err := s.appendEvent(
		ctx, tx, "approval",
		deliveryAuthorizationAggregateID(request.DeliveryID, request.AttemptID),
		1, "delivery.authorized",
		approvedAt, payload, metadata,
	); err != nil {
		return persistence.DeliveryAuthorization{}, err
	}
	if err := s.appendSuccessToolAudit(ctx, tx, metadata, scope, approvedAt, false); err != nil {
		return persistence.DeliveryAuthorization{}, err
	}
	if err := saveControlReplay(
		ctx, tx, s.tenantID, scope, metadata.IdempotencyKey,
		commandHash, 200, authorization,
	); err != nil {
		return persistence.DeliveryAuthorization{}, err
	}
	// Hold and revalidate the exact scheduler fence through transaction commit.
	// The earlier EXISTS check is only an eligibility snapshot; this row lock is
	// the authority barrier that prevents expiry/takeover during authorization.
	if err := s.enforceAttemptFence(ctx, tx, persistence.LeaseProof{
		LeaseKey: persistence.LeaseKey{
			TenantID: s.tenantID, RepositoryID: s.repositoryID,
			ResourceType: leaseResourceType, ResourceID: leaseResourceID,
		},
		OwnerID: workerID, FencingToken: fencingToken,
	}); err != nil {
		return persistence.DeliveryAuthorization{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return persistence.DeliveryAuthorization{}, databaseError("postgres.AuthorizeDelivery.commit", err)
	}
	return authorization, nil
}

// GetDeliveryAuthorization returns only an internally consistent immutable
// event whose actor, timestamp, request bytes, and digest agree.
func (s *Store) GetDeliveryAuthorization(
	ctx context.Context,
	deliveryID string,
	attemptID string,
) (persistence.DeliveryAuthorization, bool, error) {
	if err := contracts.ValidateDeliveryID(deliveryID); err != nil {
		return persistence.DeliveryAuthorization{}, false, err
	}
	if err := contracts.ValidateDeliveryAttemptID(attemptID); err != nil {
		return persistence.DeliveryAuthorization{}, false, err
	}
	var payload []byte
	var actorType, actorID string
	var occurredAt time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT payload, actor_type, actor_id, occurred_at
		FROM forja.events
		WHERE tenant_id=$1 AND repository_id=$2
		  AND aggregate_type='approval' AND aggregate_id=$3
		  AND aggregate_version=1 AND event_type='delivery.authorized'`,
		s.tenantID, s.repositoryID,
		deliveryAuthorizationAggregateID(deliveryID, attemptID),
	).Scan(&payload, &actorType, &actorID, &occurredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistence.DeliveryAuthorization{}, false, nil
	}
	if err != nil {
		return persistence.DeliveryAuthorization{}, false, databaseError("postgres.GetDeliveryAuthorization", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var authorization persistence.DeliveryAuthorization
	if err := decoder.Decode(&authorization); err != nil {
		return persistence.DeliveryAuthorization{}, false, fault.Wrap(
			fault.CodeInternal,
			"postgres.GetDeliveryAuthorization",
			"decode immutable delivery authorization",
			err,
		)
	}
	_, digest, err := encodedDeliveryRequest(authorization.Request)
	if err != nil {
		return persistence.DeliveryAuthorization{}, false, err
	}
	if actorType != "human" || actorID != authorization.ApprovedBy ||
		!occurredAt.UTC().Equal(authorization.ApprovedAt) ||
		authorization.Request.DeliveryID != deliveryID ||
		authorization.Request.AttemptID != attemptID ||
		authorization.RequestSHA256 != digest {
		return persistence.DeliveryAuthorization{}, false, fault.New(
			fault.CodeInternal,
			"postgres.GetDeliveryAuthorization",
			"immutable delivery authorization is internally inconsistent",
		)
	}
	return authorization, true, nil
}

func deliveryAuthorizationAggregateID(deliveryID string, attemptID string) string {
	return deliveryID + ":" + attemptID
}

func validateDeliveryAuthorizationRequest(request contracts.DeliveryRequest) error {
	deliveryAuthorizationContractsOnce.Do(func() {
		deliveryAuthorizationContracts, deliveryAuthorizationContractsErr = contracts.NewRegistry()
	})
	if deliveryAuthorizationContractsErr != nil {
		return fault.Wrap(
			fault.CodeInternal,
			"postgres.AuthorizeDelivery",
			"compile delivery contracts",
			deliveryAuthorizationContractsErr,
		)
	}
	encoded, _, err := encodedDeliveryRequest(request)
	if err != nil {
		return err
	}
	if err := deliveryAuthorizationContracts.ValidateJSON(
		"delivery-request.schema.json", encoded,
	); err != nil {
		return fault.Wrap(
			fault.CodeInvalidArgument,
			"postgres.AuthorizeDelivery",
			"delivery request violates its schema",
			err,
		)
	}
	if err := contracts.ValidateDeliveryRequest(request); err != nil {
		return fault.Wrap(
			fault.CodeInvalidArgument,
			"postgres.AuthorizeDelivery",
			"delivery request violates semantic authority",
			err,
		)
	}
	return nil
}

func encodedDeliveryRequest(request contracts.DeliveryRequest) ([]byte, string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, "", fault.Wrap(
			fault.CodeInvalidArgument,
			"postgres.encodedDeliveryRequest",
			"encode delivery request",
			err,
		)
	}
	digest := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(digest[:]), nil
}
