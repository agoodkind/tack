package integration_test

import "testing"

// searchMixedFoundationDBSample is the FoundationDB cluster health of one search-mixed sample.
type searchMixedFoundationDBSample struct {
	PerformanceLimitedBy string   `json:"performance_limited_by"`
	CommitSeconds        *float64 `json:"commit_seconds"`
}

// requireSearchMixedFoundationDB fails the test unless at least one sample reports the
// FoundationDB performance limit and a non-negative commit latency.
func requireSearchMixedFoundationDB(t *testing.T, output []byte, envelope searchMixedOutput) {
	t.Helper()
	for _, sample := range envelope.Result.Samples {
		health := sample.FoundationDB
		if health != nil && health.PerformanceLimitedBy != "" && health.CommitSeconds != nil && *health.CommitSeconds >= 0 {
			return
		}
	}
	t.Fatalf("qa datagen search-mixed: at least one sample must report the FoundationDB performance limit and a non-negative commit latency: %s", output)
}
