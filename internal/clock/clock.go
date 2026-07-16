// Package clock makes time deterministic in domain tests.
package clock

import "time"

// Clock returns the current time.
type Clock interface {
	Now() time.Time
}

// Real uses the system UTC clock.
type Real struct{}

func (Real) Now() time.Time {
	return time.Now().UTC()
}

// Fixed always returns a configured instant.
type Fixed struct {
	Time time.Time
}

func (f Fixed) Now() time.Time {
	return f.Time.UTC()
}
