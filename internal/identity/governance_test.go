package identity

import (
	"bytes"
	"testing"
)

func TestGovernanceIDsAreTypedUUIDv4(t *testing.T) {
	t.Parallel()
	raw := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	sprintID, err := NewSprintIDFrom(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if sprintID.String() != "sprint_00010203-0405-4607-8809-0a0b0c0d0e0f" {
		t.Fatalf("unexpected sprint ID: %s", sprintID)
	}
	if sprintID.UUID() != "00010203-0405-4607-8809-0a0b0c0d0e0f" {
		t.Fatalf("unexpected sprint UUID: %s", sprintID.UUID())
	}
	if _, err := ParseSprintID(sprintID.String()); err != nil {
		t.Fatal(err)
	}
	decisionID, err := NewDecisionIDFrom(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if decisionID.String() != "decision_00010203-0405-4607-8809-0a0b0c0d0e0f" {
		t.Fatalf("unexpected decision ID: %s", decisionID)
	}
	if _, err := ParseDecisionID(decisionID.String()); err != nil {
		t.Fatal(err)
	}
}
