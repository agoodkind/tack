//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
)

// errToolFailed marks a tool response that cannot supply the expected result.
var errToolFailed = errors.New("tool response does not provide the expected result")

// successfulToolText returns the first content item's text when the response
// has HTTP status 200 and a readable JSON result with content. It returns
// errToolFailed if those checks fail or the tool reports an error.
func successfulToolText(response publicResponse) (string, error) {
	var decoded struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if response.status != http.StatusOK || json.Unmarshal(response.body, &decoded) != nil ||
		decoded.Result.IsError || len(decoded.Result.Content) == 0 {
		return "", fmt.Errorf("%w: HTTP status %d; response body %s", errToolFailed, response.status, response.body)
	}
	return decoded.Result.Content[0].Text, nil
}

// rawIDOf extracts the value after the last Raw id marker in tool text. It
// returns errToolFailed if the marker or closing backtick is missing.
func rawIDOf(text string) (string, error) {
	const marker = "- Raw id: `"
	start := strings.LastIndex(text, marker)
	if start == -1 {
		return "", fmt.Errorf("tool result does not contain a Raw id marker with a closing backtick: %w; result %s", errToolFailed, text)
	}
	remainder := text[start+len(marker):]
	end := strings.IndexByte(remainder, '`')
	if end == -1 {
		return "", fmt.Errorf("tool result does not contain a Raw id marker with a closing backtick: %w; result %s", errToolFailed, text)
	}
	return remainder[:end], nil
}

// measure counts FoundationDB entries and YugabyteDB rows plus parts before
// starting audit workers. It times recovery until the FoundationDB outbox
// empties and the ledger count stabilizes. It then records ledger events,
// broker records, and database, broker, and FoundationDB byte measurements.
func (run *scalingRun) measure(t *testing.T) {
	t.Helper()
	run.result.backlog = run.outboxEntries(t)
	admin := ledgerAdminPool(t)
	if err := admin.QueryRow(t.Context(), readTestSQL(t, "runtime_audit_outbox_records.sql")).Scan(&run.result.yugabyteBacklog); err != nil {
		t.Fatalf("Counting YugabyteDB operator outbox rows and parts failed: %v", err)
	}
	topic := "audit.scaling-test-" + uuid.NewString()[:8]
	started := clock.Now()
	run.startWorkers(t, topic)
	deadline := scalingDeadline()
	for run.outboxEntries(t) > 0 && clock.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	run.result.ledgerEvents, run.result.databaseBytes = run.waitForLedger(t, admin)
	run.result.recovery = clock.Since(started)
	run.result.brokerBytes = topicBytes(t, topic)
	run.result.brokerRecords = topicRecords(t, topic)
	run.result.replicaBytes = run.prefixBytes(t)
}

// outboxEntries counts the FoundationDB outbox entries under the run's prefix,
// reading batches until the outbox returns an empty batch.
func (run *scalingRun) outboxEntries(t *testing.T) int {
	t.Helper()
	stores, err := fdbadapter.NewStores(run.server.clusterFile, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("Opening FoundationDB stores failed; backlog measurement requires the outbox store: %v", err)
	}
	count := 0
	var mark []byte
	for {
		entries, err := stores.OpsOutbox.ReadOutboxFrom(t.Context(), mark, 1000)
		if err != nil {
			t.Fatalf("Reading the FoundationDB outbox failed; backlog measurement requires every batch: %v", err)
		}
		if len(entries) == 0 {
			return count
		}
		count += len(entries)
		mark = entries[len(entries)-1].Mark
	}
}

// waitForLedger polls the organization's ledger event count and row bytes until
// two consecutive samples have the same positive event count. It returns those
// measurements or fails the test when the deadline expires.
func (run *scalingRun) waitForLedger(t *testing.T, admin *pgxpool.Pool) (int64, int64) {
	t.Helper()
	query := readTestSQL(t, "runtime_audit_scaling_org_events.sql")
	var previous, events, size int64
	deadline := scalingDeadline()
	for clock.Now().Before(deadline) {
		if err := admin.QueryRow(t.Context(), query, run.server.orgID).Scan(&events, &size); err != nil {
			t.Fatalf("Reading the organization's ledger event count and row bytes failed; recovery measurement requires both values: %v", err)
		}
		if events > 0 && events == previous {
			return events, size
		}
		previous = events
		time.Sleep(time.Second)
	}
	t.Fatalf("The ledger event count did not become positive and stable before the deadline for organization %s: %d events", run.server.orgID, events)
	return 0, 0
}

// row formats the run's measurements as a Markdown table row. It calculates
// throughput and latency percentiles and marks index bytes as unmeasured.
func (result scalingResult) row() string {
	latencies := slices.Clone(result.latencies)
	slices.Sort(latencies)
	percentile := func(fraction float64) time.Duration {
		if len(latencies) == 0 {
			return 0
		}
		return latencies[int(fraction*float64(len(latencies)-1))]
	}
	throughput := float64(result.requests) / result.duration.Seconds()
	return fmt.Sprintf("| %s | %d | %d | %.1f | %s | %s | %s | %d | %s | %d | %d | %d | not measured | %d |\n",
		result.label, result.requests, result.ledgerEvents, throughput, percentile(0.5), percentile(0.95), percentile(0.99),
		result.backlog, result.recovery.Round(time.Millisecond), result.databaseBytes, result.brokerRecords, result.brokerBytes,
		result.replicaBytes)
}
