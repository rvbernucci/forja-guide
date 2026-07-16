package runstate

import (
	"sync"
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/clock"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/identity"
)

const testRunID = identity.RunID("run_00010203-0405-4607-8809-0a0b0c0d0e0f")

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
