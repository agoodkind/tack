package datagen

import (
	"testing"
	"time"
)

// TestNearestRankPercentiles checks the nearest-rank p50 and p95 for samples
// from 1 to n milliseconds. An implementation that always returns the minimum
// or maximum fails at least one case.
func TestNearestRankPercentiles(t *testing.T) {
	cases := []struct {
		samples  int
		wantP50  time.Duration
		wantP95  time.Duration
	}{
		{samples: 1, wantP50: 1 * time.Millisecond, wantP95: 1 * time.Millisecond},
		{samples: 2, wantP50: 1 * time.Millisecond, wantP95: 2 * time.Millisecond},
		{samples: 20, wantP50: 10 * time.Millisecond, wantP95: 19 * time.Millisecond},
		{samples: 21, wantP50: 11 * time.Millisecond, wantP95: 20 * time.Millisecond},
	}
	for _, tc := range cases {
		sorted := make([]time.Duration, 0, tc.samples)
		for value := 1; value <= tc.samples; value++ {
			sorted = append(sorted, time.Duration(value)*time.Millisecond)
		}
		if got := nearestRank(sorted, 50); got != tc.wantP50 {
			t.Fatalf("p50 of %d samples = %s, want %s", tc.samples, got, tc.wantP50)
		}
		if got := nearestRank(sorted, 95); got != tc.wantP95 {
			t.Fatalf("p95 of %d samples = %s, want %s", tc.samples, got, tc.wantP95)
		}
	}
}
