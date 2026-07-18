package postgres

import (
	"testing"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

func TestReplayAcceptsLegacyResumeWithoutReopeningRuntimeTransition(t *testing.T) {
	createdAt := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Second)
	previous := contracts.Run{
		RunID: "run_11111111-1111-4111-8111-111111111111", Objective: "legacy resume",
		State: string(runstate.StateAwaitingDecision), Version: 5,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	resumed := previous
	resumed.State = string(runstate.StateRunning)
	resumed.Version = 6
	resumed.UpdatedAt = updatedAt

	if err := validateReplayedRunEvent(
		resumed.RunID, resumed.Version, "event_legacy_resume", "run.transitioned",
		updatedAt, resumed, previous, true,
	); err != nil {
		t.Fatalf("legacy resume replay: %v", err)
	}
	if runstate.CanTransition(runstate.StateAwaitingDecision, runstate.StateRunning) {
		t.Fatal("legacy replay compatibility reopened the runtime transition")
	}

	invalid := resumed
	invalid.State = string(runstate.StatePreparing)
	if err := validateReplayedRunEvent(
		invalid.RunID, invalid.Version, "event_invalid_resume", "run.transitioned",
		updatedAt, invalid, previous, true,
	); err == nil {
		t.Fatal("replay compatibility accepted an unrelated legacy transition")
	}
}
