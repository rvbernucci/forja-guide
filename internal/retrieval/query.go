package retrieval

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	qdrant "github.com/qdrant/go-client/qdrant"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/observability"
)

// QdrantQueryClient is the official client surface used for governed candidate
// discovery. Returned payloads remain untrusted until CanonicalResolver
// reauthorizes every fused candidate against PostgreSQL.
type QdrantQueryClient interface {
	Query(context.Context, *qdrant.QueryPoints) ([]*qdrant.ScoredPoint, error)
}

// ProjectionFreshnessReader reports bounded aggregate lag for the exact
// derived projector that backs retrieval. It never returns query text, vector
// values, entity identities, or payloads.
type ProjectionFreshnessReader interface {
	RetrievalProjectionLag(context.Context) (int64, error)
}

// QueryService executes bounded hybrid discovery and constructs a validated
// result receipt. It never returns a Qdrant payload directly as context.
type QueryService struct {
	Client         QdrantQueryClient
	CollectionName string
	Embedder       Embedder
	Sparse         SparseEncoder
	Resolver       CanonicalResolver
	Freshness      ProjectionFreshnessReader
	Observer       *observability.Observer
	QueryTimeout   time.Duration
}

func (service QueryService) Search(ctx context.Context, query contracts.RetrievalQuery) (result contracts.RetrievalResult, err error) {
	if service.Observer != nil {
		observedCtx, operation := service.Observer.Start(ctx, observability.BoundaryRetrieval, observability.OperationQuery)
		ctx = observedCtx
		defer func() { operation.End(err) }()
	}
	if err := contracts.ValidateRetrievalQuery(query); err != nil {
		return contracts.RetrievalResult{}, err
	}
	if service.Client == nil || service.Embedder == nil || service.Resolver == nil ||
		(query.Policy.SparseWeight > 0 && service.Sparse == nil) || !qdrantCollectionNamePattern.MatchString(service.CollectionName) {
		return contracts.RetrievalResult{}, fmt.Errorf("governed retrieval service is not configured")
	}
	if service.QueryTimeout < 0 || service.QueryTimeout > 30*time.Second {
		return contracts.RetrievalResult{}, fmt.Errorf("governed retrieval query timeout is invalid")
	}
	queryContext, cancel := context.WithTimeout(ctx, service.queryTimeout())
	defer cancel()
	ctx = queryContext
	descriptor := service.Embedder.Descriptor()
	if (query.Policy.SparseWeight > 0 && descriptor.SparseEncoderVersion != service.Sparse.Version()) ||
		query.ExpectedGeneration == nil || *query.ExpectedGeneration != contracts.RetrievalGenerationID(descriptor.Model, descriptor.Version, descriptor.Dimensions, descriptor.SparseEncoderVersion) {
		return contracts.RetrievalResult{}, fmt.Errorf("retrieval query does not bind the configured embedding generation")
	}
	projectionLag := int64(0)
	if service.Freshness != nil {
		var freshnessErr error
		projectionLag, freshnessErr = service.Freshness.RetrievalProjectionLag(ctx)
		if freshnessErr != nil {
			result = degradedResult(query, "projection_freshness_unavailable", "unknown", 0)
			service.recordQueryStats(ctx, result)
			return result, nil
		}
		if projectionLag > 0 {
			result = degradedResult(query, "projection_lag", "stale", projectionLag)
			service.recordQueryStats(ctx, result)
			return result, nil
		}
	}
	var denseCandidates, sparseCandidates []contracts.RetrievalCandidate
	if query.Policy.DenseWeight > 0 {
		dense, embedErr := service.Embedder.Embed(ctx, query.Query)
		if embedErr != nil || !validDenseQuery(dense, descriptor.Dimensions) {
			if embedErr == nil {
				embedErr = fmt.Errorf("embedding dimensions or values are invalid")
			}
			return contracts.RetrievalResult{}, fmt.Errorf("embed retrieval query: %w", embedErr)
		}
		denseRequest, buildErr := BuildQdrantQueryRequest(service.CollectionName, query, dense, contracts.SparseVector{}, DenseVectorName)
		if buildErr != nil {
			return contracts.RetrievalResult{}, buildErr
		}
		densePoints, queryErr := service.Client.Query(ctx, denseRequest)
		if queryErr != nil {
			result = degradedResult(query, "qdrant_dense_unavailable", "unknown", projectionLag)
			service.recordQueryStats(ctx, result)
			return result, nil
		}
		denseCandidates = qdrantCandidates(densePoints, query.Policy.DenseLimit)
	}
	if query.Policy.SparseWeight > 0 {
		sparse, sparseErr := service.Sparse.Encode(query.Query)
		if sparseErr != nil {
			return contracts.RetrievalResult{}, fmt.Errorf("encode sparse retrieval query: %w", sparseErr)
		}
		sparseRequest, buildErr := BuildQdrantQueryRequest(service.CollectionName, query, nil, sparse, SparseVectorName)
		if buildErr != nil {
			return contracts.RetrievalResult{}, buildErr
		}
		sparsePoints, queryErr := service.Client.Query(ctx, sparseRequest)
		if queryErr != nil {
			result = degradedResult(query, "qdrant_sparse_unavailable", "unknown", projectionLag)
			service.recordQueryStats(ctx, result)
			return result, nil
		}
		sparseCandidates = qdrantCandidates(sparsePoints, query.Policy.SparseLimit)
	}
	fused, malformed := fuseCandidateRanks(denseCandidates, sparseCandidates, query.Policy)
	accepted, rejected, ambiguities, err := ResolveRankedCandidates(ctx, query, fused, service.Resolver)
	if err != nil {
		result = degradedResult(query, "canonical_resolver_unavailable", "unknown", projectionLag)
		service.recordQueryStats(ctx, result)
		return result, nil
	}
	rejected = append(rejected, malformed...)
	result = contracts.RetrievalResult{
		RequestID: query.RequestID, SchemaVersion: contracts.RetrievalSchemaVersion,
		Status: "complete", ProjectionFreshness: "fresh", ProjectionLagEvents: projectionLag, CollectionGeneration: query.ExpectedGeneration,
		Accepted: accepted, Rejections: rejected,
		Ambiguities: ambiguities,
		Receipt:     receipt(query, len(denseCandidates), len(sparseCandidates), len(fused), len(accepted), len(rejected)),
	}
	if err := contracts.ValidateRetrievalResult(query, result); err != nil {
		return contracts.RetrievalResult{}, fmt.Errorf("validate governed retrieval result: %w", err)
	}
	service.recordQueryStats(ctx, result)
	return result, nil
}

