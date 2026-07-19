package retrieval

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/indexing"
	"github.com/rvbernucci/forja-guide/internal/observability"
	"github.com/rvbernucci/forja-guide/internal/persistence"
)

const DefaultQdrantProjectorName = "qdrant.retrieval"

// ActiveIndexSource exposes only the canonical source needed to rebuild the
// first retrieval family. It prevents the projection worker from treating an
// event payload or a Qdrant payload as authoritative source material.
type ActiveIndexSource interface {
	GetActiveIndexBundle(context.Context) (indexing.IndexBundle, bool, error)
}

// ProjectionRun reports bounded, non-content operational outcomes. It is
// suitable for metrics and receipts without leaking prompts, cards, or paths.
type ProjectionRun struct {
	Claimed   int
	Published int
	Skipped   int
	Retried   int
	Dead      int
}

// ProjectionWorker turns active canonical symbol metadata into Qdrant points.
// It makes the delivery acknowledgement last: a vector acknowledgement and a
// PostgreSQL provenance receipt are both required before a fenced delivery can
// advance the independent projector checkpoint.
type ProjectionWorker struct {
	Deliveries persistence.ProjectionDeliveryRepository
	Source     ActiveIndexSource
	Recorder   persistence.RetrievalPointRepository
	Writer     PointWriter
	Deleter    PointDeleter
	Embedder   Embedder
	Sparse     SparseEncoder

	ProjectorName   string
	WorkerID        string
	Generation      string
	BatchSize       int
	ClaimTTL        time.Duration
	MaxAttempts     int
	RetryDelay      time.Duration
	DeliveryTimeout time.Duration
	Now             func() time.Time
	Observer        *observability.Observer
}

