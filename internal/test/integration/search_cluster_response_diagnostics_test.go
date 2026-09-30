package integration

import (
	"net/http"
	"testing"
	"time"

	"goodkind.io/tack/internal/testenv"
)

type clusterFailureCallbacks struct {
	Public func(error)
	Worker func(error)
	Finish func()
}

func clusterPredictionDiagnostics(t *testing.T, cluster *testenv.OpenSearchCluster, fixture queryFixture, modelID, member string, stoppedAt time.Time) (func(), func()) {
	t.Helper()
	observed := 0
	breakerCaptured := false
	var endedAt time.Time
	matched := 0
	observe := func() {
		responses := cluster.PredictionResponses(t, modelID)
		collectedAt := time.Now().UTC()
		for _, response := range responses[observed:] {
			startedAt, err := time.Parse(time.RFC3339Nano, response.StartedAt)
			clusterRequire(t, "parse proxy prediction timestamp", err)
			if startedAt.Before(stoppedAt) || (!endedAt.IsZero() && startedAt.After(endedAt)) {
				continue
			}
			matched++
			completedAt := startedAt.Add(time.Duration(response.Duration))
			t.Logf("proxy prediction member=%s backend=%s method=%s status=%d origin_status=%d request_at=%s completed_at=%s observed_at=%s collection_delay=%s", member, response.Backend, response.Method, response.Status, response.OriginStatus, response.StartedAt, completedAt.Format(time.RFC3339Nano), collectedAt.Format(time.RFC3339Nano), collectedAt.Sub(completedAt))
			if response.Status == http.StatusTooManyRequests && !breakerCaptured {
				breakerCaptured = true
				t.Logf("first observed HTTP 429 resource capture member=%s request_at=%s observed_at=%s metrics_started_at=%s", member, response.StartedAt, collectedAt.Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
				captureSearchFailure(t, fixture, modelID, cluster.Members()...)
			}
		}
		observed = len(responses)
	}
	finish := func() {
		if endedAt.IsZero() {
			endedAt = time.Now().UTC()
		}
		observe()
		t.Logf("final proxy prediction collection member=%s interval_end=%s observed_at=%s records=%d captured_429=%t; pending buffered records are unavailable in this snapshot", member, endedAt.Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), matched, breakerCaptured)
	}
	return observe, finish
}

func logClusterRestartPlacement(t *testing.T, cluster *testenv.OpenSearchCluster, fixture queryFixture, modelID, member string) {
	t.Helper()
	t.Logf("cluster green after member restart member=%s observed_at=%s backends=%v", member, time.Now().UTC().Format(time.RFC3339Nano), cluster.Backends(t))
	clusterMLResponse(t, fixture, http.MethodGet, clusterModelRecordRequest{modelID: modelID})
	profile, status := clusterMLResponse(t, fixture, http.MethodGet, clusterModelProfileRequest{modelID: modelID})
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		logClusterModelWorkers(t, profile, modelID, cluster, fixture, "")
	}
}
