// Package identity creates and validates typed Forja identifiers.
package identity

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
)

var runIDPattern = regexp.MustCompile(
	`^run_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

// RunID is the stable identifier for a run aggregate.
type RunID string

// NewRunID creates a cryptographically random UUIDv4-prefixed run ID.
func NewRunID() (RunID, error) {
	return NewRunIDFrom(rand.Reader)
}

// NewRunIDFrom creates a run ID from a supplied entropy source.
func NewRunIDFrom(source io.Reader) (RunID, error) {
	var raw [16]byte
	if _, err := io.ReadFull(source, raw[:]); err != nil {
		return "", fmt.Errorf("read run id entropy: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80

	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], raw[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], raw[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], raw[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], raw[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], raw[10:16])

	return RunID("run_" + string(encoded)), nil
}

// ParseRunID validates and returns a run ID.
func ParseRunID(value string) (RunID, error) {
	if !runIDPattern.MatchString(value) {
		return "", fmt.Errorf("invalid run id %q", value)
	}
	return RunID(value), nil
}

func (id RunID) String() string {
	return string(id)
}
