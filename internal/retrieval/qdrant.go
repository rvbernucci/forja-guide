package retrieval

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"path"
	"regexp"
	"sort"
	"strings"

	qdrant "github.com/qdrant/go-client/qdrant"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

const (
	DenseVectorName  = "dense"
	SparseVectorName = "sparse"
)

var qdrantCollectionNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,119}$`)

// QdrantCollectionPlan is a fully specified, versioned derived-index build.
// The database records the same generation before this plan is applied.
type QdrantCollectionPlan struct {
	Create       *qdrant.CreateCollection
	PayloadIndex []*qdrant.CreateFieldIndexCollection
}

// QdrantEndpoint is operator-supplied transport configuration. APIKey belongs
// in a secret boundary (environment or secret manager), never in a card,
// request contract, log line, or command argument.
type QdrantEndpoint struct {
	Host   string
	Port   int
	APIKey string
	UseTLS bool
}

// ClientConfig converts a validated endpoint to the official Qdrant Go client
// configuration. Non-loopback deployments require both TLS and an API key.
func (endpoint QdrantEndpoint) ClientConfig() (*qdrant.Config, error) {
	host := strings.TrimSpace(endpoint.Host)
	if host == "" || len(host) > 253 || endpoint.Port < 1 || endpoint.Port > 65535 {
		return nil, fmt.Errorf("Qdrant endpoint is invalid")
	}
	if !loopbackHost(host) && (!endpoint.UseTLS || strings.TrimSpace(endpoint.APIKey) == "") {
		return nil, fmt.Errorf("non-loopback Qdrant endpoint requires TLS and an API key")
	}
	return &qdrant.Config{
		Host: host, Port: endpoint.Port, APIKey: endpoint.APIKey, UseTLS: endpoint.UseTLS,
		PoolSize: 1, SkipCompatibilityCheck: false,
	}, nil
}

// OpenQdrant uses the official, version-pinned client after endpoint policy is
// validated. Callers retain ownership of Close and must bound every request.
func OpenQdrant(endpoint QdrantEndpoint) (*qdrant.Client, error) {
	config, err := endpoint.ClientConfig()
	if err != nil {
		return nil, err
	}
	client, err := qdrant.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("open Qdrant client: %w", err)
	}
	return client, nil
}

// BuildQdrantCollectionPlan produces a strict collection: every field used by
// governed retrieval filters has an explicit payload index before query traffic.
func BuildQdrantCollectionPlan(collectionName string, dimensions int, generation string) (QdrantCollectionPlan, error) {
	if !qdrantCollectionNamePattern.MatchString(collectionName) || dimensions < 1 || dimensions > 4096 || generation == "" {
		return QdrantCollectionPlan{}, fmt.Errorf("invalid Qdrant collection plan")
	}
	enabled := true
	disabled := false
	limit := uint32(200)
	conditionLimit := uint64(32)
	indexLimit := uint64(32)
	create := &qdrant.CreateCollection{
		CollectionName: collectionName,
		VectorsConfig: qdrant.NewVectorsConfigMap(map[string]*qdrant.VectorParams{
			DenseVectorName: {Size: uint64(dimensions), Distance: qdrant.Distance_Cosine},
		}),
		SparseVectorsConfig: qdrant.NewSparseVectorsConfig(map[string]*qdrant.SparseVectorParams{
			SparseVectorName: {},
		}),
		StrictModeConfig: &qdrant.StrictModeConfig{
			Enabled:                    &enabled,
			MaxQueryLimit:              &limit,
			UnindexedFilteringRetrieve: &disabled,
			UnindexedFilteringUpdate:   &disabled,
			FilterMaxConditions:        &conditionLimit,
			MaxPayloadIndexCount:       &indexLimit,
		},
		Metadata: qdrant.NewValueMap(map[string]any{
			"forja_schema_version":  contracts.RetrievalSchemaVersion,
			"collection_generation": generation,
		}),
	}
	wait := true
	keywordFields := []string{
		"tenant_id", "repository_id", "collection_generation", "entity_id", "artifact_family",
		"source_commit", "source_hash", "status", "authority_class", "language", "symbol_kind",
		"repository_path", "path_scope",
	}
	indexes := make([]*qdrant.CreateFieldIndexCollection, 0, len(keywordFields)+1)
	for _, field := range keywordFields {
		fieldType := qdrant.FieldType_FieldTypeKeyword
		indexes = append(indexes, &qdrant.CreateFieldIndexCollection{
			CollectionName: collectionName, FieldName: field, FieldType: &fieldType, Wait: &wait,
		})
	}
	boolType := qdrant.FieldType_FieldTypeBool
	indexes = append(indexes, &qdrant.CreateFieldIndexCollection{
		CollectionName: collectionName, FieldName: "stale", FieldType: &boolType, Wait: &wait,
	})
	return QdrantCollectionPlan{Create: create, PayloadIndex: indexes}, nil
}

// QdrantPoint converts a fully validated derived point into Qdrant's wire
// contract. The UUID is only an index key; the canonical stable PointID stays
// in payload and is checked again during canonical resolution.
func QdrantPoint(point contracts.RetrievalPoint) (*qdrant.PointStruct, error) {
	if err := contracts.ValidateRetrievalPoint(point); err != nil {
		return nil, fmt.Errorf("validate retrieval point: %w", err)
	}
	dense, err := float32Values(point.Dense)
	if err != nil {
		return nil, err
	}
	sparse, err := float32Values(point.Sparse.Values)
	if err != nil {
		return nil, err
	}
	payload, err := qdrant.TryValueMap(pointPayload(point))
	if err != nil {
		return nil, fmt.Errorf("encode Qdrant payload: %w", err)
	}
	return &qdrant.PointStruct{
		Id: qdrant.NewID(pointUUID(point.PointID)), Payload: payload,
		Vectors: qdrant.NewVectorsMap(map[string]*qdrant.Vector{
			DenseVectorName:  qdrant.NewVectorDense(dense),
			SparseVectorName: qdrant.NewVectorSparse(point.Sparse.Indices, sparse),
		}),
	}, nil
}

// BuildQdrantQueryRequest emits a bounded dense or sparse request with every
// access constraint embedded in the vector-store filter before ranking.
func BuildQdrantQueryRequest(
	collectionName string,
	query contracts.RetrievalQuery,
	dense []float64,
	sparse contracts.SparseVector,
	mode string,
) (*qdrant.QueryPoints, error) {
	if !qdrantCollectionNamePattern.MatchString(collectionName) {
		return nil, fmt.Errorf("invalid Qdrant collection name")
	}
	if err := contracts.ValidateRetrievalQuery(query); err != nil {
		return nil, err
	}
	filter, err := qdrantFilter(query)
	if err != nil {
		return nil, err
	}
	limit := query.Policy.DenseLimit
	using := DenseVectorName
	var rankQuery *qdrant.Query
	switch mode {
	case DenseVectorName:
		values, err := float32Values(dense)
		if err != nil || len(values) == 0 {
			return nil, fmt.Errorf("invalid dense retrieval query: %w", err)
		}
		rankQuery = qdrant.NewQueryDense(values)
	case SparseVectorName:
		values, err := float32Values(sparse.Values)
		if err != nil || len(values) == 0 || len(values) != len(sparse.Indices) {
			return nil, fmt.Errorf("invalid sparse retrieval query: %w", err)
		}
		if !strictAscending(sparse.Indices) {
			return nil, fmt.Errorf("sparse retrieval indices must be strictly ascending")
		}
		limit = query.Policy.SparseLimit
		using = SparseVectorName
		rankQuery = qdrant.NewQuerySparse(sparse.Indices, values)
	default:
		return nil, fmt.Errorf("unsupported retrieval mode %q", mode)
	}
	return &qdrant.QueryPoints{
		CollectionName: collectionName,
		Query:          rankQuery,
		Using:          &using,
		Filter:         filter,
		Limit:          qdrant.PtrOf(uint64(limit)),
		WithPayload: qdrant.NewWithPayloadInclude(
			"point_id", "entity_id", "artifact_family", "source_commit", "source_hash",
			"status", "authority_class", "stale", "language", "symbol_kind", "repository_path",
			"proof_refs", "collection_generation",
		),
	}, nil
}

func pointPayload(point contracts.RetrievalPoint) map[string]any {
	payload := map[string]any{
		"point_id": point.PointID, "entity_id": point.EntityID,
		"artifact_family": point.ArtifactFamily, "source_hash": point.SourceHash,
		"card_text_hash": point.CardTextHash, "status": point.Status,
		"authority_class": point.AuthorityClass, "stale": point.Stale,
		"tenant_id": point.TenantID, "repository_id": point.RepositoryID,
		"collection_generation": point.CollectionGeneration,
		"proof_refs":            stringInterfaces(point.ProofRefs),
		"path_scope":            stringInterfaces(pathScopes(point.RepositoryPath)),
	}
	if point.SourceCommit != nil {
		payload["source_commit"] = *point.SourceCommit
	}
	if point.Language != nil {
		payload["language"] = *point.Language
	}
	if point.SymbolKind != nil {
		payload["symbol_kind"] = *point.SymbolKind
	}
	if point.RepositoryPath != nil {
		payload["repository_path"] = *point.RepositoryPath
	}
	return payload
}

func qdrantFilter(query contracts.RetrievalQuery) (*qdrant.Filter, error) {
	must := []*qdrant.Condition{
		qdrant.NewMatchKeyword("tenant_id", query.TenantID),
		qdrant.NewMatchKeyword("repository_id", query.RepositoryID),
		qdrant.NewMatchKeyword("source_commit", query.Scope.SourceCommit),
		qdrant.NewMatchKeyword("status", "active"),
		qdrant.NewMatchBool("stale", false),
		qdrant.NewMatchKeywords("artifact_family", query.Filters.ArtifactFamilies...),
		qdrant.NewMatchKeywords("authority_class", query.Filters.AuthorityClasses...),
		qdrant.NewMatchKeywords("path_scope", normalizeScopePaths(query.Scope.AllowedPaths)...),
	}
	if query.ExpectedGeneration != nil {
		must = append(must, qdrant.NewMatchKeyword("collection_generation", *query.ExpectedGeneration))
	}
	if len(query.Filters.Languages) > 0 {
		must = append(must, qdrant.NewMatchKeywords("language", query.Filters.Languages...))
	}
	if len(query.Filters.SymbolKinds) > 0 {
		must = append(must, qdrant.NewMatchKeywords("symbol_kind", query.Filters.SymbolKinds...))
	}
	filter := &qdrant.Filter{Must: must}
	if len(query.Scope.DeniedPaths) > 0 {
		filter.MustNot = []*qdrant.Condition{
			qdrant.NewMatchKeywords("path_scope", normalizeScopePaths(query.Scope.DeniedPaths)...),
		}
	}
	return filter, nil
}

func normalizeScopePaths(paths []string) []string {
	result := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, value := range paths {
		value = strings.TrimSuffix(value, "/**")
		if value == "" || value == "**" {
			value = "**"
		}
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func pathScopes(repositoryPath *string) []string {
	if repositoryPath == nil {
		return []string{"**"}
	}
	clean := path.Clean(*repositoryPath)
	if clean == "." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return []string{"**"}
	}
	result := []string{"**"}
	parts := strings.Split(clean, "/")
	for index := range parts {
		result = append(result, strings.Join(parts[:index+1], "/"))
	}
	return result
}

func pointUUID(pointID string) string {
	digest := sha256.Sum256([]byte("forja-qdrant-point-v1\x00" + pointID))
	bytes := digest[:16]
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(bytes)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func float32Values(values []float64) ([]float32, error) {
	converted := make([]float32, len(values))
	for index, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value > math.MaxFloat32 || value < -math.MaxFloat32 {
			return nil, fmt.Errorf("vector value at index %d cannot be represented as float32", index)
		}
		converted[index] = float32(value)
	}
	return converted, nil
}

func strictAscending(values []uint32) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] >= values[index] {
			return false
		}
	}
	return true
}

func stringInterfaces(values []string) []any {
	result := make([]any, len(values))
	for index := range values {
		result[index] = values[index]
	}
	return result
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
