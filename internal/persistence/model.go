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
	VerifyLease(context.Context, LeaseProof, time.Duration) (Lease, error)
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

// DeliveryAuthorization is the immutable human approval of one complete
// delivery authority envelope. The digest covers every request field.
type DeliveryAuthorization struct {
	Request       contracts.DeliveryRequest `json:"request"`
	RequestSHA256 string                    `json:"request_sha256"`
	ApprovedBy    string                    `json:"approved_by"`
	ApprovedAt    time.Time                 `json:"approved_at"`
}

// DeliveryAuthorizationRepository persists and reads exact request approval.
type DeliveryAuthorizationRepository interface {
	AuthorizeDelivery(
		context.Context,
		contracts.DeliveryRequest,
		runstate.CommandMetadata,
	) (DeliveryAuthorization, error)
	GetDeliveryAuthorization(
		context.Context,
		string,
		string,
	) (DeliveryAuthorization, bool, error)
}

// DeliveryPublicationIntent is the immutable, replay-safe authority persisted
// before a namespaced Git ref may change.
type DeliveryPublicationIntent struct {
	DeliveryID                string
	TenantID                  string
	RepositoryID              string
	AttemptID                 string
	LeaseSetID                string
	LeaseTTLMS                int
	PublicationRef            string
	PublicationPreviousCommit *string
	ResultCommit              string
	AuthoritySHA256           string
	ReceiptSHA256             string
	IntentSHA256              string
	ReceiptJSON               []byte
}

// DeliveryPublication records one prepared or durably published attempt.
type DeliveryPublication struct {
	Intent         DeliveryPublicationIntent
	State          string
	ObservedCommit *string
	PreparedAt     time.Time
	PublishedAt    *time.Time
	UpdatedAt      time.Time
}

// DeliveryPublicationRepository journals the cross-system Git publication
// protocol. Recovery may finalize only an exact prepared intent whose ref is
// independently observed at its exact result commit.
type DeliveryPublicationRepository interface {
	GetDeliveryPublication(
		context.Context,
		string,
		string,
	) (DeliveryPublication, bool, error)
	PrepareDeliveryPublication(
		context.Context,
		DeliveryPublicationIntent,
		LeaseSet,
	) (DeliveryPublication, error)
	CompleteDeliveryPublication(
		context.Context,
		DeliveryPublicationIntent,
		LeaseSet,
		func(context.Context) error,
		func(context.Context) error,
	) (DeliveryPublication, error)
	RecoverDeliveryPublication(
		context.Context,
		DeliveryPublicationIntent,
		string,
	) (DeliveryPublication, error)
	AbandonDeliveryPublication(
		context.Context,
		DeliveryPublicationIntent,
		func(context.Context) (*string, error),
	) (DeliveryPublication, error)
	ConflictDeliveryPublication(
		context.Context,
		DeliveryPublicationIntent,
		*string,
	) (DeliveryPublication, error)
}

// ArtifactPublicationIntent is the immutable command authorized before bytes
// leave PostgreSQL. Object keys and provider settings are deliberately absent.
type ArtifactPublicationIntent struct {
	OperationID string
	ArtifactID  string
	RunID       *string
	Kind        string
	ContentHash string
	SizeBytes   int64
	MediaType   string
	CreatedBy   string
	Provenance  contracts.Provenance
	Metadata    map[string]any
}

