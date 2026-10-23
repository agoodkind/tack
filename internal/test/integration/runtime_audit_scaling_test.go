//go:build runtimeaudit

package integration_test

import (
	"strconv"
	"strings"
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
// one app instance count.
type scalingResult struct {
	label           string
	instances       int
	requests        int
	ledgerEvents    int64
	duration        time.Duration
	latencies       []time.Duration
	backlog         int
	yugabyteBacklog int64
	recovery        time.Duration
	databaseBytes   int64
	brokerRecords   int64
	brokerBytes     int64
	replicaBytes    int64
}

// TestRuntimeAuditScaling runs the same generated MCP workload at each
// configured app instance count. Each run shares one FoundationDB prefix and
// ledger across its instances. The test reports workload, audit recovery, and
// storage measurements and requires request and ledger event counts to match
// the one-instance run. It also checks broker record and byte limits and
// repeats the two-instance workload with a relay crash.
func TestRuntimeAuditScaling(t *testing.T) {
	results := make([]scalingResult, 0, len(scalingInstanceCounts)+1)
	for _, instances := range scalingInstanceCounts {
		results = append(results, runScalingWorkload(t, strconv.Itoa(instances), instances, 0))
	}
	results = append(results, runScalingWorkload(t, scalingTakeoverLabel, scalingTakeoverInstances, scalingCrashAfter))
	baseline := results[0]
	for _, result := range results[1:] {
		if result.requests != baseline.requests || result.ledgerEvents != baseline.ledgerEvents {
			t.Errorf("The workload recorded different request or ledger event counts from the expected one-instance baseline with %s instances: %d requests %d events; 1 instance: %d requests %d events",
				result.label, result.requests, result.ledgerEvents, baseline.requests, baseline.ledgerEvents)
		}
		requireFlatBroker(t, baseline, result)
	}
	var table strings.Builder
	table.WriteString("\nThe table compares MCP workload and audit recovery measurements across app instance counts.\n\nScaling results:\n| Instances | Requests | Ledger events | Throughput (requests/s) | Latency (50th percentile) | Latency (95th percentile) | Latency (99th percentile) | Outbox entries | Audit recovery time | Ledger row bytes | Broker records | Broker bytes | Index bytes | FoundationDB key and value bytes |\n")
	table.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, result := range results {
		table.WriteString(result.row())
	}
	t.Log(table.String())
}
