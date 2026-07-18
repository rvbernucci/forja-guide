// Package contracts maps the language-neutral public schemas into Go types.
package contracts

import "time"

const WorkerReportSchemaRef = "https://forja.dev/schemas/worker-report.schema.json"

// Run is the canonical run aggregate contract.
type Run struct {
	RunID         string    `json:"run_id"`
	SchemaVersion string    `json:"schema_version"`
	Objective     string    `json:"objective"`
	State         string    `json:"state"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Sprint is the canonical governed plan contract.
type Sprint struct {
	SprintID          string    `json:"sprint_id"`
	SchemaVersion     string    `json:"schema_version"`
	SequenceNumber    int       `json:"sequence_number"`
	Title             string    `json:"title"`
	Objective         string    `json:"objective"`
	Status            string    `json:"status"`
	Version           int       `json:"version"`
	RunID             string    `json:"run_id"`
	PendingDecisionID *string   `json:"pending_decision_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Decision is a durable approval request bound to one canonical action.
type Decision struct {
	DecisionID    string     `json:"decision_id"`
	SchemaVersion string     `json:"schema_version"`
	SprintID      string     `json:"sprint_id"`
	RunID         string     `json:"run_id"`
	Action        string     `json:"action"`
	RiskClass     string     `json:"risk_class"`
	Status        string     `json:"status"`
	Version       int        `json:"version"`
	RequestedBy   string     `json:"requested_by"`
	DecidedBy     *string    `json:"decided_by,omitempty"`
	Reason        *string    `json:"reason,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	DecidedAt     *time.Time `json:"decided_at,omitempty"`
}

// Actor identifies who caused a durable event.
type Actor struct {
	ActorType string `json:"actor_type"`
	ActorID   string `json:"actor_id"`
}

// RunEvent maps run-event.schema.json.
type RunEvent struct {
	EventID          string         `json:"event_id"`
	EventType        string         `json:"event_type"`
	SchemaVersion    string         `json:"schema_version"`
	AggregateType    string         `json:"aggregate_type"`
	AggregateID      string         `json:"aggregate_id"`
	AggregateVersion int            `json:"aggregate_version"`
	OccurredAt       time.Time      `json:"occurred_at"`
	Actor            Actor          `json:"actor"`
	CorrelationID    string         `json:"correlation_id"`
	CausationID      *string        `json:"causation_id,omitempty"`
	IdempotencyKey   string         `json:"idempotency_key"`
	Payload          map[string]any `json:"payload"`
}

// Provenance describes the origin of an artifact.
type Provenance struct {
	SourceType   string   `json:"source_type"`
	SourceRefs   []string `json:"source_refs"`
	SourceCommit *string  `json:"source_commit,omitempty"`
}

// Artifact maps artifact.schema.json.
type Artifact struct {
	ArtifactID    string         `json:"artifact_id"`
	SchemaVersion string         `json:"schema_version"`
	TenantID      string         `json:"tenant_id"`
	RepositoryID  string         `json:"repository_id"`
	RunID         *string        `json:"run_id,omitempty"`
	Kind          string         `json:"kind"`
	Status        string         `json:"status"`
	ContentHash   string         `json:"content_hash"`
	MediaType     string         `json:"media_type"`
	StorageURI    *string        `json:"storage_uri,omitempty"`
	SizeBytes     *int64         `json:"size_bytes,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	CreatedBy     string         `json:"created_by"`
	ValidatedBy   []string       `json:"validated_by,omitempty"`
	Supersedes    []string       `json:"supersedes,omitempty"`
	Provenance    Provenance     `json:"provenance"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

// ContextScope bounds repository context retrieval.
type ContextScope struct {
	Commit       string   `json:"commit"`
	AllowedPaths []string `json:"allowed_paths"`
	DeniedPaths  []string `json:"denied_paths,omitempty"`
	Languages    []string `json:"languages,omitempty"`
}

// ContextBudget limits context assembly.
type ContextBudget struct {
	MaxTokens    int `json:"max_tokens"`
	MaxSources   int `json:"max_sources"`
	MaxGraphHops int `json:"max_graph_hops"`
}

// ContextRequest maps context-request.schema.json.
type ContextRequest struct {
	RequestID     string        `json:"request_id"`
	SchemaVersion string        `json:"schema_version"`
	TenantID      string        `json:"tenant_id"`
	RepositoryID  string        `json:"repository_id"`
	Objective     string        `json:"objective"`
	Scope         ContextScope  `json:"scope"`
	SeedEntityIDs []string      `json:"seed_entity_ids,omitempty"`
	Budget        ContextBudget `json:"budget"`
}

// ContextEntity is an entity selected for a context pack.
type ContextEntity struct {
	EntityID   string `json:"entity_id"`
	Kind       string `json:"kind"`
	Authority  string `json:"authority"`
	SourceHash string `json:"source_hash"`
}

// ContextPath is a proven or candidate graph path.
type ContextPath struct {
	PathID           string   `json:"path_id"`
	EntityIDs        []string `json:"entity_ids"`
	RelationTypes    []string `json:"relation_types"`
	EvidenceStrength string   `json:"evidence_strength"`
}

// ContextSource is a cited source selected for a context pack.
type ContextSource struct {
	ArtifactID    string `json:"artifact_id"`
	Citation      string `json:"citation"`
	Authority     string `json:"authority"`
	TokenEstimate int    `json:"token_estimate"`
}

// ContextGap exposes missing or ambiguous evidence.
type ContextGap struct {
	GapID       string `json:"gap_id"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

// ContextReceipt records retrieval candidate and freshness counts.
type ContextReceipt struct {
	ExactCandidates     int    `json:"exact_candidates"`
	SemanticCandidates  int    `json:"semantic_candidates"`
	ResolvedEntities    int    `json:"resolved_entities"`
	GraphPaths          int    `json:"graph_paths"`
	ProjectionFreshness string `json:"projection_freshness"`
}

// ContextPack maps context-pack.schema.json.
type ContextPack struct {
	ContextPackID string          `json:"context_pack_id"`
	SchemaVersion string          `json:"schema_version"`
	RequestID     string          `json:"request_id"`
	TenantID      string          `json:"tenant_id"`
	RepositoryID  string          `json:"repository_id"`
	SourceCommit  string          `json:"source_commit"`
	Status        string          `json:"status"`
	Entities      []ContextEntity `json:"entities"`
	Paths         []ContextPath   `json:"paths"`
	Sources       []ContextSource `json:"sources"`
	Gaps          []ContextGap    `json:"gaps"`
	TokenEstimate int             `json:"token_estimate"`
	Receipt       ContextReceipt  `json:"receipt"`
}

// WorkerBudgets bounds one process attempt independently of model behavior.
type WorkerBudgets struct {
	WallClockMS         int  `json:"wall_clock_ms"`
	InactivityMS        int  `json:"inactivity_ms"`
	MaxOutputBytes      int  `json:"max_output_bytes"`
	CancellationGraceMS int  `json:"cancellation_grace_ms"`
	MaxRetries          int  `json:"max_retries"`
	MaxTokens           *int `json:"max_tokens,omitempty"`
	MaxCommands         *int `json:"max_commands,omitempty"`
}

// WorkerTask is the complete authority and budget envelope for one attempt.
type WorkerTask struct {
	TaskID            string        `json:"task_id"`
	AttemptID         string        `json:"attempt_id"`
	RunID             string        `json:"run_id"`
	SchemaVersion     string        `json:"schema_version"`
	Role              string        `json:"role"`
	Objective         string        `json:"objective"`
	RepositoryPath    string        `json:"repository_path"`
	WorktreePath      string        `json:"worktree_path"`
	ReadScopes        []string      `json:"read_scopes"`
	WriteScopes       []string      `json:"write_scopes"`
	ContextPackRef    *string       `json:"context_pack_ref,omitempty"`
	ResultSchemaRef   string        `json:"result_schema_ref"`
	EvidenceOutputDir string        `json:"evidence_output_dir"`
	AttemptOrdinal    int           `json:"attempt_ordinal"`
	Model             *string       `json:"model,omitempty"`
	Budgets           WorkerBudgets `json:"budgets"`
}

// WorkerReport is the model-authored, schema-constrained completion report.
type WorkerReport struct {
	Status       string   `json:"status"`
	Summary      string   `json:"summary"`
	ChangedPaths []string `json:"changed_paths"`
	EvidenceRefs []string `json:"evidence_refs"`
	Risks        []string `json:"risks"`
}

// WorkerUsage contains non-negative accounting observed from an adapter.
type WorkerUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	ToolCalls         int `json:"tool_calls"`
}

// WorkerResult is the supervisor-authored result for one bounded process.
type WorkerResult struct {
	TaskID            string        `json:"task_id"`
	AttemptID         string        `json:"attempt_id"`
	RunID             string        `json:"run_id"`
	SchemaVersion     string        `json:"schema_version"`
	Adapter           string        `json:"adapter"`
	Status            string        `json:"status"`
	Retryable         bool          `json:"retryable"`
	TerminationReason string        `json:"termination_reason"`
	StartedAt         time.Time     `json:"started_at"`
	FinishedAt        time.Time     `json:"finished_at"`
	DurationMS        int64         `json:"duration_ms"`
	ExitCode          *int          `json:"exit_code"`
	Stdout            string        `json:"stdout"`
	Stderr            string        `json:"stderr"`
	StdoutSHA256      string        `json:"stdout_sha256"`
	StderrSHA256      string        `json:"stderr_sha256"`
	Usage             WorkerUsage   `json:"usage"`
	Report            *WorkerReport `json:"report"`
	EvidenceRefs      []string      `json:"evidence_refs"`
}
