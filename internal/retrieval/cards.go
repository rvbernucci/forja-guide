// Package retrieval builds and evaluates governed retrieval projections.
package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

const MaxCardTextBytes = 32768

// Embedder produces only the dense portion of a version-pinned retrieval point.
type Embedder interface {
	Descriptor() contracts.EmbeddingDescriptor
	Embed(context.Context, string) ([]float64, error)
}

// CardSource holds canonical material after the caller has completed authority checks.
type CardSource struct {
	TenantID       string
	RepositoryID   string
	EntityID       string
	ArtifactFamily string
	SourceCommit   *string
	SourceHash     string
	AuthorityClass string
	Status         string
	Language       *string
	SymbolKind     *string
	RepositoryPath *string
	Title          string
	Body           string
	ProofRefs      []string
	GraphNodeIDs   []string
}

// BuildCardText serializes a bounded card with sorted optional evidence.
func BuildCardText(source CardSource) (string, error) {
	if err := validateCardSource(source); err != nil {
		return "", err
	}
	lines := []string{
		"family: " + source.ArtifactFamily,
		"entity: " + source.EntityID,
		"authority: " + source.AuthorityClass,
		"source_hash: " + source.SourceHash,
	}
	if source.SourceCommit != nil {
		lines = append(lines, "source_commit: "+*source.SourceCommit)
	}
	if source.RepositoryPath != nil {
		lines = append(lines, "repository_path: "+*source.RepositoryPath)
	}
	if source.Language != nil {
		lines = append(lines, "language: "+*source.Language)
	}
	if source.SymbolKind != nil {
		lines = append(lines, "symbol_kind: "+*source.SymbolKind)
	}
	if title := normalizeCardText(source.Title); title != "" {
		lines = append(lines, "title: "+title)
	}
	if body := normalizeCardText(source.Body); body != "" {
		lines = append(lines, "content: "+body)
	}
	if len(source.ProofRefs) > 0 {
		proofs := append([]string(nil), source.ProofRefs...)
		sort.Strings(proofs)
		lines = append(lines, "proof_refs: "+strings.Join(proofs, ","))
	}
	if len(source.GraphNodeIDs) > 0 {
		nodes := append([]string(nil), source.GraphNodeIDs...)
		sort.Strings(nodes)
		lines = append(lines, "graph_node_ids: "+strings.Join(nodes, ","))
	}
	card := strings.Join(lines, "\n")
	if len(card) > MaxCardTextBytes {
		return "", fmt.Errorf("retrieval card exceeds %d bytes", MaxCardTextBytes)
	}
	return card, nil
}

// BuildSymbolSource maps deterministic Sprint 08 metadata into one safe symbol card.
func BuildSymbolSource(snapshot contracts.RepositorySnapshot, file contracts.FileCard, symbol contracts.SymbolCard, authorityClass string, proofRefs []string) (CardSource, error) {
	return buildIndexedSymbolSource(snapshot, file, symbol, authorityClass, proofRefs, "symbol")
}

// BuildTestSource emits a separate test-family card only for a symbol the
// canonical index itself marks as a test. The entity remains the stable symbol
// identity, while the artifact family makes test-only retrieval filters work.
func BuildTestSource(snapshot contracts.RepositorySnapshot, file contracts.FileCard, symbol contracts.SymbolCard, authorityClass string, proofRefs []string) (CardSource, error) {
	if !symbol.Test {
		return CardSource{}, fmt.Errorf("test source requires a canonical test symbol")
	}
	return buildIndexedSymbolSource(snapshot, file, symbol, authorityClass, proofRefs, "test")
}

// BuildDecisionSource produces a bounded card for a resolved, durable approval
// decision. Pending decisions are intentionally absent: they are not a stable
// outcome and must not influence future context selection.
func BuildDecisionSource(tenantID, repositoryID string, decision contracts.Decision) (CardSource, error) {
	if err := validateDecisionForRetrieval(decision); err != nil {
		return CardSource{}, err
	}
	sourceHash, err := decisionSourceHash(decision)
	if err != nil {
		return CardSource{}, err
	}
	body := "risk_class: " + decision.RiskClass + "\nstatus: " + decision.Status
	if decision.Reason != nil {
		body += "\nreason: " + *decision.Reason
	}
	return CardSource{
		TenantID: tenantID, RepositoryID: repositoryID,
		EntityID: decision.DecisionID, ArtifactFamily: "decision", SourceHash: sourceHash,
		AuthorityClass: "canonical", Status: "active",
		Title: "decision: " + decision.Action, Body: body,
		ProofRefs: []string{"decision:" + decision.DecisionID, "sprint:" + decision.SprintID, "run:" + decision.RunID},
	}, nil
}

