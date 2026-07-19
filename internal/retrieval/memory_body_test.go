package retrieval

import "testing"

func TestPrepareMemoryBodyBoundsAndNormalizes(t *testing.T) {
	got, err := PrepareMemoryBody("text/plain", []byte("  approved\n memory  Authorization: Bearer unsafe-token-value\nAKIA1234567890ABCDEF hf_12345678901234567890 sk-12345678901234567890"))
	if err != nil || got != "approved memory Authorization: Bearer [REDACTED] [REDACTED] [REDACTED] [REDACTED]" {
		t.Fatalf("body=%q err=%v", got, err)
	}
	if _, err := PrepareMemoryBody("application/json", []byte("{}")); err == nil {
		t.Fatal("unapproved media type accepted")
	}
	if _, err := PrepareMemoryBody("text/plain", nil); err == nil {
		t.Fatal("empty body accepted")
	}
}
