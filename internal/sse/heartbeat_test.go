package sse

import (
	"math"
	"testing"
	"time"
)

func TestClampHeartbeat(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero uses default", 0, defaultHeartbeat},
		{"negative uses default", -time.Second, defaultHeartbeat},
		{"sub-second kept for the test seam", 20 * time.Millisecond, 20 * time.Millisecond},
		{"normal cadence kept", 15 * time.Second, 15 * time.Second},
		{"upper bound kept", maxHeartbeat, maxHeartbeat},
		{"ms-for-s confusion clamped", 15000 * time.Second, defaultHeartbeat},
		{"overflowed duration clamped", time.Duration(math.MaxInt64), defaultHeartbeat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampHeartbeat(tc.in); got != tc.want {
				t.Fatalf("clampHeartbeat(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