// BuildIncidentSource produces a bounded, safe operational card. Incidents
// intentionally retain only terminal classification and immutable references;
// raw worker output remains outside retrieval and context assembly.
func BuildIncidentSource(incident contracts.Incident) (CardSource, error) {
	if err := contracts.ValidateIncident(incident); err != nil {
		return CardSource{}, fmt.Errorf("validate incident: %w", err)
	}
	body := "severity: " + incident.Severity +
		"\nclassification: " + incident.Classification +
		"\nretryable: " + fmt.Sprintf("%t", incident.Retryable) +
		"\nevent_type: " + incident.EventType
	return CardSource{
		TenantID: incident.TenantID, RepositoryID: incident.RepositoryID,
		EntityID: incident.IncidentID, ArtifactFamily: "incident", SourceHash: incident.SourceHash,
		AuthorityClass: "canonical", Status: "active",
		Title: "incident: " + incident.Classification, Body: body,
		ProofRefs: append([]string{
			"incident:" + incident.IncidentID,
			"attempt:" + incident.AttemptID,
			"run:" + incident.RunID,
			"event:" + incident.EventID,
			"incident_source_hash:" + incident.SourceHash,
		}, incident.EvidenceRefs...),
	}, nil
}

func validateDecisionForRetrieval(decision contracts.Decision) error {
	if decision.SchemaVersion != "1.0" || !strings.HasPrefix(decision.DecisionID, "decision_") ||
		!strings.HasPrefix(decision.SprintID, "sprint_") || !strings.HasPrefix(decision.RunID, "run_") ||
		decision.Version < 1 || strings.TrimSpace(decision.Action) != decision.Action || decision.Action == "" || len(decision.Action) > 160 ||
		(decision.RiskClass != "low" && decision.RiskClass != "medium" && decision.RiskClass != "high" && decision.RiskClass != "critical") ||
		(decision.Status != "approved" && decision.Status != "rejected") || decision.DecidedBy == nil || strings.TrimSpace(*decision.DecidedBy) == "" ||
		decision.Reason == nil || strings.TrimSpace(*decision.Reason) == "" || len(*decision.Reason) > 2000 || decision.DecidedAt == nil || decision.DecidedAt.IsZero() {
		return fmt.Errorf("decision is not a resolved canonical retrieval source")
	}
	return nil
}

