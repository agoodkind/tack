package integration

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/testenv"
)

// exclusionDeadline bounds the wait for a failing node to be excluded. The
// attempt limit and the retry delay of released work set the expected wait.
const exclusionDeadline = 2 * time.Minute

// exclusionPollInterval spaces the verify runs while released work waits.
const exclusionPollInterval = time.Second

// searchVerifyReport is the exclusion and stuck work listing that ops search
// verify writes.
type searchVerifyReport struct {
	ExcludedNodes int `json:"excluded_nodes"`
	Exclusions    []struct {
		NodeID string `json:"node_id"`
		Class  string `json:"class"`
		Index  string `json:"index"`
		Reason string `json:"reason"`
	} `json:"exclusions"`
	StuckWorkItems int `json:"stuck_work_items"`
	StuckWork      []struct {
		Kind      string `json:"kind"`
		ItemID    string `json:"item_id"`
		Attempts  int64  `json:"attempts"`
		LastError string `json:"last_error"`
	} `json:"stuck_work"`
}

// reason returns the listed reason of nodeID and whether the listing has it.
func (report searchVerifyReport) reason(nodeID uuid.UUID) (string, bool) {
	for _, exclusion := range report.Exclusions {
		if exclusion.NodeID == nodeID.String() {
			return exclusion.Reason, true
		}
	}
	return "", false
}

// putOrphanNode creates one node of kind without a hierarchy parent through
// stores that schedule no search work. The node matches a node written
// before search work existed. A replacement scan is the first search
// operation that reads it.
func putOrphanNode(t *testing.T, kind opaqueKind, name, included string) uuid.UUID {
	t.Helper()
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), testTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores without search work: %v", err)
	}
	nodeID := uuid.Must(uuid.NewV7())
	props := map[string]json.RawMessage{kind.IncludedKey: mustJSON(included), kind.ExcludedKey: mustJSON(readerExcludedValue)}
	now := clock.Now().UTC()
	created := &node.Node{ID: nodeID, OrgID: kind.OrgID, NodeType: kind.TypeKey, Name: name, Props: props, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: nodeID, OrgID: kind.OrgID, NodeType: kind.TypeKey, Name: name, Props: props, CreatedAt: now, UpdatedAt: now}
	if err := stores.Nodes.CreateAtomic(t.Context(), created, view, nil, nil, nil, nil); err != nil {
		t.Fatalf("create orphan node: %v", err)
	}
	return nodeID
}

// runSearchVerifyCommand runs the audited ops search verify command through
// the rendered Cobra tree and decodes the exclusion listing it writes. It
// returns the command error separately. The query fixture serves an index
// other than the provisioned node-pages-1, and the physical checks that
// follow the listing report that index.
func runSearchVerifyCommand(t *testing.T, cfg *config.Config) (searchVerifyReport, error) {
	t.Helper()
	factory := cli.System(cfg)
	var output bytes.Buffer
	factory.Out = &output
	pool, err := pgxpool.New(t.Context(), testenv.Ledger(t))
	if err != nil {
		t.Fatalf("open audit ledger pool: %v", err)
	}
	defer pool.Close()
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	root := searchCommandRoot(factory)
	root.SetArgs([]string{
		"--execute", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca30",
		"--operator-email", "operator@example.com", "--operator-name", "Search Test",
		"ops", "search", "verify",
	})
	runErr := root.Execute()
	var report searchVerifyReport
	if err := json.NewDecoder(&output).Decode(&report); err != nil {
		t.Fatalf("decode ops search verify listing (command error %v): %v", runErr, err)
	}
	return report, runErr
}

// runUntilExcluded runs worker slices until ops search verify lists nodeID,
// and ignores slice failures. It fails the test after exclusionDeadline.
func runUntilExcluded(t *testing.T, fixture queryFixture, worker *service.SearchWorker, nodeID uuid.UUID) searchVerifyReport {
	t.Helper()
	deadline := clock.Now().Add(exclusionDeadline)
	var lastErr error
	for clock.Now().Before(deadline) {
		claimed, err := worker.RunSlice(t.Context())
		if err != nil {
			lastErr = err
		}
		if claimed {
			continue
		}
		report, _ := runSearchVerifyCommand(t, fixture.Config)
		if _, listed := report.reason(nodeID); listed {
			return report
		}
		waitUntil(t, clock.Now().Add(exclusionPollInterval))
	}
	t.Fatalf("node %s was not excluded within %s; last slice error: %v", nodeID, exclusionDeadline, lastErr)
	return searchVerifyReport{}
}
