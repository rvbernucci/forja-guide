package runstate

import (
	"sync"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/clock"
	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/identity"
)

const testRunID = identity.RunID("run_00010203-0405-4607-8809-0a0b0c0d0e0f")
const secondTestRunID = identity.RunID("run_11111111-2222-4333-8444-555555555555")

func TestStoreLifecycle(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	store := NewStore(clock.Fixed{Time: now})
	run, err := store.Create(testRunID, "Build a deterministic kernel")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != string(StateDraft) || run.Version != 1 {
		t.Fatalf("unexpected initial run: %#v", run)
	}
	run, err = store.Transition(testRunID, 1, StateAwaitingApproval)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != string(StateAwaitingApproval) || run.Version != 2 {
		t.Fatalf("unexpected transitioned run: %#v", run)
	}
}

func TestStoreFailsClosed(t *testing.T) {
	t.Parallel()
	store := NewStore(clock.Fixed{Time: time.Now().UTC()})
	if _, err := store.Create(testRunID, "ab"); !fault.IsCode(err, fault.CodeInvalidArgument) {
		t.Fatalf("expected invalid argument, got %v", err)
	}
	if _, err := store.Create(testRunID, "Valid objective"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(testRunID, 1, StateCompleted); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("expected invalid transition conflict, got %v", err)
	}
	if _, err := store.Transition(testRunID, 2, StateAwaitingApproval); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("expected version conflict, got %v", err)
	}
}

func TestStoreCountsObjectiveAsUnicodeCodePoints(t *testing.T) {
	t.Parallel()
	store := NewStore(clock.Fixed{Time: time.Now().UTC()})
	if _, err := store.Create(testRunID, "😀😀"); !fault.IsCode(err, fault.CodeInvalidArgument) {
		t.Fatalf("expected two code points to fail, got %v", err)
	}
	objective := "😀😀😀"
	run, err := store.Create(testRunID, objective)
	if err != nil {
		t.Fatal(err)
	}
	if run.Objective != objective {
		t.Fatalf("objective changed: %q", run.Objective)
	}
}

func TestStoreSerializesConcurrentTransitions(t *testing.T) {
	t.Parallel()
	store := NewStore(clock.Fixed{Time: time.Now().UTC()})
	if _, err := store.Create(testRunID, "Concurrent objective"); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.Transition(testRunID, 1, StateAwaitingApproval)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	successes := 0
	conflicts := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case fault.IsCode(err, fault.CodeConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestTerminalAndTimestamps(t *testing.T) {
	t.Parallel()
	if !Terminal(StateCompleted) || Terminal(StateRunning) {
		t.Fatal("unexpected terminal classification")
	}
	if !ValidTimestamp(time.Now().UTC()) || ValidTimestamp(time.Time{}) {
		t.Fatal("unexpected timestamp validation")
	}
}

func TestMachineDoesNotMoveAggregateTimeBackward(t *testing.T) {
	t.Parallel()
	createdAt := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	machine := NewMachine(clock.Fixed{Time: createdAt.Add(-time.Minute)})
	run := contracts.Run{
		RunID:         testRunID.String(),
		SchemaVersion: "1.0",
		Objective:     "Preserve monotonic aggregate time",
		State:         string(StateDraft),
		Version:       1,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}
	updated, err := machine.Transition(run, StateAwaitingApproval)
	if err != nil {
		t.Fatalf("transition with a regressed clock: %v", err)
	}
	if updated.UpdatedAt != createdAt {
		t.Fatalf("updated_at moved backward: got %s want %s", updated.UpdatedAt, createdAt)
	}
}

func TestCommandMetadataValidation(t *testing.T) {
	t.Parallel()
	valid := CommandMetadata{
		IdempotencyKey: "command-123",
		ActorType:      "agent",
		ActorID:        "co-architect",
		CorrelationID:  "correlation-123",
	}
	if err := ValidateCommandMetadata(valid); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}
	cases := []CommandMetadata{
		{},
		{
			IdempotencyKey: "short",
			ActorType:      "agent",
			ActorID:        "agent",
			CorrelationID:  "correlation",
		},
		{
			IdempotencyKey: "command-123",
			ActorType:      "administrator",
			ActorID:        "agent",
			CorrelationID:  "correlation",
		},
		{
			IdempotencyKey: "command-123",
			ActorType:      "agent",
			CorrelationID:  "correlation",
		},
		{
			IdempotencyKey: "command-123",
			ActorType:      "agent",
			ActorID:        "agent",
			CorrelationID:  "x",
		},
	}
	for _, metadata := range cases {
		if err := ValidateCommandMetadata(metadata); err == nil {
			t.Fatalf("invalid metadata accepted: %#v", metadata)
		}
	}
}

func TestInMemoryRepositoryReplaysSuccessfulCommands(t *testing.T) {
	t.Parallel()
	store := NewStore(clock.Fixed{
		Time: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	})
	createMetadata := testCommandMetadata("create-command-0001")
	created, err := store.CreateRun(
		t.Context(),
		testRunID,
		"Replay this in-memory command",
		createMetadata,
	)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.CreateRun(
		t.Context(),
		secondTestRunID,
		"Replay this in-memory command",
		createMetadata,
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayed != created {
		t.Fatalf("create replay = %#v, want %#v", replayed, created)
	}
	if _, err := store.CreateRun(
		t.Context(),
		secondTestRunID,
		"Different command body",
		createMetadata,
	); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("expected create idempotency conflict, got %v", err)
	}

	transitionMetadata := testCommandMetadata("transition-command-0001")
	transitioned, err := store.TransitionRun(
		t.Context(),
		testRunID,
		1,
		StateAwaitingApproval,
		transitionMetadata,
	)
	if err != nil {
		t.Fatal(err)
	}
	transitionReplay, err := store.TransitionRun(
		t.Context(),
		testRunID,
		1,
		StateAwaitingApproval,
		transitionMetadata,
	)
	if err != nil {
		t.Fatal(err)
	}
	if transitionReplay != transitioned {
		t.Fatalf("transition replay = %#v, want %#v", transitionReplay, transitioned)
	}
	if _, err := store.TransitionRun(
		t.Context(),
		testRunID,
		1,
		StateCancelling,
		transitionMetadata,
	); !fault.IsCode(err, fault.CodeConflict) {
		t.Fatalf("expected transition idempotency conflict, got %v", err)
	}
}

func TestInMemoryRepositoryScopesIdempotencyByCommand(t *testing.T) {
	t.Parallel()
	store := NewStore(clock.Fixed{Time: time.Now().UTC()})
	metadata := testCommandMetadata("shared-command-key")
	if _, err := store.CreateRun(
		t.Context(),
		testRunID,
		"Create with a scoped key",
		metadata,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionRun(
		t.Context(),
		testRunID,
		1,
		StateAwaitingApproval,
		metadata,
	); err != nil {
		t.Fatalf("scoped key rejected across command types: %v", err)
	}
}

func testCommandMetadata(key string) CommandMetadata {
	return CommandMetadata{
		IdempotencyKey: key,
		ActorType:      "agent",
		ActorID:        "co-architect",
		CorrelationID:  key,
	}
}

func FuzzParseState(f *testing.F) {
	f.Add("draft")
	f.Add("unknown")
	f.Fuzz(func(t *testing.T, value string) {
		state, err := ParseState(value)
		if err == nil && string(state) != value {
			t.Fatalf("parse changed state: %q != %q", state, value)
		}
	})
}