func (service QueryService) queryTimeout() time.Duration {
	if service.QueryTimeout == 0 {
		return 5 * time.Second
	}
	return service.QueryTimeout
}

func degradedResult(query contracts.RetrievalQuery, gap, freshness string, projectionLag int64) contracts.RetrievalResult {
	return contracts.RetrievalResult{
		RequestID: query.RequestID, SchemaVersion: contracts.RetrievalSchemaVersion,
		Status: "degraded", ProjectionFreshness: freshness, ProjectionLagEvents: projectionLag, CollectionGeneration: query.ExpectedGeneration,
		Accepted: []contracts.RetrievalCandidate{}, Rejections: []contracts.RetrievalRejection{}, Gaps: []string{gap},
		Receipt: receipt(query, 0, 0, 0, 0, 0),
	}
}

func (service QueryService) recordQueryStats(ctx context.Context, result contracts.RetrievalResult) {
	if service.Observer == nil {
		return
	}
	service.Observer.RecordRetrievalStats(ctx, observability.RetrievalStats{
		DenseCandidates: result.Receipt.DenseCandidates, SparseCandidates: result.Receipt.SparseCandidates,
		FusedCandidates: result.Receipt.FusedCandidates, Accepted: result.Receipt.ResolvedCandidates,
		Rejected: result.Receipt.RejectedCandidates, Degraded: result.Status == "degraded",
	})
}

func receipt(query contracts.RetrievalQuery, dense, sparse, fused, resolved, rejected int) contracts.RetrievalReceipt {
	hash, _ := contracts.RetrievalPolicyHash(query.Policy)
	return contracts.RetrievalReceipt{DenseCandidates: dense, SparseCandidates: sparse, FusedCandidates: fused, ResolvedCandidates: resolved, RejectedCandidates: rejected, PolicyHash: hash}
}

