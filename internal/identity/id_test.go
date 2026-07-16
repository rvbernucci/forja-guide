package identity

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewRunIDFromProducesUUIDv4(t *testing.T) {
	t.Parallel()
	source := bytes.NewReader([]byte{
		0, 1, 2, 3, 4, 5, 6, 7,
		8, 9, 10, 11, 12, 13, 14, 15,
	})
	id, err := NewRunIDFrom(source)
	if err != nil {
		t.Fatal(err)
	}
	const expected = "run_00010203-0405-4607-8809-0a0b0c0d0e0f"
	if id.String() != expected {
		t.Fatalf("got %s, want %s", id, expected)
	}
	if _, err := ParseRunID(id.String()); err != nil {
		t.Fatal(err)
	}
}

func TestNewRunIDFromRejectsShortEntropy(t *testing.T) {
	t.Parallel()
	if _, err := NewRunIDFrom(strings.NewReader("short")); err == nil {
		t.Fatal("expected short entropy to fail")
	}
}

func FuzzParseRunID(f *testing.F) {
	f.Add("run_00010203-0405-4607-8809-0a0b0c0d0e0f")
	f.Add("")
	f.Fuzz(func(t *testing.T, value string) {
		id, err := ParseRunID(value)
		if err == nil && id.String() != value {
			t.Fatalf("parse changed value: %q != %q", id, value)
		}
	})
}
