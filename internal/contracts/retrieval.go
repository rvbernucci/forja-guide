package contracts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

const RetrievalSchemaVersion = "1.0"

var (
	retrievalPointIDPattern      = regexp.MustCompile(`^retrieval_[a-f0-9]{64}$`)
	retrievalGenerationIDPattern = regexp.MustCompile(`^retrieval_generation_[a-f0-9]{64}$`)
	retrievalRequestIDPattern    = regexp.MustCompile(`^retrieval_request_[A-Za-z0-9_-]{1,200}$`)
	retrievalEntityIDPattern     = regexp.MustCompile(`^(?:symbol|file|artifact|memory|decision|test|incident)_[A-Za-z0-9_-]{1,200}$`)
	retrievalTenantIDPattern     = regexp.MustCompile(`^tenant_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	retrievalRepositoryIDPattern = regexp.MustCompile(`^repo_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	retrievalContentHashPattern  = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	retrievalSourceCommitPattern = regexp.MustCompile(`^[a-f0-9]{40,64}$`)
	retrievalAuthorityClasses    = map[string]struct{}{"canonical": {}, "supporting": {}, "candidate": {}}
	retrievalArtifactFamilies    = map[string]struct{}{"symbol": {}, "decision": {}, "test": {}, "memory": {}, "incident": {}}
	retrievalProjectionFreshness = map[string]struct{}{"fresh": {}, "stale": {}, "unknown": {}}
	retrievalResultStatuses      = map[string]struct{}{"complete": {}, "degraded": {}, "blocked": {}}
	retrievalPointStatuses       = map[string]struct{}{"active": {}, "superseded": {}, "tombstoned": {}}
)

// SparseVector is the deterministic lexical representation of one retrieval card.
type SparseVector struct {
	Indices []uint32  `json:"indices"`
	Values  []float64 `json:"values"`
}

// EmbeddingDescriptor pins the provider and vector contract used for a point.
type EmbeddingDescriptor struct {
	Model                string    `json:"model"`
	Version              string    `json:"version"`
	Dimensions           int       `json:"dimensions"`
	SparseEncoderVersion string    `json:"sparse_encoder_version"`
	EmbeddedAt           time.Time `json:"embedded_at"`
}

// RetrievalPoint is the complete, derived point contract before Qdrant upsert.
type RetrievalPoint struct {
	PointID              string              `json:"point_id"`
	SchemaVersion        string              `json:"schema_version"`
	CollectionGeneration string              `json:"collection_generation"`
	TenantID             string              `json:"tenant_id"`
	RepositoryID         string              `json:"repository_id"`
	EntityID             string              `json:"entity_id"`
	ArtifactFamily       string              `json:"artifact_family"`
	SourceCommit         *string             `json:"source_commit,omitempty"`
	SourceHash           string              `json:"source_hash"`
	CardText             string              `json:"card_text"`
	CardTextHash         string              `json:"card_text_hash"`
	Status               string              `json:"status"`
	AuthorityClass       string              `json:"authority_class"`
	Stale                bool                `json:"stale"`
	Language             *string             `json:"language,omitempty"`
	SymbolKind           *string             `json:"symbol_kind,omitempty"`
	RepositoryPath       *string             `json:"repository_path,omitempty"`
	ProofRefs            []string            `json:"proof_refs"`
	GraphNodeIDs         []string            `json:"graph_node_ids"`
	Dense                []float64           `json:"dense"`
	Sparse               SparseVector        `json:"sparse"`
	Embedding            EmbeddingDescriptor `json:"embedding"`
}

// RetrievalScope is the source boundary enforced before approximate search.
type RetrievalScope struct {
	SourceCommit string   `json:"source_commit"`
	AllowedPaths []string `json:"allowed_paths"`
	DeniedPaths  []string `json:"denied_paths,omitempty"`
}

// RetrievalFilters are trusted-code constraints that must be sent to both ranks.
type RetrievalFilters struct {
	ArtifactFamilies []string `json:"artifact_families"`
	AuthorityClasses []string `json:"authority_classes"`
	Languages        []string `json:"languages,omitempty"`
	SymbolKinds      []string `json:"symbol_kinds,omitempty"`
}

// RetrievalPolicy provides bounded, explainable weighted reciprocal-rank fusion.
type RetrievalPolicy struct {
	Limit        int     `json:"limit"`
	DenseLimit   int     `json:"dense_limit"`
	SparseLimit  int     `json:"sparse_limit"`
	DenseWeight  float64 `json:"dense_weight"`
	SparseWeight float64 `json:"sparse_weight"`
	RRFK         int     `json:"rrf_k"`
}

// RetrievalQuery is the public request contract before embeddings are generated.
type RetrievalQuery struct {
	RequestID          string           `json:"request_id"`
	SchemaVersion      string           `json:"schema_version"`
	TenantID           string           `json:"tenant_id"`
	RepositoryID       string           `json:"repository_id"`
	Query              string           `json:"query"`
	Scope              RetrievalScope   `json:"scope"`
	Filters            RetrievalFilters `json:"filters"`
	Policy             RetrievalPolicy  `json:"policy"`
	ExpectedGeneration *string          `json:"expected_generation,omitempty"`
}

// RetrievalCandidate records exactly how one canonically resolved point ranked.
type RetrievalCandidate struct {
	PointID        string   `json:"point_id"`
	EntityID       string   `json:"entity_id"`
	ArtifactFamily string   `json:"artifact_family"`
	SourceCommit   *string  `json:"source_commit,omitempty"`
	SourceHash     string   `json:"source_hash"`
	AuthorityClass string   `json:"authority_class"`
	RepositoryPath *string  `json:"repository_path,omitempty"`
	DenseRank      *int     `json:"dense_rank,omitempty"`
	SparseRank     *int     `json:"sparse_rank,omitempty"`
	FusedScore     float64  `json:"fused_score"`
	ProofRefs      []string `json:"proof_refs"`
}

// RetrievalRejection exposes bounded, non-content reasons for discarded candidates.
type RetrievalRejection struct {
	PointID string `json:"point_id"`
	Reason  string `json:"reason"`
}

// RetrievalAmbiguity exposes bounded alternative canonical identities without
// treating any ambiguous result as authorized context. It contains no card
// text, path, source hash, provider payload, or similarity score.
type RetrievalAmbiguity struct {
	PointID              string   `json:"point_id"`
	AlternativeEntityIDs []string `json:"alternative_entity_ids"`
}

// RetrievalReceipt keeps retrieval policy effects auditable without query bodies.
type RetrievalReceipt struct {
	DenseCandidates    int    `json:"dense_candidates"`
	SparseCandidates   int    `json:"sparse_candidates"`
	FusedCandidates    int    `json:"fused_candidates"`
	ResolvedCandidates int    `json:"resolved_candidates"`
	RejectedCandidates int    `json:"rejected_candidates"`
	PolicyHash         string `json:"policy_hash"`
}

// RetrievalResult is safe to pass to later context assembly after resolution.
type RetrievalResult struct {
	RequestID            string               `json:"request_id"`
	SchemaVersion        string               `json:"schema_version"`
	Status               string               `json:"status"`
	ProjectionFreshness  string               `json:"projection_freshness"`
	CollectionGeneration *string              `json:"collection_generation,omitempty"`
	Accepted             []RetrievalCandidate `json:"accepted"`
	Rejections           []RetrievalRejection `json:"rejections"`
	Ambiguities          []RetrievalAmbiguity `json:"ambiguities,omitempty"`
	Gaps                 []string             `json:"gaps,omitempty"`
	Receipt              RetrievalReceipt     `json:"receipt"`
}

// StableRetrievalID derives IDs from canonical components, never from scores or time.
func StableRetrievalID(prefix string, components ...string) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("forja-retrieval-id-v1\x00"))
	_, _ = digest.Write([]byte(prefix))
	for _, component := range components {
		_, _ = digest.Write([]byte("\x00" + component))
	}
	return prefix + "_" + hex.EncodeToString(digest.Sum(nil))
}

