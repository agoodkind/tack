//go:build runtimeaudit

package integration_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditrelay"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

const (
	// scalingRelayBatch limits the number of entries in each relay outbox read.
	scalingRelayBatch = 64
	// scalingLeaseDuration sets the FoundationDB lease duration for the scaling
	// relays.
	scalingLeaseDuration = 2 * time.Second
	// scalingTakeoverLabel labels the crash run.
	scalingTakeoverLabel = "2 with a relay crash"
	// scalingTakeoverInstances sets the app instance count for the relay crash
	// run.
	scalingTakeoverInstances = 2
	// scalingCrashAfter sets the acknowledged record count at which the first
	// relay crashes.
	scalingCrashAfter = 100
	// scalingBrokerTolerance sets the multiplier for the one-instance broker byte
	// baseline.
	scalingBrokerTolerance = 1.1
)

// errRelayCrashed stops the current batch after the test cancels the relay
// context.
var errRelayCrashed = errors.New("relay crashed after the configured record count")

// startWorkers starts one relay and one audit consumer per app instance on
// topic. The consumers share one Kafka group. When crashAfter is positive, it
// waits for the first relay to publish a record before starting the remaining
// relays.
func (run *scalingRun) startWorkers(t *testing.T, topic string) {
	t.Helper()
	group := "tack-scaling-test-" + uuid.NewString()[:8]
	for range run.result.instances {
		startScalingConsumer(t, topic, group)
	}
	first := 0
	if run.crashAfter > 0 {
		first = 1
		run.startCrashingRelay(t, topic)
	}
	for range run.result.instances - first {
		run.openRelay(t, topic, nil).Start(t.Context())
	}
}

// startCrashingRelay starts a relay that cancels its context after crashAfter
// broker acknowledgments. It waits for the relay to publish its first record
// before returning.
func (run *scalingRun) startCrashingRelay(t *testing.T, topic string) {
	t.Helper()
	relayContext, crash := context.WithCancel(t.Context())
	t.Cleanup(crash)
	var produced atomic.Int64
	run.openRelay(t, topic, func(context.Context, uuid.UUID) error {
		if produced.Add(1) < int64(run.crashAfter) {
			return nil
		}
		crash()
		return errRelayCrashed
	}).Start(relayContext)
	deadline := scalingDeadline()
	for produced.Load() == 0 && clock.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if produced.Load() == 0 {
		t.Fatal("The first relay did not publish a record before the deadline")
	}
}

// startScalingConsumer starts an audit consumer for topic in group and
// registers cleanup to close it.
func startScalingConsumer(t *testing.T, topic, group string) {
	t.Helper()
	consumer, err := audit.NewConsumer(t.Context(), audit.ConsumerConfig{
		Brokers: []string{testenv.Kafka(t)}, Topic: topic, GroupID: group,
		BatchSize: 32, PollInterval: 100 * time.Millisecond, YugabyteDSN: testenv.Ledger(t),
	})
	if err != nil {
		t.Fatalf("Creating an audit consumer failed; the scaling run requires one consumer per app instance: %v", err)
	}
	consumer.Start(t.Context())
	t.Cleanup(func() { _ = consumer.Close() })
}

// openRelay opens a relay with the run's lease duration and optional
// afterProduce callback. Test cleanup closes the relay and its YugabyteDB pool.
func (run *scalingRun) openRelay(t *testing.T, topic string, afterProduce func(context.Context, uuid.UUID) error) *audit.Relay {
	t.Helper()
	opened, err := auditrelay.Open(t.Context(), auditrelay.Config{
		Brokers: []string{testenv.Kafka(t)}, Topic: topic, YugabyteDSN: testenv.Ledger(t),
		FDBClusterFile: run.server.clusterFile, FDBTransactionTimeout: 5 * time.Second,
		PollInterval: 100 * time.Millisecond, BatchSize: scalingRelayBatch,
		LeaseDuration: scalingLeaseDuration, AfterProduce: afterProduce,
	})
	if err != nil {
		t.Fatalf("Opening an audit relay failed; the scaling run requires one relay per app instance: %v", err)
	}
	t.Cleanup(opened.Pool.Close)
	t.Cleanup(func() { _ = opened.Relay.Close() })
	return opened.Relay
}

// requireFlatBroker checks broker record counts against both outbox backlogs
// and broker bytes against the one-instance run. It allows only the
// takeoverRecords records that a relay takeover republishes.
func requireFlatBroker(t *testing.T, baseline, result scalingResult, takeoverRecords int64) {
	t.Helper()
	extraRecords := takeoverRecords
	published := int64(result.backlog) + result.yugabyteBacklog
	if result.brokerRecords < published || result.brokerRecords > published+extraRecords {
		t.Errorf("The broker record count is outside the expected range for run %s: %d records for %d FoundationDB and %d YugabyteDB outbox entries; allowed extra %d",
			result.label, result.brokerRecords, result.backlog, result.yugabyteBacklog, extraRecords)
	}
	perRecord := float64(baseline.brokerBytes) / float64(max(baseline.brokerRecords, 1))
	limit := float64(baseline.brokerBytes)*scalingBrokerTolerance + perRecord*float64(extraRecords)
	if float64(result.brokerBytes) > limit {
		t.Errorf("The broker byte count exceeds the allowed limit for run %s: %d bytes; baseline %d bytes; limit %.0f bytes", result.label, result.brokerBytes, baseline.brokerBytes, limit)
	}
}
