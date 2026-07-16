// Package runstate implements the deterministic in-memory run state machine.
package runstate

import (
	"fmt"
	"sync"
	"time"

	"github.com/rvbernucci/forja-guide/internal/clock"
	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/identity"
)

// State is a valid run lifecycle state.
type State string

const (
	StateDraft            State = "draft"
	StateAwaitingApproval State = "awaiting_approval"
	StateQueued           State = "queued"
	StatePreparing        State = "preparing"
	StateRunning          State = "running"
	StateValidating       State = "validating"
	StateAwaitingDecision State = "awaiting_decision"
	StateCompleted        State = "completed"
	StateCancelling       State = "cancelling"
	StateCancelled        State = "cancelled"
	StateFailedRetryable  State = "failed_retryable"
	StateFailedTerminal   State = "failed_terminal"
)

var allowedTransitions = map[State]map[State]struct{}{
	StateDraft: {
		StateAwaitingApproval: {},
		StateCancelling:       {},
	},
	StateAwaitingApproval: {
		StateQueued:     {},
		StateCancelling: {},
	},
	StateQueued: {
		StatePreparing:  {},
		StateCancelling: {},
	},
	StatePreparing: {
		StateRunning:         {},
		StateCancelling:      {},
		StateFailedRetryable: {},
		StateFailedTerminal:  {},
	},
	StateRunning: {
		StateValidating:      {},
		StateCancelling:      {},
		StateFailedRetryable: {},
		StateFailedTerminal:  {},
	},
	StateValidating: {
		StateAwaitingDecision: {},
		StateCompleted:        {},
		StateCancelling:       {},
		StateFailedRetryable:  {},
		StateFailedTerminal:   {},
	},
	StateAwaitingDecision: {
		StateRunning:        {},
		StateCompleted:      {},
		StateCancelling:     {},
		StateFailedTerminal: {},
	},
	StateFailedRetryable: {
		StateQueued:     {},
		StateCancelling: {},
	},
	StateCancelling: {
		StateCancelled: {},
	},
}

// ParseState validates a state string.
func ParseState(value string) (State, error) {
	state := State(value)
	switch state {
	case StateDraft,
		StateAwaitingApproval,
		StateQueued,
		StatePreparing,
		StateRunning,
		StateValidating,
		StateAwaitingDecision,
		StateCompleted,
		StateCancelling,
		StateCancelled,
		StateFailedRetryable,
		StateFailedTerminal:
		return state, nil
	default:
		return "", fault.New(
			fault.CodeInvalidArgument,
			"runstate.ParseState",
			fmt.Sprintf("unknown run state %q", value),
		)
	}
}

// CanTransition reports whether a state transition is explicitly allowed.
func CanTransition(from, to State) bool {
	destinations, ok := allowedTransitions[from]
	if !ok {
		return false
	}
	_, ok = destinations[to]
	return ok
}

// Machine applies state transitions using a deterministic clock.
type Machine struct {
	clock clock.Clock
}

// NewMachine creates a state machine.
func NewMachine(source clock.Clock) *Machine {
	if source == nil {
		source = clock.Real{}
	}
	return &Machine{clock: source}
}

// Transition returns an updated copy when the transition is valid.
func (m *Machine) Transition(run contracts.Run, target State) (contracts.Run, error) {
	current, err := ParseState(run.State)
	if err != nil {
		return contracts.Run{}, err
	}
	if !CanTransition(current, target) {
		return contracts.Run{}, fault.New(
			fault.CodeConflict,
			"runstate.Machine.Transition",
			fmt.Sprintf("transition %s -> %s is not allowed", current, target),
		)
	}
	run.State = string(target)
	run.Version++
	run.UpdatedAt = m.clock.Now()
	return run, nil
}

// Store is a concurrency-safe in-memory run repository for Sprint 01.
type Store struct {
	mu      sync.RWMutex
	runs    map[identity.RunID]contracts.Run
	clock   clock.Clock
	machine *Machine
}

// NewStore creates an empty in-memory store.
func NewStore(source clock.Clock) *Store {
	if source == nil {
		source = clock.Real{}
	}
	return &Store{
		runs:    make(map[identity.RunID]contracts.Run),
		clock:   source,
		machine: NewMachine(source),
	}
}

// Create adds a draft run with a caller-supplied ID.
func (s *Store) Create(id identity.RunID, objective string) (contracts.Run, error) {
	if len(objective) < 3 || len(objective) > 8000 {
		return contracts.Run{}, fault.New(
			fault.CodeInvalidArgument,
			"runstate.Store.Create",
			"objective length must be between 3 and 8000 characters",
		)
	}
	now := s.clock.Now()
	run := contracts.Run{
		RunID:         id.String(),
		SchemaVersion: "1.0",
		Objective:     objective,
		State:         string(StateDraft),
		Version:       1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.runs[id]; exists {
		return contracts.Run{}, fault.New(
			fault.CodeConflict,
			"runstate.Store.Create",
			fmt.Sprintf("run %s already exists", id),
		)
	}
	s.runs[id] = run
	return run, nil
}

// Get returns a copy of a run.
func (s *Store) Get(id identity.RunID) (contracts.Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.runs[id]
	if !ok {
		return contracts.Run{}, fault.New(
			fault.CodeNotFound,
			"runstate.Store.Get",
			fmt.Sprintf("run %s was not found", id),
		)
	}
	return run, nil
}

// Transition applies a state change atomically.
func (s *Store) Transition(
	id identity.RunID,
	expectedVersion int,
	target State,
) (contracts.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	run, ok := s.runs[id]
	if !ok {
		return contracts.Run{}, fault.New(
			fault.CodeNotFound,
			"runstate.Store.Transition",
			fmt.Sprintf("run %s was not found", id),
		)
	}
	if run.Version != expectedVersion {
		return contracts.Run{}, fault.New(
			fault.CodeConflict,
			"runstate.Store.Transition",
			fmt.Sprintf(
				"run %s version mismatch: expected %d, current %d",
				id,
				expectedVersion,
				run.Version,
			),
		)
	}
	updated, err := s.machine.Transition(run, target)
	if err != nil {
		return contracts.Run{}, err
	}
	s.runs[id] = updated
	return updated, nil
}

// Terminal reports whether the state cannot transition further.
func Terminal(state State) bool {
	return state == StateCompleted ||
		state == StateCancelled ||
		state == StateFailedTerminal
}

// ValidTimestamp reports whether a timestamp is normalized enough for contracts.
func ValidTimestamp(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, offset := value.Zone()
	return offset == 0
}
