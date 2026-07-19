package retrieval

import (
	"strings"
	"testing"
)

func TestPrepareMemoryBodyBoundsAndNormalizes(t *testing.T) {
	body := "  approved\n memory  Authorization: Bearer unsafe-token-value\nAKIA1234567890ABCDEF " +
		"hf_" + strings.Repeat("1", 20) + " sk-" + strings.Repeat("1", 20)
	got, err := PrepareMemoryBody("text/plain", []byte(body))
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
