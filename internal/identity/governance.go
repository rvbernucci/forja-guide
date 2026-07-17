package identity

import (
	"crypto/rand"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var (
	sprintIDPattern   = regexp.MustCompile(`^sprint_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	decisionIDPattern = regexp.MustCompile(`^decision_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

// SprintID is the stable public identifier for a Sprint aggregate.
type SprintID string

// DecisionID is the stable public identifier for a governed decision.
type DecisionID string

func NewSprintID() (SprintID, error) {
	return NewSprintIDFrom(rand.Reader)
}

func NewSprintIDFrom(source io.Reader) (SprintID, error) {
	value, err := newGovernanceID("sprint", source)
	return SprintID(value), err
}

func ParseSprintID(value string) (SprintID, error) {
	if !sprintIDPattern.MatchString(value) {
		return "", fmt.Errorf("invalid sprint id %q", value)
	}
	return SprintID(value), nil
}

func (id SprintID) String() string { return string(id) }

func NewDecisionID() (DecisionID, error) {
	return NewDecisionIDFrom(rand.Reader)
}

func NewDecisionIDFrom(source io.Reader) (DecisionID, error) {
	value, err := newGovernanceID("decision", source)
	return DecisionID(value), err
}

func ParseDecisionID(value string) (DecisionID, error) {
	if !decisionIDPattern.MatchString(value) {
		return "", fmt.Errorf("invalid decision id %q", value)
	}
	return DecisionID(value), nil
}

func (id DecisionID) String() string { return string(id) }

// UUID returns the database UUID body while retaining a prefixed public API.
func (id SprintID) UUID() string { return strings.TrimPrefix(id.String(), "sprint_") }

func newGovernanceID(prefix string, source io.Reader) (string, error) {
	runID, err := NewRunIDFrom(source)
	if err != nil {
		return "", err
	}
	return prefix + "_" + strings.TrimPrefix(runID.String(), "run_"), nil
}