func (worker ProjectionWorker) ProcessOnce(ctx context.Context) (run ProjectionRun, err error) {
	if worker.Observer != nil {
		observedCtx, operation := worker.Observer.Start(ctx, observability.BoundaryRetrieval, observability.OperationProjectRetrieval)
		ctx = observedCtx
		defer func() {
			worker.Observer.RecordRetrievalStats(ctx, observability.RetrievalStats{
				ProjectionClaimed: run.Claimed, ProjectionPublished: run.Published,
				ProjectionSkipped: run.Skipped, ProjectionRetried: run.Retried, ProjectionDead: run.Dead,
			})
			operation.End(err)
		}()
	}
	if err := worker.validate(); err != nil {
		return ProjectionRun{}, err
	}
	deliveries, err := worker.Deliveries.ClaimProjectionDeliveries(
		ctx, worker.projectorName(), worker.WorkerID, worker.BatchSize, worker.ClaimTTL,
	)
	if err != nil {
		return ProjectionRun{}, fmt.Errorf("claim retrieval projection deliveries: %w", err)
	}
	run = ProjectionRun{Claimed: len(deliveries)}
	for _, delivery := range deliveries {
		// A shutdown leaves the fenced delivery unacknowledged for its lease to
		// expire. It must not try to write a retry receipt through a cancelled
		// database context and accidentally claim progress.
		if err := ctx.Err(); err != nil {
			return run, err
		}
		deliveryContext, cancel := context.WithTimeout(ctx, worker.deliveryTimeout())
		skipped, projectErr := worker.projectDelivery(deliveryContext, delivery)
		cancel()
		if projectErr == nil {
			if err := worker.Deliveries.CompleteProjectionDelivery(
				ctx, worker.projectorName(), delivery.OutboxID, worker.WorkerID, delivery.FencingToken,
			); err != nil {
				return run, fmt.Errorf("complete retrieval projection delivery %d: %w", delivery.OutboxID, err)
			}
			run.Published++
			if skipped {
				run.Skipped++
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			return run, err
		}
		retryAt := worker.now().Add(worker.RetryDelay)
		if err := worker.Deliveries.FailProjectionDelivery(
			ctx, worker.projectorName(), delivery.OutboxID, worker.WorkerID, delivery.FencingToken,
			projectErr, retryAt, worker.MaxAttempts,
		); err != nil {
			return run, fmt.Errorf("fail retrieval projection delivery %d: %w", delivery.OutboxID, err)
		}
		run.Retried++
		if delivery.Attempts >= worker.MaxAttempts {
			run.Dead++
		}
	}
	return run, nil
}

func (worker ProjectionWorker) projectDelivery(ctx context.Context, delivery persistence.ProjectionDelivery) (bool, error) {
	// Historical outbox replay is intentional. Only the currently active
	// activation can project data; every other event is a safe no-op.
	if delivery.AggregateType != "index_snapshot" {
		return true, nil
	}
	if delivery.EventType == "index_snapshot.superseded" {
		return worker.tombstoneSupersededSnapshot(ctx, delivery)
	}
	if delivery.EventType != "index_snapshot.activated" {
		return true, nil
	}
	bundle, found, err := worker.Source.GetActiveIndexBundle(ctx)
	if err != nil {
		return false, fmt.Errorf("load active canonical index: %w", err)
	}
	if !found || bundle.Snapshot.SnapshotID != delivery.AggregateID {
		return true, nil
	}
	files := make(map[string]contracts.FileCard, len(bundle.Files))
	for _, file := range bundle.Files {
		files[file.FileID] = file
	}
	symbols := append([]indexingSymbol(nil), symbolsForProjection(bundle)...)
	sort.Slice(symbols, func(left, right int) bool { return symbols[left].ID < symbols[right].ID })
	for _, item := range symbols {
		file, found := files[item.Symbol.FileID]
		if !found {
			return false, fmt.Errorf("canonical symbol %s references missing file", item.Symbol.SymbolID)
		}
		buildSource := BuildSymbolSource
		if item.Symbol.Test {
			buildSource = BuildTestSource
		}
		source, err := buildSource(bundle.Snapshot, file, item.Symbol, "canonical", nil)
		if err != nil {
			return false, err
		}
		point, err := BuildPoint(ctx, source, worker.Generation, worker.Embedder, worker.Sparse)
		if err != nil {
			return false, err
		}
		if err := worker.Writer.UpsertPoint(ctx, point); err != nil {
			return false, err
		}
		if err := worker.Recorder.RecordRetrievalProjectionPoint(ctx, point, delivery.OutboxID); err != nil {
			return false, fmt.Errorf("record retrieval point provenance: %w", err)
		}
	}
	return false, nil
}

func (worker ProjectionWorker) tombstoneSupersededSnapshot(ctx context.Context, delivery persistence.ProjectionDelivery) (bool, error) {
	if worker.Deleter == nil {
		return false, fmt.Errorf("retrieval projection deleter is required for snapshot tombstones")
	}
	var payload struct {
		Snapshot contracts.RepositorySnapshot `json:"snapshot"`
	}
	if err := json.Unmarshal(delivery.Payload, &payload); err != nil {
		return false, fmt.Errorf("decode superseded snapshot event: %w", err)
	}
	if err := contracts.ValidateRepositorySnapshot(payload.Snapshot); err != nil || payload.Snapshot.SnapshotID != delivery.AggregateID || payload.Snapshot.Status != "superseded" {
		return false, fmt.Errorf("superseded snapshot event is invalid")
	}
	pointIDs, err := worker.Recorder.TombstoneRetrievalProjectionPoints(ctx, worker.Generation, payload.Snapshot.SourceCommit, delivery.OutboxID)
	if err != nil {
		return false, fmt.Errorf("tombstone canonical retrieval points: %w", err)
	}
	if len(pointIDs) == 0 {
		return false, nil
	}
	if err := worker.Deleter.DeletePoints(ctx, pointIDs); err != nil {
		return false, err
	}
	return false, nil
}

type indexingSymbol struct {
	ID     string
	Symbol contracts.SymbolCard
}

func symbolsForProjection(bundle indexing.IndexBundle) []indexingSymbol {
	result := make([]indexingSymbol, 0, len(bundle.Symbols))
	for _, symbol := range bundle.Symbols {
		result = append(result, indexingSymbol{ID: symbol.SymbolID, Symbol: symbol})
	}
	return result
}

func (worker ProjectionWorker) validate() error {
	if worker.Deliveries == nil || worker.Source == nil || worker.Recorder == nil || worker.Writer == nil || worker.Embedder == nil || worker.Sparse == nil {
		return fmt.Errorf("retrieval projection worker dependencies are required")
	}
	if worker.WorkerID == "" || worker.Generation == "" || worker.BatchSize < 1 || worker.BatchSize > 1000 || worker.ClaimTTL < time.Millisecond || worker.ClaimTTL > time.Hour || worker.MaxAttempts < 1 || worker.RetryDelay < 0 || worker.DeliveryTimeout < 0 || worker.DeliveryTimeout > 30*time.Second {
		return fmt.Errorf("retrieval projection worker configuration is invalid")
	}
	if worker.Embedder.Descriptor().SparseEncoderVersion != worker.Sparse.Version() {
		return fmt.Errorf("retrieval projection sparse encoder does not match embedding descriptor")
	}
	return nil
}

func (worker ProjectionWorker) deliveryTimeout() time.Duration {
	if worker.DeliveryTimeout == 0 {
		return 15 * time.Second
	}
	return worker.DeliveryTimeout
}

func (worker ProjectionWorker) projectorName() string {
	if worker.ProjectorName == "" {
		return DefaultQdrantProjectorName
	}
	return worker.ProjectorName
}

func (worker ProjectionWorker) now() time.Time {
	if worker.Now != nil {
		return worker.Now().UTC()
	}
	return time.Now().UTC()
}
