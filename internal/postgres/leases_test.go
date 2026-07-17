package postgres

import (
	"testing"
	"time"
)

func TestIntervalStringPreservesSupportedTTLRange(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{
			name:     "minimum",
			duration: time.Millisecond,
			want:     "0 seconds 1000 microseconds",
		},
		{
			name:     "fractional second",
			duration: time.Second + 234567*time.Microsecond + 999*time.Nanosecond,
			want:     "1 seconds 234567 microseconds",
		},
		{
			name:     "maximum lease",
			duration: 24 * time.Hour,
			want:     "86400 seconds 0 microseconds",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := intervalString(test.duration); got != test.want {
				t.Fatalf("intervalString(%s) = %q, want %q", test.duration, got, test.want)
			}
		})
	}
}
