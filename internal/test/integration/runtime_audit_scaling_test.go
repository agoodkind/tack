//go:build runtimeaudit

package integration_test

import (
	"strconv"
	"testing"
	"time"
)

// scalingInstanceCounts sets the app instance counts for the scaling runs.
var scalingInstanceCounts = []int{1, 2, 4}

const (
	// scalingProjects sets the number of projects in each scaling workload.
	scalingProjects = 4
	// scalingIssuesPerProject sets the number of issue creation, read, and update
	// jobs per project.
	scalingIssuesPerProject = 15
	// scalingClients sets the number of concurrent workers that run issue jobs,
	// regardless of the app instance count.
	scalingClients = 8
	// scalingDrainTimeout limits each wait for the outbox to empty or the ledger
	// event count to stabilize.
	scalingDrainTimeout = 5 * time.Minute
)

// scalingResult stores workload, audit recovery, and storage measurements for
// one app instance count. drained counts the YugabyteDB outbox records that
// earlier tests left and that the run relayed to a separate topic before its
// workload.
type scalingResult struct {
	label            string
	instances        int
	requests         int
	ledgerEvents     int64
	duration         time.Duration
	latencies        []time.Duration
	backlog          int
	yugabyteBacklog  int64
	drained          int64
	recovery         time.Duration
	databaseBytes    int64
	ledgerIndexBytes int64
	ledgerTotalBytes int64
	eventBytes       float64
	partBytes        float64
	brokerRecords    int64
	brokerBytes      int64
	fdbBytes         int64
	replication      int64
	replicaBytes     int64
	receipts         int64
	receiptBytes     float64
	takeovers        int64
}

// TestRuntimeAuditScaling runs the same generated MCP workload at each
// configured app instance count. Each run shares one FoundationDB prefix and
// ledger across its instances. The test reports workload, audit recovery, and
// storage measurements and requires request and ledger event counts to match
// the one-instance run. It checks the broker record count of every run,
// including the one-instance run, against the outbox records of that run and
// checks broker byte limits. It also repeats the two-instance workload with a
// relay crash.
func TestRuntimeAuditScaling(t *testing.T) {
	results := make([]scalingResult, 0, len(scalingInstanceCounts)+1)
	for _, instances := range scalingInstanceCounts {
		results = append(results, runScalingWorkload(t, strconv.Itoa(instances), instances, 0))
	}
	baseline := results[0]
	for _, result := range results {
		requireScalingRun(t, baseline, result, 0)
	}
	takeover := runScalingWorkload(t, scalingTakeoverLabel, scalingTakeoverInstances, scalingCrashAfter)
	requireScalingRun(t, baseline, takeover, takeover.yugabyteBacklog)
	results = append(results, takeover)
	t.Log(scalingTables(results))
}

// requireScalingRun requires result to match the request and ledger event
// counts of baseline and to keep broker records and bytes within the
// requireFlatBroker limits.
func requireScalingRun(t *testing.T, baseline, result scalingResult, rollbackRecords int64) {
	t.Helper()
	if result.requests != baseline.requests || result.ledgerEvents != baseline.ledgerEvents {
		t.Errorf("The workload recorded different request or ledger event counts from the expected one-instance baseline with %s instances: %d requests %d events; 1 instance: %d requests %d events",
			result.label, result.requests, result.ledgerEvents, baseline.requests, baseline.ledgerEvents)
	}
	requireFlatBroker(t, baseline, result, rollbackRecords)
}
