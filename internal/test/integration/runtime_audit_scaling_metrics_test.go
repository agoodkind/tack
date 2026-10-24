//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

// call selects an app handler by taking the index modulo the handler count and
// sends an MCP tool call. It records the request count and response latency,
// then returns the tool text or a response error.
func (run *scalingRun) call(t *testing.T, index int, tool string, arguments map[string]any) (string, error) {
	t.Helper()
	server := *run.server
	server.handler = run.handlers[index%len(run.handlers)]
	started := clock.Now()
	response := server.rpc(t, newRequestID(t, "scaling"), "tools/call", map[string]any{"name": tool, "arguments": arguments})
	latency := clock.Since(started)
	run.mu.Lock()
	run.result.latencies = append(run.result.latencies, latency)
	run.result.requests++
	run.mu.Unlock()
	return successfulToolText(response)
}

// mustCall returns the tool text from call and fails the test if call returns
// an error.
func (run *scalingRun) mustCall(t *testing.T, index int, tool string, arguments map[string]any) string {
	t.Helper()
	text, err := run.call(t, index, tool, arguments)
	if err != nil {
		t.Fatalf("The scaling workload received an error instead of a successful result from tool %s: %v", tool, err)
	}
	return text
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
// broker records, lease takeovers, and database, broker, and FoundationDB byte
// measurements. Each lease acquisition after the first is a takeover. The
// YugabyteDB audit table sizes are differences from the sizes that
// startScalingRun read before the workload.
func (run *scalingRun) measure(t *testing.T) {
	t.Helper()
	run.result.backlog = run.outboxEntries(t)
	admin := ledgerAdminPool(t)
	run.result.yugabyteBacklog = outboxRecordCount(t, admin)
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
	run.result.takeovers = max(run.leaseAcquisitions.Load()-1, 0)
	after := readLedgerStorage(t, admin)
	run.result.ledgerIndexBytes = after.indexBytes - run.storageBefore.indexBytes
	run.result.ledgerTotalBytes = after.totalBytes - run.storageBefore.totalBytes
	run.result.eventBytes = perUnit(after.eventTableBytes, after.events)
	run.result.partBytes = perUnit(after.partTableBytes, after.parts)
	run.measureFoundationDB(t)
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
