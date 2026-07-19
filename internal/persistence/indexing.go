package persistence

import (
	"context"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/indexing"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

type IndexAdapterRun struct {
	Adapter         contracts.AdapterDescriptor `json:"adapter"`
	Status          string                      `json:"status"`
	DiagnosticCount int                         `json:"diagnostic_count"`
}

type IndexDelta struct {
	Ordinal          int     `json:"ordinal"`
	ChangeKind       string  `json:"change_kind"`
	EntityKind       string  `json:"entity_kind"`
	EntityID         string  `json:"entity_id"`
	PreviousEntityID *string `json:"previous_entity_id,omitempty"`
}

type IndexInvalidation struct {
	EntityID   string  `json:"entity_id"`
	Reason     string  `json:"reason"`
	SourceHash *string `json:"source_hash,omitempty"`
}

type IndexPublication struct {
	Bundle        indexing.IndexBundle `json:"bundle"`
	AdapterRuns   []IndexAdapterRun    `json:"adapter_runs"`
	Deltas        []IndexDelta         `json:"deltas"`
	Invalidations []IndexInvalidation  `json:"invalidations"`
}

type IndexRepository interface {
	ValidateIndexPublicationAuthority(
		context.Context,
		indexing.IndexBundle,
		runstate.CommandMetadata,
	) error
	PublishIndexSnapshot(
		context.Context,
		IndexPublication,
		runstate.CommandMetadata,
	) (contracts.RepositorySnapshot, error)
	GetActiveIndexSnapshot(context.Context) (contracts.RepositorySnapshot, bool, error)
	GetActiveIndexBundle(context.Context) (indexing.IndexBundle, bool, error)
}
