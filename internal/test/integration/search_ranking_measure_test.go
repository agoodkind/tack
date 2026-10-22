package integration_test

import (
	"slices"
	"testing"
)

// measure runs one search for the probe query and follows its cursor to the last
// page. It logs each target's rank in the full result order with the phase and
// query. A rank of 0 means the search did not return the target.
// It does not fail the test for a rank. It fails the test only when the search
// does not complete within rankingMaxPages pages.
func (r *rankingRun) measure(t *testing.T, phase string, probe rankingProbe) {
	t.Helper()
	ranks := make([]int, len(probe.Targets))
	page := r.search(t, probe.Query, "")
	offset := 0
	for range rankingMaxPages {
		for position, target := range probe.Targets {
			if index := slices.Index(page.IDs, target); index >= 0 {
				ranks[position] = offset + index + 1
			}
		}
		if page.Complete {
			t.Logf("ranking measurement phase=%s query=%q ranks=%v", phase, probe.Query, ranks)
			return
		}
		offset += len(page.IDs)
		page = r.search(t, probe.Query, page.Cursor)
	}
	t.Fatalf("measurement of %q did not complete within %d pages", probe.Query, rankingMaxPages)
}
