// Package runstate implements the deterministic run state machine and storage
// boundary.
package runstate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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

// CommandMetadata carries replay and audit identity across a command boundary.
type CommandMetadata struct {
	IdempotencyKey string
	ActorType      string
	ActorID        string
	CorrelationID  string
	CausationID    *string
}

// Repository persists run aggregates without exposing storage mechanics.
type Repository interface {
	CreateRun(
		context.Context,
		identity.RunID,
		string,
		CommandMetadata,
	) (contracts.Run, error)
	GetRun(context.Context, identity.RunID) (contracts.Run, error)
	TransitionRun(
		context.Context,
		identity.RunID,
		int,
		State,
		CommandMetadata,
	) (contracts.Run, error)
}

// ValidateCommandMetadata enforces the canonical durable event identity
// contract independently of the selected repository.
func ValidateCommandMetadata(metadata CommandMetadata) error {
	if length := utf8.RuneCountInString(metadata.IdempotencyKey); length < 8 || length > 200 {
		return fault.New(
			fault.CodeInvalidArgument,
			"runstate.ValidateCommandMetadata",
			"idempotency key length must be between 8 and 200 characters",
		)
	}
	switch metadata.ActorType {
	case "human", "agent", "worker", "system":
	default:
		return fault.New(
			fault.CodeInvalidArgument,
			"runstate.ValidateCommandMetadata",
			"actor type must be human, agent, worker, or system",
		)
	}
	if length := utf8.RuneCountInString(metadata.ActorID); length < 1 || length > 160 {
		return fault.New(
			fault.CodeInvalidArgument,
			"runstate.ValidateCommandMetadata",
			"actor ID length must be between 1 and 160 characters",
		)
	}
	if length := utf8.RuneCountInString(metadata.CorrelationID); length < 3 || length > 160 {
		return fault.New(
			fault.CodeInvalidArgument,
			"runstate.ValidateCommandMetadata",
			"correlation ID length must be between 3 and 160 characters",
		)
	}
	if metadata.CausationID != nil && utf8.RuneCountInString(*metadata.CausationID) > 160 {
		return fault.New(
			fault.CodeInvalidArgument,
			"runstate.ValidateCommandMetadata",
			"causation ID must not exceed 160 characters",
		)
	}
	return nil
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
	now := m.clock.Now()
	if now.Before(run.UpdatedAt) {
		now = run.UpdatedAt
	}
	run.UpdatedAt = now
	return run, nil
}

// Store is the concurrency-safe ephemeral run repository.
type Store struct {
	mu       sync.RWMutex
	runs     map[identity.RunID]contracts.Run
	receipts map[string]commandReceipt
	clock    clock.Clock
	machine  *Machine
}

type commandReceipt struct {
	requestHash [sha256.Size]byte
	run         contracts.Run
}

// NewStore creates an empty in-memory store.
func NewStore(source clock.Clock) *Store {
	if source == nil {
		source = clock.Real{}
	}
	return &Store{
		runs:     make(map[identity.RunID]contracts.Run),
		receipts: make(map[string]commandReceipt),
		clock:    source,
		machine:  NewMachine(source),
	}
}

// Create adds a draft run with a caller-supplied ID.
func (s *Store) Create(id identity.RunID, objective string) (contracts.Run, error) {
	if err := validateObjective(objective); err != nil {
		return contracts.Run{}, err
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

// CreateRun implements Repository for deterministic in-memory operation.
func (s *Store) CreateRun(
	_ context.Context,
	id identity.RunID,
	objective string,
	metadata CommandMetadata,
) (contracts.Run, error) {
	if err := validateObjective(objective); err != nil {
		return contracts.Run{}, err
	}
	if err := ValidateCommandMetadata(metadata); err != nil {
		return contracts.Run{}, err
	}
	scope := "create_run"
	requestHash := commandRequestHash(metadata, "create_run", objective)

	s.mu.Lock()
	defer s.mu.Unlock()
	if replay, found, err := s.replayLocked(
		scope,
		metadata.IdempotencyKey,
		requestHash,
	); err != nil || found {
		return replay, err
	}
	if _, exists := s.runs[id]; exists {
		return contracts.Run{}, fault.New(
			fault.CodeConflict,
			"runstate.Store.CreateRun",
			fmt.Sprintf("run %s already exists", id),
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
	s.runs[id] = run
	s.saveReceiptLocked(scope, metadata.IdempotencyKey, requestHash, run)
	return run, nil
}

// GetRun implements Repository for deterministic in-memory operation.
func (s *Store) GetRun(
	_ context.Context,
	id identity.RunID,
) (contracts.Run, error) {
	return s.Get(id)
}

// TransitionRun implements Repository for deterministic in-memory operation.
func (s *Store) TransitionRun(
	_ context.Context,
	id identity.RunID,
	expectedVersion int,
	target State,
	metadata CommandMetadata,
) (contracts.Run, error) {
	if expectedVersion < 1 {
		return contracts.Run{}, fault.New(
			fault.CodeInvalidArgument,
			"runstate.Store.TransitionRun",
			"expected version must be at least 1",
		)
	}
	if err := ValidateCommandMetadata(metadata); err != nil {
		return contracts.Run{}, err
	}
	scope := "transition_run:" + id.String()
	requestHash := commandRequestHash(
		metadata,
		"transition_run",
		id.String(),
		fmt.Sprint(expectedVersion),
		string(target),
	)

	s.mu.Lock()
	defer s.mu.Unlock()
	if replay, found, err := s.replayLocked(
		scope,
		metadata.IdempotencyKey,
		requestHash,
	); err != nil || found {
		return replay, err
	}
	run, ok := s.runs[id]
	if !ok {
		return contracts.Run{}, fault.New(
			fault.CodeNotFound,
			"runstate.Store.TransitionRun",
			fmt.Sprintf("run %s was not found", id),
		)
	}
	if run.Version != expectedVersion {
		return contracts.Run{}, fault.New(
			fault.CodeConflict,
			"runstate.Store.TransitionRun",
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
	s.saveReceiptLocked(scope, metadata.IdempotencyKey, requestHash, updated)
	return updated, nil
}

func validateObjective(objective string) error {
	length := utf8.RuneCountInString(objective)
	if length < 3 || length > 8000 {
		return fault.New(
			fault.CodeInvalidArgument,
			"runstate.Store.Create",
			"objective length must be between 3 and 8000 characters",
		)
	}
	return nil
}

func commandRequestHash(metadata CommandMetadata, parts ...string) [sha256.Size]byte {
	causation := ""
	if metadata.CausationID != nil {
		causation = *metadata.CausationID
	}
	return sha256.Sum256([]byte(strings.Join(append(
		parts,
		metadata.ActorType,
		metadata.ActorID,
		causation,
	), "\x00")))
}

func (s *Store) replayLocked(
	scope string,
	key string,
	requestHash [sha256.Size]byte,
) (contracts.Run, bool, error) {
	receipt, found := s.receipts[scope+"\x00"+key]
	if !found {
		return contracts.Run{}, false, nil
	}
	if receipt.requestHash != requestHash {
		return contracts.Run{}, true, fault.New(
			fault.CodeConflict,
			"runstate.Store.idempotency",
			"idempotency key was reused with a different command",
		)
	}
	return receipt.run, true, nil
}

func (s *Store) saveReceiptLocked(
	scope string,
	key string,
	requestHash [sha256.Size]byte,
	run contracts.Run,
) {
	s.receipts[scope+"\x00"+key] = commandReceipt{
		requestHash: requestHash,
		run:         run,
	}
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
