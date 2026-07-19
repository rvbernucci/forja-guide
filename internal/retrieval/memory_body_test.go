package retrieval

import "testing"

func TestPrepareMemoryBodyBoundsAndNormalizes(t *testing.T) {
	got, err := PrepareMemoryBody("text/plain", []byte("  approved\n memory  "))
	if err != nil || got != "approved memory" {
		t.Fatalf("body=%q err=%v", got, err)
	}
	if _, err := PrepareMemoryBody("application/json", []byte("{}")); err == nil {
		t.Fatal("unapproved media type accepted")
	}
	if _, err := PrepareMemoryBody("text/plain", nil); err == nil {
		t.Fatal("empty body accepted")
	}
}
