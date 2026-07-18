// Package worker supervises bounded, authority-free agent processes.
package worker

import (
	"context"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

// Invocation is an argv-safe process description produced by an adapter.
type Invocation struct {
	Path  string
	Args  []string
	Dir   string
	Stdin string
}

// IsolationCapability is the static containment contract implemented by an
// adapter. Version 1.0 requires full-worktree reads, declared-root writes, and
// denied command-network access.
type IsolationCapability struct {
	PolicyID        string
	Version         string
	ReadBoundary    string
	WriteBoundary   string
	NetworkBoundary string
}

// InvocationIsolationPolicy is trusted supervisor-side authority for proving
// that an adapter invocation exactly enforces its declared containment.
type InvocationIsolationPolicy interface {
	ID() string
	Verify(contracts.WorkerTask, ExecutionPaths, Invocation) error
}

// ExecutionPaths are supervisor-owned files exposed to an adapter.
type ExecutionPaths struct {
	HomeDir          string
	ReportPath       string
	ReportSchemaPath string
}

// Adapter translates a canonical task into one process invocation.
type Adapter interface {
	Name() string
	IsolationCapability() IsolationCapability
	Build(contracts.WorkerTask, ExecutionPaths) (Invocation, error)
	ParseUsage([]byte) contracts.WorkerUsage
	RetryableFailure(exitCode int, stderr string) bool
}

// Event is safe lifecycle telemetry. It deliberately has no output-content field.
type Event struct {
	Kind       string    `json:"kind"`
	TaskID     string    `json:"task_id"`
	AttemptID  string    `json:"attempt_id"`
	Adapter    string    `json:"adapter"`
	OccurredAt time.Time `json:"occurred_at"`
	Stream     string    `json:"stream,omitempty"`
	Bytes      int       `json:"bytes,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	ExitCode   *int      `json:"exit_code,omitempty"`
}

// EventSink receives structured process metadata without raw worker output.
type EventSink interface {
	Emit(context.Context, Event) error
}

type discardEvents struct{}

func (discardEvents) Emit(context.Context, Event) error { return nil }
