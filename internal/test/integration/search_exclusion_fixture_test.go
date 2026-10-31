package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/testenv"
)

// exclusionDeadline limits the wait for repeated failures of one work item
// and for its recovery. The attempt count and retry delay after release
// determine the expected wait.
const exclusionDeadline = 2 * time.Minute

// exclusionPollInterval spaces the verify runs while released work waits.
const exclusionPollInterval = time.Second

// searchVerifyReport is the exclusion and stuck work listing that ops search
// verify writes.
type searchVerifyReport struct {
	ExcludedNodes  int                     `json:"excluded_nodes"`
	Exclusions     []searchVerifyExclusion `json:"exclusions"`
	StuckWorkItems int                     `json:"stuck_work_items"`
	StuckWork      []searchVerifyStuckWork `json:"stuck_work"`
}

type searchVerifyExclusion struct {
	NodeID string `json:"node_id"`
	Class  string `json:"class"`
	Index  string `json:"index"`
	Reason string `json:"reason"`
}

type searchVerifyStuckWork struct {
	Kind      string `json:"kind"`
	ItemID    string `json:"item_id"`
	Attempts  int64  `json:"attempts"`
	LastError string `json:"last_error"`
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

// putOrphanNode creates a node of kind without a hierarchy parent and
// records it in fixture.Harness.titles. The stores do not schedule search
// work for the write. A replacement scan must read the node without a
// search work item.
func putOrphanNode(t *testing.T, fixture queryFixture, kind opaqueKind, name, included string) uuid.UUID {
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
	if err := stores.Nodes.CreateAtomic(WithTestOperation(t, t.Context()), created, view, nil, nil, nil, nil); err != nil {
		t.Fatalf("create orphan node: %v", err)
	}
	fixture.Harness.titles.Add(datagen.SearchItem{Title: name, NodeType: kind.TypeKey}, nodeID)
	return nodeID
}

// runSearchVerifyCommand runs the audited ops search verify command through
// the rendered Cobra tree. It decodes successful output. After a failure, it
// requires empty output and checks the public error against stored diagnostics.
func runSearchVerifyCommand(t *testing.T, cfg *config.Config) (searchVerifyReport, error) {
	t.Helper()
	factory := cli.System(cfg)
	var output bytes.Buffer
	factory.Out = &output
	pool, err := pgxpool.New(WithTestOperation(t, t.Context()), testenv.Ledger(t))
	if err != nil {
		t.Fatalf("open audit ledger pool: %v", err)
	}
	defer pool.Close()
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	factory.SetAuditReads(audit.NewPoolReadLifecycle(pool))
	root := searchCommandRoot(factory)
	root.SetArgs([]string{
		"--execute", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca30",
		"--operator-email", "operator@example.com", "--operator-name", "Search Test",
		"ops", "search", "verify",
	})
	runErr := root.Execute()
	if runErr != nil {
		if output.Len() != 0 {
			t.Fatalf("failed ops search verify printed unaudited result bytes: %s", output.String())
		}
		return searchVerifyFailureState(t, cfg, runErr), runErr
	}
	var report searchVerifyReport
	if err := json.NewDecoder(&output).Decode(&report); err != nil {
		t.Fatalf("decode ops search verify listing (command error %v): %v", runErr, err)
	}
	return report, runErr
}

// searchVerifyFailureState checks every stored stuck item against the public
// command error. Separate storage reads preserve exclusion-state assertions.
func searchVerifyFailureState(t *testing.T, cfg *config.Config, commandError error) searchVerifyReport {
	t.Helper()
	stores, err := fdbadapter.NewStores(cfg.FDBClusterFile, cfg.FDBTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores to compare verify failure evidence: %v", err)
	}
	report := searchVerifyReport{ExcludedNodes: 0, Exclusions: nil, StuckWorkItems: 0, StuckWork: nil}
	cursor := ""
	for {
		page, err := stores.SearchExclusions(t.Context(), cursor)
		if err != nil {
			t.Fatalf("read exclusion state after verify failure: %v", err)
		}
		for _, exclusion := range page.Exclusions {
			report.Exclusions = append(report.Exclusions, searchVerifyExclusion{
				NodeID: exclusion.NodeID.String(), Class: string(exclusion.Class), Index: exclusion.Index, Reason: exclusion.Reason,
			})
		}
		if page.Done {
			break
		}
		cursor = page.NextCursor
	}
	cursor = ""
	for {
		page, err := stores.SearchStuckWork(t.Context(), cursor)
		if err != nil {
			t.Fatalf("read stuck work after verify failure: %v", err)
		}
		for _, work := range page.Work {
			want := fmt.Sprintf("%s work %s failed %d times, last error: %s", work.Class, work.ItemID, work.Attempts, work.LastError)
			if !strings.Contains(commandError.Error(), want) {
				t.Fatalf("verify error %v omitted the actual stuck work %q", commandError, want)
			}
			report.StuckWork = append(report.StuckWork, searchVerifyStuckWork{
				Kind: string(work.Class), ItemID: work.ItemID, Attempts: work.Attempts, LastError: work.LastError,
			})
		}
		if page.Done {
			break
		}
		cursor = page.NextCursor
	}
	report.ExcludedNodes, report.StuckWorkItems = len(report.Exclusions), len(report.StuckWork)
	return report
}

// indexedText reports whether an active page of nodeID in the serving index
// contains text.
func indexedText(t *testing.T, fixture queryFixture, nodeID uuid.UUID, text string) bool {
	t.Helper()
	for _, page := range searchNodePages(t, fixture.Client, fixture.Index, nodeID, false) {
		if page.PageText != nil && strings.Contains(*page.PageText, text) {
			return true
		}
	}
	return false
}