// ArtifactPublication is the recoverable database side of the object saga.
type ArtifactPublication struct {
	Intent    ArtifactPublicationIntent
	State     string
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ArtifactEvidence is transport evidence observed only after complete body
// verification. It cannot create authority without the prepared intent.
type ArtifactEvidence struct {
	ObjectKey              string
	ETag                   string
	VersionID              string
	ProviderChecksumSHA256 string
}

// ArtifactReconciliationCandidate is a stale canonical publication whose
// provider body must be re-verified before PostgreSQL may finalize it.
type ArtifactReconciliationCandidate struct {
	Publication  ArtifactPublication
	ExpectedETag string
}

// ArtifactReconciliationRepository exposes recovery commands separately from
// the original publisher's idempotency and actor identity.
type ArtifactReconciliationRepository interface {
	ListArtifactReconciliationCandidates(
		context.Context,
		time.Time,
		int,
	) ([]ArtifactReconciliationCandidate, error)
	CompleteArtifactReconciliation(
		context.Context,
		string,
		ArtifactEvidence,
		runstate.CommandMetadata,
	) (contracts.Artifact, error)
	FailArtifactReconciliation(
		context.Context,
		string,
		string,
		runstate.CommandMetadata,
	) (ArtifactPublication, error)
}

// ArtifactPublicationRepository atomically journals and finalizes the
// PostgreSQL half of content-addressed object publication.
type ArtifactPublicationRepository interface {
	PrepareArtifactPublication(
		context.Context,
		ArtifactPublicationIntent,
		runstate.CommandMetadata,
	) (ArtifactPublication, *contracts.Artifact, error)
	MarkArtifactPublicationUploading(
		context.Context,
		ArtifactPublicationIntent,
		runstate.CommandMetadata,
	) (ArtifactPublication, error)
	CompleteArtifactPublication(
		context.Context,
		ArtifactPublicationIntent,
		ArtifactEvidence,
		runstate.CommandMetadata,
	) (contracts.Artifact, error)
	FailArtifactPublication(
		context.Context,
		ArtifactPublicationIntent,
		string,
		runstate.CommandMetadata,
	) (ArtifactPublication, error)
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

// ProjectionDelivery is one independently leased downstream view of a
// canonical event. It intentionally has its own attempts and fencing token:
// completing another projector must never advance this projector's cursor.
type ProjectionDelivery struct {
	OutboxMessage
	ProjectorName string
}

// ProjectionDeliveryRepository gives derived stores independent, replayable
// fan-out. Registering a consumer atomically backfills the existing outbox;
// subsequent outbox inserts fan out through the database trigger.
type ProjectionDeliveryRepository interface {
	EnsureProjectionConsumer(context.Context, string, [32]byte) error
	ClaimProjectionDeliveries(context.Context, string, string, int, time.Duration) ([]ProjectionDelivery, error)
	CompleteProjectionDelivery(context.Context, string, int64, string, int64) error
	FailProjectionDelivery(context.Context, string, int64, string, int64, error, time.Time, int) error
	RequeueProjectionDelivery(context.Context, string, int64) error
}

// RetrievalPointRepository records the canonical provenance of a successfully
// written derived point. The record is deliberately separate from the vector
// write: a failed canonical receipt leaves the vector untrusted and therefore
// unavailable to governed retrieval until the projector retries it.
type RetrievalPointRepository interface {
	RecordRetrievalProjectionPoint(context.Context, contracts.RetrievalPoint, int64) error
	TombstoneRetrievalProjectionPoints(context.Context, string, string, int64) ([]string, error)
}

// RetrievalRebuildRepository is the explicit operator path for rebuilding a
// deleted or replaced Qdrant generation from canonical outbox history. It
// clears canonical point authority before reopening the independent delivery
// ledger, so stale vectors cannot remain resolvable during replay.
type RetrievalRebuildRepository interface {
	ResetRetrievalProjection(context.Context, string, [32]byte, string) error
}

// RetrievalGenerationConfig is the immutable vector contract for one physical
// Qdrant collection generation. Registering it does not make it serve traffic:
// the operator must first verify and observe the Qdrant alias cutover.
type RetrievalGenerationConfig struct {
	GenerationID         string
	CollectionAlias      string
	CollectionName       string
	EmbeddingModel       string
	EmbeddingVersion     string
	Dimensions           int
	SparseEncoderVersion string
}

// RetrievalGeneration is the canonical lifecycle receipt for a derived vector
// generation. A generation may be active only after a verified external alias
// cutover; a subsequent activation drains the prior active generation.
type RetrievalGeneration struct {
	RetrievalGenerationConfig
	Status      string
	CreatedAt   time.Time
	ActivatedAt *time.Time
	RetiredAt   *time.Time
}

// RetrievalGenerationRepository serializes the PostgreSQL side of blue-green
// retrieval transitions. It intentionally does not own Qdrant mutation: the
// caller must perform the guarded Qdrant cutover first, then durably record
// the matching active generation here.
type RetrievalGenerationRepository interface {
	RegisterRetrievalGeneration(context.Context, RetrievalGenerationConfig) error
	GetRetrievalGeneration(context.Context, string) (RetrievalGeneration, bool, error)
	ActivateRetrievalGeneration(context.Context, string) (*RetrievalGeneration, error)
	RetireRetrievalGeneration(context.Context, string) error
}

// ProjectionRepository rebuilds derived state from immutable canonical events.
type ProjectionRepository interface {
	RebuildRunProjection(context.Context, string) error
}