func decisionSourceHash(decision contracts.Decision) (string, error) {
	payload := struct {
		DecisionID string  `json:"decision_id"`
		SprintID   string  `json:"sprint_id"`
		RunID      string  `json:"run_id"`
		Action     string  `json:"action"`
		RiskClass  string  `json:"risk_class"`
		Status     string  `json:"status"`
		Version    int     `json:"version"`
		Requested  string  `json:"requested_by"`
		Decided    *string `json:"decided_by"`
		Reason     *string `json:"reason"`
	}{
		DecisionID: decision.DecisionID, SprintID: decision.SprintID, RunID: decision.RunID,
		Action: decision.Action, RiskClass: decision.RiskClass, Status: decision.Status,
		Version: decision.Version, Requested: decision.RequestedBy, Decided: decision.DecidedBy, Reason: decision.Reason,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode canonical decision source: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func buildIndexedSymbolSource(snapshot contracts.RepositorySnapshot, file contracts.FileCard, symbol contracts.SymbolCard, authorityClass string, proofRefs []string, artifactFamily string) (CardSource, error) {
	if err := contracts.ValidateRepositorySnapshot(snapshot); err != nil {
		return CardSource{}, fmt.Errorf("validate snapshot: %w", err)
	}
	if err := contracts.ValidateFileCard(file); err != nil {
		return CardSource{}, fmt.Errorf("validate file card: %w", err)
	}
	if err := contracts.ValidateSymbolCard(symbol); err != nil {
		return CardSource{}, fmt.Errorf("validate symbol card: %w", err)
	}
	if snapshot.SnapshotID != file.SnapshotID || symbol.SnapshotID != snapshot.SnapshotID || symbol.FileID != file.FileID {
		return CardSource{}, fmt.Errorf("symbol source does not bind one snapshot and file")
	}
	language := symbol.Language
	kind := symbol.Kind
	commit := snapshot.SourceCommit
	flags := make([]string, 0, 4)
	if symbol.Exported {
		flags = append(flags, "exported")
	}
	if symbol.Test {
		flags = append(flags, "test")
	}
	if symbol.Route {
		flags = append(flags, "route")
	}
	if symbol.Schema {
		flags = append(flags, "schema")
	}
	body := "signature: " + symbol.Signature
	if len(flags) > 0 {
		body += "\nflags: " + strings.Join(flags, ",")
	}
	return CardSource{
		TenantID: snapshot.TenantID, RepositoryID: snapshot.RepositoryID,
		EntityID: symbol.SymbolID, ArtifactFamily: artifactFamily, SourceCommit: &commit,
		SourceHash: file.SourceHash, AuthorityClass: authorityClass, Status: "active",
		Language: &language, SymbolKind: &kind, RepositoryPath: &file.Path,
		Title: symbol.QualifiedName, Body: body,
		ProofRefs: append([]string{"snapshot:" + snapshot.SnapshotID, "file:" + file.FileID}, proofRefs...),
	}, nil
}

// BuildPoint combines deterministic card and sparse data with validated provider output.
func BuildPoint(ctx context.Context, source CardSource, generation string, embedder Embedder, sparseEncoder SparseEncoder) (contracts.RetrievalPoint, error) {
	if embedder == nil || sparseEncoder == nil {
		return contracts.RetrievalPoint{}, fmt.Errorf("embedder and sparse encoder are required")
	}
	card, err := BuildCardText(source)
	if err != nil {
		return contracts.RetrievalPoint{}, err
	}
	descriptor := embedder.Descriptor()
	if descriptor.SparseEncoderVersion != sparseEncoder.Version() {
		return contracts.RetrievalPoint{}, fmt.Errorf("embedding descriptor does not bind sparse encoder version")
	}
	if expected := contracts.RetrievalGenerationID(descriptor.Model, descriptor.Version, descriptor.Dimensions, descriptor.SparseEncoderVersion); generation != expected {
		return contracts.RetrievalPoint{}, fmt.Errorf("collection generation does not bind embedding descriptor")
	}
	dense, err := embedder.Embed(ctx, card)
	if err != nil {
		return contracts.RetrievalPoint{}, fmt.Errorf("embed retrieval card: %w", err)
	}
	sparse, err := sparseEncoder.Encode(card)
	if err != nil {
		return contracts.RetrievalPoint{}, fmt.Errorf("encode sparse retrieval card: %w", err)
	}
	point := contracts.RetrievalPoint{
		SchemaVersion: contracts.RetrievalSchemaVersion, CollectionGeneration: generation,
		TenantID: source.TenantID, RepositoryID: source.RepositoryID, EntityID: source.EntityID,
		ArtifactFamily: source.ArtifactFamily, SourceCommit: source.SourceCommit, SourceHash: source.SourceHash,
		CardText: card, CardTextHash: contracts.CardTextHash(card), Status: source.Status,
		AuthorityClass: source.AuthorityClass, Language: source.Language, SymbolKind: source.SymbolKind,
		RepositoryPath: source.RepositoryPath, ProofRefs: sortedStrings(source.ProofRefs),
		GraphNodeIDs: sortedStrings(source.GraphNodeIDs), Dense: dense, Sparse: sparse, Embedding: descriptor,
	}
	point.PointID = contracts.RetrievalPointID(generation, point.EntityID, point.SourceHash)
	if err := contracts.ValidateRetrievalPoint(point); err != nil {
		return contracts.RetrievalPoint{}, err
	}
	return point, nil
}

func validateCardSource(source CardSource) error {
	point := contracts.RetrievalPoint{
		PointID: contracts.RetrievalPointID(
			contracts.RetrievalGenerationID("placeholder", "placeholder", 1, "placeholder"), source.EntityID, source.SourceHash,
		),
		SchemaVersion:        contracts.RetrievalSchemaVersion,
		CollectionGeneration: contracts.RetrievalGenerationID("placeholder", "placeholder", 1, "placeholder"),
		TenantID:             source.TenantID, RepositoryID: source.RepositoryID, EntityID: source.EntityID,
		ArtifactFamily: source.ArtifactFamily, SourceCommit: source.SourceCommit, SourceHash: source.SourceHash,
		CardText: "placeholder", CardTextHash: contracts.CardTextHash("placeholder"), Status: source.Status,
		AuthorityClass: source.AuthorityClass, Language: source.Language, SymbolKind: source.SymbolKind,
		RepositoryPath: source.RepositoryPath, ProofRefs: source.ProofRefs, GraphNodeIDs: source.GraphNodeIDs,
		Dense: []float64{1}, Sparse: contracts.SparseVector{Indices: []uint32{1}, Values: []float64{1}},
		Embedding: contracts.EmbeddingDescriptor{Model: "placeholder", Version: "placeholder", Dimensions: 1, SparseEncoderVersion: "placeholder", EmbeddedAt: time.Unix(1, 0).UTC()},
	}
	if err := contracts.ValidateRetrievalPoint(point); err != nil {
		return fmt.Errorf("validate retrieval card source: %w", err)
	}
	if len(source.Title) > MaxCardTextBytes || len(source.Body) > MaxCardTextBytes {
		return fmt.Errorf("retrieval card source text is out of bounds")
	}
	return nil
}

func normalizeCardText(value string) string {
	return strings.Join(strings.FieldsFunc(value, unicode.IsSpace), " ")
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
