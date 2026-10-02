package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
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
				captureFirstCluster429(t, cluster, fixture, member)
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

func captureFirstCluster429(t *testing.T, cluster *testenv.OpenSearchCluster, fixture queryFixture, stoppedMember string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 3*time.Second)
	defer cancel()
	var stats struct {
		Nodes map[string]struct {
			Name string `json:"name"`
			JVM  struct {
				Mem struct {
					HeapUsedInBytes      int64 `json:"heap_used_in_bytes"`
					HeapUsedPercent      int   `json:"heap_used_percent"`
					HeapCommittedInBytes int64 `json:"heap_committed_in_bytes"`
					HeapMaxInBytes       int64 `json:"heap_max_in_bytes"`
				} `json:"mem"`
			} `json:"jvm"`
			Breakers map[string]struct {
				EstimatedSizeInBytes int64 `json:"estimated_size_in_bytes"`
				LimitSizeInBytes     int64 `json:"limit_size_in_bytes"`
				Tripped              int64 `json:"tripped"`
			} `json:"breakers"`
		} `json:"nodes"`
	}
	response, err := opensearch.Do(ctx, fixture.Client.Client, http.MethodGet,
		opensearchapi.NodesStatsReq{Metric: []string{"jvm", "breaker"}}, &stats)
	if err != nil || response == nil {
		t.Logf("first HTTP 429 JVM and breaker snapshot failed at=%s error=%v", time.Now().UTC().Format(time.RFC3339Nano), err)
	} else if response.IsError() {
		t.Logf("first HTTP 429 JVM and breaker snapshot rejected at=%s status=%d error=%v", time.Now().UTC().Format(time.RFC3339Nano), response.StatusCode, opensearch.ParseError(response))
	} else {
		for nodeID, node := range stats.Nodes {
			if node.Name == stoppedMember {
				continue
			}
			t.Logf("first HTTP 429 survivor node=%s id=%s observed_at=%s heap_used=%d heap_used_percent=%d heap_committed=%d heap_max=%d breakers=%v", node.Name, nodeID, time.Now().UTC().Format(time.RFC3339Nano), node.JVM.Mem.HeapUsedInBytes, node.JVM.Mem.HeapUsedPercent, node.JVM.Mem.HeapCommittedInBytes, node.JVM.Mem.HeapMaxInBytes, node.Breakers)
		}
	}
	for _, member := range cluster.Members() {
		if member == stoppedMember {
			continue
		}
		requestedAt := time.Now().UTC().Format(time.RFC3339Nano)
		logs, logErr := cluster.MemberLogTail(t.Context(), member)
		if logs == "" {
			logs = "[no log lines available]"
		}
		t.Logf("first HTTP 429 survivor logs member=%s requested_at=%s error=%v\n%s", member, requestedAt, logErr, logs)
	}
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