// RetrievalGenerationID identifies one schema and embedding-compatible collection build.
func RetrievalGenerationID(model, version string, dimensions int, sparseVersion string) string {
	return StableRetrievalID(
		"retrieval_generation",
		RetrievalSchemaVersion,
		strings.TrimSpace(model),
		strings.TrimSpace(version),
		fmt.Sprintf("%d", dimensions),
		strings.TrimSpace(sparseVersion),
	)
}

// IsRetrievalGenerationID reports whether a value has the stable public form
// for a retrieval collection generation. It does not establish that the
// generation is registered, active, or backed by a verified physical store.
func IsRetrievalGenerationID(value string) bool {
	return retrievalGenerationIDPattern.MatchString(value)
}

// RetrievalPointID binds a projected point to its generation, canonical entity, and source bytes.
func RetrievalPointID(generation, entityID, sourceHash string) string {
	return StableRetrievalID("retrieval", generation, entityID, sourceHash)
}

// IsRetrievalPointID reports whether a stable retrieval point identifier has
// the public structural form required for derived-store deletion requests.
// It does not establish canonical authority; callers must still resolve it.
func IsRetrievalPointID(value string) bool {
	return retrievalPointIDPattern.MatchString(value)
}

// IsRetrievalEntityID reports the public structural form of a canonical
// retrieval entity identity. Authority still requires canonical resolution.
func IsRetrievalEntityID(value string) bool {
	return retrievalEntityIDPattern.MatchString(value)
}

