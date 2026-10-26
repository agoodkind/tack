package integration_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	// concurrentDescriptionWriters is the number of tack_update_issue calls
	// that change the same description at the same time.
	concurrentDescriptionWriters = 8
	// concurrentInitialDescription is the description of the issue before the
	// concurrent updates.
	concurrentInitialDescription = "initial description"
	// descriptionPath is the field path of the description property.
	descriptionPath = "/props/description"
)

// concurrentDescriptionRun is the outcome of concurrent description updates
// of one issue: the descriptions of the successful calls, the description the
// issue stores afterwards, the description change rows in commit version
// order, and the `audit last-change` report.
type concurrentDescriptionRun struct {
	ledger  *nodeAuditLedger
	orgID   uuid.UUID
	issueID uuid.UUID
	written []string
	final   string
	changes []audit.Row
	report  lastChangeReport
}

// runConcurrentDescriptionUpdates creates an issue through the audited MCP
// harness and sends concurrentDescriptionWriters concurrent updates that set
// distinct descriptions. It waits for one change row per successful update and
// runs `audit last-change` for the issue.
func runConcurrentDescriptionUpdates(t *testing.T) concurrentDescriptionRun {
	t.Helper()
	harness := integration.NewAuditedMCPHarness(t)
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), nodeAuditTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	windowStart := clock.Now().Add(-time.Minute)
	create := auditedIssueArguments(harness)
	create.ProjectReference = harness.Project
	create.Name = "concurrent last change issue"
	create.Properties = datagen.NodeProperties{"description": quoted(concurrentInitialDescription)}
	issueID := uuid.MustParse(harness.Call(t, "tack_create_issue", create).RawID())
	written := writeDescriptionsConcurrently(t, harness, issueID)
	view, err := stores.Views.Get(t.Context(), issueID)
	if err != nil || view == nil {
		t.Fatalf("issue %s after the concurrent updates = %v, %v", issueID, view, err)
	}
	run := concurrentDescriptionRun{
		ledger: startNodeAuditLedger(t), orgID: view.OrgID, issueID: issueID, written: written,
		final: decodedJSONString(t, view.Props["description"]), changes: nil, report: lastChangeReport{},
	}
	run.changes = waitForDescriptionChanges(t, run, len(written))
	run.report = runLastChange(t, run.ledger, run.orgID, issueID, windowStart, clock.Now().Add(time.Minute))
	return run
}

// writeDescriptionsConcurrently sends the updates at the same time and
// returns the descriptions of the successful updates. At least two updates
// must succeed.
func writeDescriptionsConcurrently(t *testing.T, harness *integration.MCPHarness, issueID uuid.UUID) []string {
	t.Helper()
	descriptions := make([]string, concurrentDescriptionWriters)
	failures := make([]error, concurrentDescriptionWriters)
	var group sync.WaitGroup
	for index := range concurrentDescriptionWriters {
		descriptions[index] = "concurrent description " + strconv.Itoa(index)
		arguments := auditedIssueArguments(harness)
		arguments.NodeID = issueID.String()
		arguments.Properties = datagen.NodeProperties{"description": quoted(descriptions[index])}
		group.Go(func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					failures[index] = fmt.Errorf("update %d panicked: %v", index, recovered)
				}
			}()
			_, failures[index] = harness.TryCall(t.Context(), "tack_update_issue", arguments)
		})
	}
	group.Wait()
	written := make([]string, 0, concurrentDescriptionWriters)
	for index, failure := range failures {
		if failure == nil {
			written = append(written, descriptions[index])
		}
	}
	if len(written) < 2 {
		t.Fatalf("concurrent updates succeeded %d times, want at least 2: %v", len(written), errors.Join(failures...))
	}
	return written
}

// waitForDescriptionChanges polls the ledger until it contains want
// description change rows of the issue and returns them in commit version
// order.
func waitForDescriptionChanges(t *testing.T, run concurrentDescriptionRun, want int) []audit.Row {
	t.Helper()
	deadline := clock.Now().Add(nodeAuditDeadline)
	for {
		changes := descriptionChanges(t, run)
		if len(changes) >= want {
			return changes
		}
		if clock.Now().After(deadline) {
			t.Fatalf("description change rows of issue %s after %s = %d, want %d", run.issueID, nodeAuditDeadline, len(changes), want)
		}
		time.Sleep(nodeAuditPollInterval)
	}
}

// descriptionChanges returns the node.update change rows of the issue that
// change the description, in commit version order.
func descriptionChanges(t *testing.T, run concurrentDescriptionRun) []audit.Row {
	t.Helper()
	var changes []audit.Row
	for _, row := range run.ledger.orgRows(t, run.orgID, run.issueID, audit.VerbNodeUpdate) {
		if row.Context.Phase == audit.PhaseChange && row.Delta != nil && slices.Contains(row.Delta.Changed, descriptionPath) {
			changes = append(changes, row)
		}
	}
	slices.SortFunc(changes, func(left, right audit.Row) int {
		return strings.Compare(commitVersion(t, left), commitVersion(t, right))
	})
	return changes
}

// recordDescription returns the description in one record encoding.
func recordDescription(t *testing.T, record json.RawMessage) string {
	t.Helper()
	return decodedJSONString(t, decodeAuditedIssueFields(t, record).Props["description"])
}

// decodedJSONString decodes one JSON string value.
func decodedJSONString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode the JSON string %s: %v", raw, err)
	}
	return value
}
