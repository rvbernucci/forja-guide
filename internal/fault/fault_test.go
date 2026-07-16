package fault

import (
	"errors"
	"testing"
)

func TestCodeSurvivesWrapping(t *testing.T) {
	t.Parallel()
	root := errors.New("root")
	err := Wrap(CodeUnavailable, "test", "dependency failed", root)
	if !IsCode(err, CodeUnavailable) {
		t.Fatal("expected unavailable code")
	}
	if CodeOf(err) != CodeUnavailable {
		t.Fatalf("unexpected code: %s", CodeOf(err))
	}
	if !errors.Is(err, root) {
		t.Fatal("expected root cause to be preserved")
	}
	if CodeOf(root) != CodeInternal {
		t.Fatal("uncategorized errors must fail as internal")
	}
}