// RetrievalFamilyRequiresSourceCommit reports whether an entity is derived
// from a specific repository snapshot. Decisions, memories, and incidents are
// repository-global canonical records and deliberately have no source commit.
func RetrievalFamilyRequiresSourceCommit(family string) bool {
	return family == "symbol" || family == "test"
}

// RetrievalScopeCoversRepository reports whether a scope can safely include a
// repository-global entity. A global card has no path-level provenance, so an
// allow-all scope with any denied path is still too narrow to authorize it.
func RetrievalScopeCoversRepository(scope RetrievalScope) bool {
	return slices.Contains(scope.AllowedPaths, "**") && len(scope.DeniedPaths) == 0
}

// CardTextHash returns the required content hash for a deterministic retrieval card.
func CardTextHash(text string) string {
	digest := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(digest[:])
}

// RetrievalPolicyHash identifies the exact fusion policy without exposing query text.
func RetrievalPolicyHash(policy RetrievalPolicy) (string, error) {
	if err := validateRetrievalPolicy(policy); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return "", fmt.Errorf("marshal retrieval policy: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// ValidateRetrievalPoint rejects malformed provider output before it reaches Qdrant.
func ValidateRetrievalPoint(point RetrievalPoint) error {
	if point.SchemaVersion != RetrievalSchemaVersion {
		return fmt.Errorf("retrieval point schema version must be %q", RetrievalSchemaVersion)
	}
	if !retrievalPointIDPattern.MatchString(point.PointID) ||
		!retrievalGenerationIDPattern.MatchString(point.CollectionGeneration) ||
		!retrievalTenantIDPattern.MatchString(point.TenantID) ||
		!retrievalRepositoryIDPattern.MatchString(point.RepositoryID) {
		return fmt.Errorf("retrieval point has invalid stable identifiers")
	}
	if !retrievalEntityIDPattern.MatchString(point.EntityID) {
		return fmt.Errorf("retrieval point identity is invalid")
	}
	if _, ok := retrievalArtifactFamilies[point.ArtifactFamily]; !ok {
		return fmt.Errorf("retrieval point artifact family is invalid")
	}
	if RetrievalFamilyRequiresSourceCommit(point.ArtifactFamily) && point.SourceCommit == nil {
		return fmt.Errorf("source-bound retrieval point requires a source commit")
	}
	if !RetrievalFamilyRequiresSourceCommit(point.ArtifactFamily) && point.SourceCommit != nil {
		return fmt.Errorf("repository-global retrieval point must not carry a source commit")
	}
	if point.SourceCommit != nil && !retrievalSourceCommitPattern.MatchString(*point.SourceCommit) {
		return fmt.Errorf("retrieval point source commit is invalid")
	}
	if !retrievalContentHashPattern.MatchString(point.SourceHash) ||
		!retrievalContentHashPattern.MatchString(point.CardTextHash) ||
		point.CardTextHash != CardTextHash(point.CardText) {
		return fmt.Errorf("retrieval point card hash is invalid")
	}
	if len(point.CardText) == 0 || len(point.CardText) > 32768 {
		return fmt.Errorf("retrieval point card text is out of bounds")
	}
	if _, ok := retrievalPointStatuses[point.Status]; !ok {
		return fmt.Errorf("retrieval point status is invalid")
	}
	if _, ok := retrievalAuthorityClasses[point.AuthorityClass]; !ok {
		return fmt.Errorf("retrieval point authority class is invalid")
	}
	if err := validateOptionalBoundedString(point.Language, 80, "language"); err != nil {
		return err
	}
	if err := validateOptionalBoundedString(point.SymbolKind, 80, "symbol kind"); err != nil {
		return err
	}
	if err := validateOptionalRepositoryPath(point.RepositoryPath); err != nil {
		return err
	}
	if err := validateUniqueBoundedStrings(point.ProofRefs, 256, 512, "proof references"); err != nil {
		return err
	}
	if err := validateUniqueBoundedStrings(point.GraphNodeIDs, 256, 256, "graph node IDs"); err != nil {
		return err
	}
	if err := validateEmbedding(point.Dense, point.Sparse, point.Embedding); err != nil {
		return err
	}
	expected := RetrievalPointID(point.CollectionGeneration, point.EntityID, point.SourceHash)
	if point.PointID != expected {
		return fmt.Errorf("retrieval point ID does not bind generation, entity, and source hash")
	}
	return nil
}

// ValidateRetrievalQuery validates caller input before providers or Qdrant receive it.
func ValidateRetrievalQuery(query RetrievalQuery) error {
	if query.SchemaVersion != RetrievalSchemaVersion || !retrievalRequestIDPattern.MatchString(query.RequestID) {
		return fmt.Errorf("retrieval query identity is invalid")
	}
	if !retrievalTenantIDPattern.MatchString(query.TenantID) ||
		!retrievalRepositoryIDPattern.MatchString(query.RepositoryID) ||
		strings.TrimSpace(query.Query) == "" || len(query.Query) > 16384 {
		return fmt.Errorf("retrieval query scope or text is invalid")
	}
	if !retrievalSourceCommitPattern.MatchString(query.Scope.SourceCommit) {
		return fmt.Errorf("retrieval query source commit is invalid")
	}
	if err := validateScopePaths(query.Scope); err != nil {
		return err
	}
	if err := validateRetrievalFilters(query.Filters); err != nil {
		return err
	}
	if retrievalFiltersIncludeRepositoryGlobalFamily(query.Filters) && !RetrievalScopeCoversRepository(query.Scope) {
		return fmt.Errorf("repository-global retrieval requires an unrestricted repository scope")
	}
	if err := validateRetrievalPolicy(query.Policy); err != nil {
		return err
	}
	if query.ExpectedGeneration != nil && !retrievalGenerationIDPattern.MatchString(*query.ExpectedGeneration) {
		return fmt.Errorf("retrieval query expected generation is invalid")
	}
	return nil
}

// ValidateRetrievalResult confirms a resolved result remains within the request contract.
func ValidateRetrievalResult(query RetrievalQuery, result RetrievalResult) error {
	if err := ValidateRetrievalQuery(query); err != nil {
		return fmt.Errorf("validate retrieval query: %w", err)
	}
	if result.SchemaVersion != RetrievalSchemaVersion || result.RequestID != query.RequestID {
		return fmt.Errorf("retrieval result does not bind the request")
	}
	if _, ok := retrievalResultStatuses[result.Status]; !ok {
		return fmt.Errorf("retrieval result status is invalid")
	}
	if _, ok := retrievalProjectionFreshness[result.ProjectionFreshness]; !ok {
		return fmt.Errorf("retrieval projection freshness is invalid")
	}
	if result.CollectionGeneration != nil && !retrievalGenerationIDPattern.MatchString(*result.CollectionGeneration) {
		return fmt.Errorf("retrieval result collection generation is invalid")
	}
	if len(result.Accepted) > query.Policy.Limit || len(result.Rejections) > 400 || len(result.Ambiguities) > 400 || len(result.Gaps) > 64 {
		return fmt.Errorf("retrieval result exceeds bounded output")
	}
	if result.Receipt.DenseCandidates > query.Policy.DenseLimit ||
		result.Receipt.SparseCandidates > query.Policy.SparseLimit ||
		result.Receipt.FusedCandidates > query.Policy.DenseLimit+query.Policy.SparseLimit ||
		result.Receipt.ResolvedCandidates != len(result.Accepted) ||
		result.Receipt.RejectedCandidates != len(result.Rejections) {
		return fmt.Errorf("retrieval receipt counts are inconsistent")
	}
	policyHash, err := RetrievalPolicyHash(query.Policy)
	if err != nil || result.Receipt.PolicyHash != policyHash {
		return fmt.Errorf("retrieval receipt policy hash is invalid")
	}
	seenEntities := make(map[string]struct{}, len(result.Accepted))
	for _, candidate := range result.Accepted {
		if err := validateRetrievalCandidate(candidate); err != nil {
			return err
		}
		if _, exists := seenEntities[candidate.EntityID]; exists {
			return fmt.Errorf("retrieval result contains duplicate entity %q", candidate.EntityID)
		}
		seenEntities[candidate.EntityID] = struct{}{}
	}
	ambiguousRejections := make(map[string]struct{}, len(result.Rejections))
	for _, rejection := range result.Rejections {
		if !retrievalPointIDPattern.MatchString(rejection.PointID) || !validRejectionReason(rejection.Reason) {
			return fmt.Errorf("retrieval rejection is invalid")
		}
		if rejection.Reason == "ambiguous_identity" {
			ambiguousRejections[rejection.PointID] = struct{}{}
		}
	}
	seenAmbiguities := make(map[string]struct{}, len(result.Ambiguities))
	for _, ambiguity := range result.Ambiguities {
		if !retrievalPointIDPattern.MatchString(ambiguity.PointID) || len(ambiguity.AlternativeEntityIDs) < 2 || len(ambiguity.AlternativeEntityIDs) > 16 || !sort.StringsAreSorted(ambiguity.AlternativeEntityIDs) || !uniqueRetrievalEntityIDs(ambiguity.AlternativeEntityIDs) {
			return fmt.Errorf("retrieval ambiguity is invalid")
		}
		if _, found := ambiguousRejections[ambiguity.PointID]; !found {
			return fmt.Errorf("retrieval ambiguity lacks an ambiguous rejection")
		}
		if _, found := seenAmbiguities[ambiguity.PointID]; found {
			return fmt.Errorf("retrieval ambiguity duplicates a point")
		}
		seenAmbiguities[ambiguity.PointID] = struct{}{}
	}
	if err := validateUniqueBoundedStrings(result.Gaps, 64, 512, "retrieval gaps"); err != nil {
		return err
	}
	return nil
}

func uniqueRetrievalEntityIDs(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !retrievalEntityIDPattern.MatchString(value) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validateEmbedding(dense []float64, sparse SparseVector, descriptor EmbeddingDescriptor) error {
	if strings.TrimSpace(descriptor.Model) == "" || len(descriptor.Model) > 200 ||
		strings.TrimSpace(descriptor.Version) == "" || len(descriptor.Version) > 160 ||
		strings.TrimSpace(descriptor.SparseEncoderVersion) == "" || len(descriptor.SparseEncoderVersion) > 160 ||
		descriptor.Dimensions < 1 || descriptor.Dimensions > 4096 || descriptor.EmbeddedAt.IsZero() {
		return fmt.Errorf("embedding descriptor is invalid")
	}
	if len(dense) != descriptor.Dimensions {
		return fmt.Errorf("dense vector dimensions do not match descriptor")
	}
	for _, value := range dense {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("dense vector contains a non-finite value")
		}
	}
	if len(sparse.Indices) == 0 || len(sparse.Indices) != len(sparse.Values) || len(sparse.Indices) > 8192 {
		return fmt.Errorf("sparse vector shape is invalid")
	}
	for index, value := range sparse.Values {
		if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("sparse vector contains an invalid value")
		}
		if index > 0 && sparse.Indices[index-1] >= sparse.Indices[index] {
			return fmt.Errorf("sparse vector indices must be strictly ascending")
		}
	}
	return nil
}

func validateScopePaths(scope RetrievalScope) error {
	if len(scope.AllowedPaths) == 0 || len(scope.AllowedPaths) > 256 || len(scope.DeniedPaths) > 256 {
		return fmt.Errorf("retrieval scope paths are out of bounds")
	}
	seen := make(map[string]struct{}, len(scope.AllowedPaths)+len(scope.DeniedPaths))
	for _, item := range append(slices.Clone(scope.AllowedPaths), scope.DeniedPaths...) {
		if !validRetrievalPath(item) {
			return fmt.Errorf("retrieval scope contains an invalid path")
		}
		if _, exists := seen[item]; exists {
			return fmt.Errorf("retrieval scope contains duplicate paths")
		}
		seen[item] = struct{}{}
	}
	return nil
}

func validRetrievalPath(value string) bool {
	if value == "**" {
		return true
	}
	if strings.TrimSpace(value) == "" || len(value) > 4096 || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return false
	}
	clean := path.Clean(value)
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func validateRetrievalFilters(filters RetrievalFilters) error {
	if len(filters.ArtifactFamilies) == 0 || len(filters.ArtifactFamilies) > 5 ||
		len(filters.AuthorityClasses) == 0 || len(filters.AuthorityClasses) > 3 ||
		len(filters.Languages) > 32 || len(filters.SymbolKinds) > 64 {
		return fmt.Errorf("retrieval filters are out of bounds")
	}
	if !allKnownUnique(filters.ArtifactFamilies, retrievalArtifactFamilies) ||
		!allKnownUnique(filters.AuthorityClasses, retrievalAuthorityClasses) {
		return fmt.Errorf("retrieval filters contain invalid values")
	}
	if err := validateUniqueBoundedStrings(filters.Languages, 32, 80, "retrieval languages"); err != nil {
		return err
	}
	return validateUniqueBoundedStrings(filters.SymbolKinds, 64, 80, "retrieval symbol kinds")
}

func retrievalFiltersIncludeRepositoryGlobalFamily(filters RetrievalFilters) bool {
	for _, family := range filters.ArtifactFamilies {
		if !RetrievalFamilyRequiresSourceCommit(family) {
			return true
		}
	}
	return false
}

func validateRetrievalPolicy(policy RetrievalPolicy) error {
	if policy.Limit < 1 || policy.Limit > 100 || policy.DenseLimit < policy.Limit || policy.DenseLimit > 200 ||
		policy.SparseLimit < policy.Limit || policy.SparseLimit > 200 || policy.RRFK < 1 || policy.RRFK > 1000 ||
		policy.DenseWeight <= 0 || policy.DenseWeight > 10 || policy.SparseWeight <= 0 || policy.SparseWeight > 10 ||
		math.IsNaN(policy.DenseWeight) || math.IsInf(policy.DenseWeight, 0) ||
		math.IsNaN(policy.SparseWeight) || math.IsInf(policy.SparseWeight, 0) {
		return fmt.Errorf("retrieval policy is invalid")
	}
	return nil
}

func validateRetrievalCandidate(candidate RetrievalCandidate) error {
	if !retrievalPointIDPattern.MatchString(candidate.PointID) || !retrievalEntityIDPattern.MatchString(candidate.EntityID) ||
		!retrievalContentHashPattern.MatchString(candidate.SourceHash) || candidate.FusedScore <= 0 ||
		math.IsNaN(candidate.FusedScore) || math.IsInf(candidate.FusedScore, 0) {
		return fmt.Errorf("retrieval candidate is invalid")
	}
	if _, ok := retrievalArtifactFamilies[candidate.ArtifactFamily]; !ok {
		return fmt.Errorf("retrieval candidate artifact family is invalid")
	}
	if _, ok := retrievalAuthorityClasses[candidate.AuthorityClass]; !ok {
		return fmt.Errorf("retrieval candidate authority class is invalid")
	}
	if candidate.SourceCommit != nil && !retrievalSourceCommitPattern.MatchString(*candidate.SourceCommit) {
		return fmt.Errorf("retrieval candidate source commit is invalid")
	}
	if RetrievalFamilyRequiresSourceCommit(candidate.ArtifactFamily) && candidate.SourceCommit == nil {
		return fmt.Errorf("source-bound retrieval candidate requires a source commit")
	}
	if !RetrievalFamilyRequiresSourceCommit(candidate.ArtifactFamily) && candidate.SourceCommit != nil {
		return fmt.Errorf("repository-global retrieval candidate must not carry a source commit")
	}
	if candidate.DenseRank != nil && (*candidate.DenseRank < 1 || *candidate.DenseRank > 200) {
		return fmt.Errorf("retrieval candidate dense rank is invalid")
	}
	if candidate.SparseRank != nil && (*candidate.SparseRank < 1 || *candidate.SparseRank > 200) {
		return fmt.Errorf("retrieval candidate sparse rank is invalid")
	}
	if candidate.DenseRank == nil && candidate.SparseRank == nil {
		return fmt.Errorf("retrieval candidate has no rank evidence")
	}
	if err := validateOptionalRepositoryPath(candidate.RepositoryPath); err != nil {
		return err
	}
	return validateUniqueBoundedStrings(candidate.ProofRefs, 256, 512, "candidate proof references")
}

func validateOptionalBoundedString(value *string, maximum int, label string) error {
	if value == nil {
		return nil
	}
	if strings.TrimSpace(*value) == "" || len(*value) > maximum {
		return fmt.Errorf("retrieval %s is invalid", label)
	}
	return nil
}

func validateOptionalRepositoryPath(value *string) error {
	if value == nil {
		return nil
	}
	if !validRetrievalPath(*value) || *value == "**" {
		return fmt.Errorf("retrieval repository path is invalid")
	}
	return nil
}

func validateUniqueBoundedStrings(values []string, maximumCount, maximumLength int, label string) error {
	if len(values) > maximumCount {
		return fmt.Errorf("%s exceed the maximum count", label)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > maximumLength {
			return fmt.Errorf("%s contain an invalid value", label)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s contain duplicates", label)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func allKnownUnique(values []string, allowed map[string]struct{}) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, ok := allowed[value]; !ok {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validRejectionReason(reason string) bool {
	_, ok := map[string]struct{}{
		"unauthorized_scope": {}, "missing_canonical_entity": {}, "ambiguous_identity": {},
		"source_hash_mismatch": {}, "source_commit_mismatch": {}, "inactive_source": {},
		"stale_projection": {}, "malformed_candidate": {}, "duplicate_entity": {},
	}[reason]
	return ok
}

// FuseRetrievalRanks applies weighted RRF over two trusted, bounded rank lists.
func FuseRetrievalRanks(dense, sparse []RetrievalPoint, policy RetrievalPolicy) ([]RetrievalCandidate, error) {
	if err := validateRetrievalPolicy(policy); err != nil {
		return nil, err
	}
	if len(dense) > policy.DenseLimit || len(sparse) > policy.SparseLimit {
		return nil, fmt.Errorf("retrieval rank list exceeds policy limit")
	}
	type scored struct {
		candidate RetrievalCandidate
	}
	scores := make(map[string]*scored, len(dense)+len(sparse))
	apply := func(points []RetrievalPoint, weight float64, isDense bool) error {
		seen := make(map[string]struct{}, len(points))
		for index, point := range points {
			if err := ValidateRetrievalPoint(point); err != nil {
				return err
			}
			if _, exists := seen[point.PointID]; exists {
				return fmt.Errorf("retrieval rank list contains duplicate point %q", point.PointID)
			}
			seen[point.PointID] = struct{}{}
			rank := index + 1
			entry, exists := scores[point.PointID]
			if !exists {
				entry = &scored{candidate: RetrievalCandidate{
					PointID: point.PointID, EntityID: point.EntityID, ArtifactFamily: point.ArtifactFamily,
					SourceCommit: point.SourceCommit, SourceHash: point.SourceHash,
					AuthorityClass: point.AuthorityClass, RepositoryPath: point.RepositoryPath,
					ProofRefs: slices.Clone(point.ProofRefs),
				}}
				scores[point.PointID] = entry
			} else if entry.candidate.EntityID != point.EntityID || entry.candidate.SourceHash != point.SourceHash {
				return fmt.Errorf("retrieval point identity conflicts across rank lists")
			}
			entry.candidate.FusedScore += weight / float64(policy.RRFK+rank)
			if isDense {
				entry.candidate.DenseRank = &rank
			} else {
				entry.candidate.SparseRank = &rank
			}
		}
		return nil
	}
	if err := apply(dense, policy.DenseWeight, true); err != nil {
		return nil, err
	}
	if err := apply(sparse, policy.SparseWeight, false); err != nil {
		return nil, err
	}
	result := make([]RetrievalCandidate, 0, len(scores))
	for _, entry := range scores {
		result = append(result, entry.candidate)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].FusedScore != result[right].FusedScore {
			return result[left].FusedScore > result[right].FusedScore
		}
		if result[left].EntityID != result[right].EntityID {
			return result[left].EntityID < result[right].EntityID
		}
		return result[left].PointID < result[right].PointID
	})
	if len(result) > policy.Limit {
		result = result[:policy.Limit]
	}
	return result, nil
}