func validDenseQuery(values []float64, dimensions int) bool {
	if dimensions < 1 || len(values) != dimensions {
		return false
	}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func qdrantCandidates(points []*qdrant.ScoredPoint, limit int) []contracts.RetrievalCandidate {
	result := make([]contracts.RetrievalCandidate, 0, min(len(points), limit))
	for _, point := range points {
		if len(result) == limit {
			break
		}
		if point == nil {
			continue
		}
		payload := point.GetPayload()
		candidate := contracts.RetrievalCandidate{
			PointID: payloadString(payload, "point_id"), EntityID: payloadString(payload, "entity_id"),
			ArtifactFamily: payloadString(payload, "artifact_family"), SourceHash: payloadString(payload, "source_hash"),
			AuthorityClass: payloadString(payload, "authority_class"), ProofRefs: payloadStrings(payload, "proof_refs"),
		}
		if value, found := optionalPayloadString(payload, "source_commit"); found {
			candidate.SourceCommit = &value
		}
		if value, found := optionalPayloadString(payload, "repository_path"); found {
			candidate.RepositoryPath = &value
		}
		result = append(result, candidate)
	}
	return result
}

func fuseCandidateRanks(dense, sparse []contracts.RetrievalCandidate, policy contracts.RetrievalPolicy) ([]contracts.RetrievalCandidate, []contracts.RetrievalRejection) {
	type scored struct{ candidate contracts.RetrievalCandidate }
	entries := make(map[string]*scored, len(dense)+len(sparse))
	rejections := []contracts.RetrievalRejection{}
	apply := func(values []contracts.RetrievalCandidate, weight float64, isDense bool) {
		if weight == 0 {
			return
		}
		seen := make(map[string]struct{}, len(values))
		for index, candidate := range values {
			if candidate.PointID == "" {
				continue
			}
			if _, exists := seen[candidate.PointID]; exists {
				rejections = append(rejections, contracts.RetrievalRejection{PointID: candidate.PointID, Reason: "malformed_candidate"})
				continue
			}
			seen[candidate.PointID] = struct{}{}
			rank := index + 1
			entry, exists := entries[candidate.PointID]
			if !exists {
				entry = &scored{candidate: candidate}
				entries[candidate.PointID] = entry
			} else if entry.candidate.EntityID != candidate.EntityID || entry.candidate.SourceHash != candidate.SourceHash || entry.candidate.ArtifactFamily != candidate.ArtifactFamily {
				rejections = append(rejections, contracts.RetrievalRejection{PointID: candidate.PointID, Reason: "malformed_candidate"})
				delete(entries, candidate.PointID)
				continue
			}
			entry.candidate.FusedScore += weight / float64(policy.RRFK+rank)
			if isDense {
				entry.candidate.DenseRank = &rank
			} else {
				entry.candidate.SparseRank = &rank
			}
		}
	}
	apply(dense, policy.DenseWeight, true)
	apply(sparse, policy.SparseWeight, false)
	result := make([]contracts.RetrievalCandidate, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.candidate)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].FusedScore != result[right].FusedScore {
			return result[left].FusedScore > result[right].FusedScore
		}
		return result[left].PointID < result[right].PointID
	})
	if len(result) > policy.Limit {
		result = result[:policy.Limit]
	}
	return result, rejections
}

func payloadString(payload map[string]*qdrant.Value, field string) string {
	value, _ := optionalPayloadString(payload, field)
	return value
}

func optionalPayloadString(payload map[string]*qdrant.Value, field string) (string, bool) {
	value, found := payload[field]
	if !found || value == nil || value.GetStringValue() == "" {
		return "", false
	}
	return value.GetStringValue(), true
}

func payloadStrings(payload map[string]*qdrant.Value, field string) []string {
	value, found := payload[field]
	if !found || value == nil || value.GetListValue() == nil {
		return []string{}
	}
	values := value.GetListValue().GetValues()
	result := make([]string, 0, len(values))
	for _, item := range values {
		if item == nil || item.GetStringValue() == "" {
			return []string{}
		}
		result = append(result, item.GetStringValue())
	}
	return result
}
