// Package persistence defines storage-neutral durable coordination contracts.
package persistence

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/identity"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

// Attempt is one durable execution attempt for a run.
type Attempt struct {
	AttemptID         string     `json:"attempt_id"`
	RunID             string     `json:"run_id"`
	Ordinal           int        `json:"ordinal"`
	Status            string     `json:"status"`
	LeaseResourceType string     `json:"lease_resource_type"`
	LeaseResourceID   string     `json:"lease_resource_id"`
	WorkerID          string     `json:"worker_id"`
	FencingToken      int64      `json:"fencing_token"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
	Version           int        `json:"version"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// AttemptRepository creates replay-safe execution attempts.
type AttemptRepository interface {
	CreateAttempt(
		context.Context,
		identity.RunID,
		string,
		runstate.CommandMetadata,
		LeaseProof,
	) (Attempt, error)
}

// AttemptLifecycleRepository persists fenced execution and recovery state.
type AttemptLifecycleRepository interface {
	AttemptRepository
	GetAttempt(context.Context, string) (Attempt, error)
	StartAttempt(
		context.Context,
		string,
		int,
		runstate.CommandMetadata,
		LeaseProof,
	) (Attempt, error)
	FinishAttempt(
		context.Context,
		string,
		int,
		contracts.WorkerResult,
		runstate.CommandMetadata,
		LeaseProof,
	) (Attempt, error)
	ReconcileAbandonedAttempts(
		context.Context,
		runstate.CommandMetadata,
		LeaseProof,
	) ([]Attempt, error)
}

// LeaseProof binds a protected write to one live fenced ownership grant.
type LeaseProof struct {
	LeaseKey
	OwnerID      string
	FencingToken int64
}

// LeaseKey identifies one exclusively coordinated resource.
type LeaseKey struct {
	TenantID     string
	RepositoryID string
	ResourceType string
	ResourceID   string
}

// Lease is a renewable ownership grant protected by a monotonic fencing token.
type Lease struct {
	LeaseKey
	OwnerID      string
	FencingToken int64
	AcquiredAt   time.Time
	ExpiresAt    time.Time
}

// LeaseSet is one immutable, atomically managed collection of fenced grants.
// Its ID identifies an attempt and is independent from member resource IDs.
type LeaseSet struct {
	LeaseSetID string
	OwnerID    string
	Leases     []Lease
	AcquiredAt time.Time
	ExpiresAt  time.Time
}

// LeaseRepository coordinates ownership without exposing database locking.
type LeaseRepository interface {
	AcquireLease(
		context.Context,
		LeaseKey,
		string,
		time.Duration,
	) (Lease, error)
	RenewLease(
		context.Context,
		LeaseKey,
		string,
		int64,
		time.Duration,
	) (Lease, error)
	ReleaseLease(context.Context, LeaseKey, string, int64) error
}

// LeaseSetRepository prevents partial grants and authority expansion after a
// bounded delivery starts.
type LeaseSetRepository interface {
	AcquireLeaseSet(
		context.Context,
		string,
		[]LeaseKey,
		string,
		time.Duration,
	) (LeaseSet, error)
	RenewLeaseSet(context.Context, LeaseSet, time.Duration) (LeaseSet, error)
	ReleaseLeaseSet(context.Context, LeaseSet) error
}

// OutboxMessage is a claimed canonical event awaiting projection.
type OutboxMessage struct {
	OutboxID         int64
	EventID          string
	AggregateType    string
	AggregateID      string
	AggregateVersion int
	EventType        string
	Payload          json.RawMessage
	Attempts         int
	FencingToken     int64
}

// OutboxRepository supports competing workers in one dispatcher group.
// Independent downstream projectors require durable fan-out state, which is
// introduced with those projection adapters in later Sprints.
type OutboxRepository interface {
	ClaimOutbox(
		context.Context,
		string,
		int,
		time.Duration,
	) ([]OutboxMessage, error)
	CompleteOutbox(context.Context, int64, string, int64) error
	FailOutbox(
		context.Context,
		int64,
		string,
		int64,
		error,
		time.Time,
		int,
	) error
}

// ProjectionRepository rebuilds derived state from immutable canonical events.
type ProjectionRepository interface {
	RebuildRunProjection(context.Context, string) error
}
