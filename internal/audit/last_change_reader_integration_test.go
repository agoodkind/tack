package audit_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

// TestReadNodeHistoryTruncationReportsPartial writes two /name changes of one
// node to the test ledger. The change with the later commit version has the
// older event time. A read with a row limit of one reads only the row with
// the newer event time, and /name must report partial. A read without that
// limit reads both rows, and /name must report the later commit version as
// known.
func TestReadNodeHistoryTruncationReportsPartial(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, ledgerDSN)
	if err != nil {
		t.Fatalf("open the ledger pool: %v", err)
	}
	t.Cleanup(admin.Close)
	orgID, nodeID, now := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), clock.Now().UTC()
	t.Cleanup(func() { removeOrgRows(admin, orgID) })
	recorder, err := audit.NewYBRecorder(ctx, ledgerDSN)
	if err != nil {
		t.Fatalf("open the recorder: %v", err)
	}
	defer recorder.Close()
	newerTime := nodeNameChange(t, orgID, nodeID, now, "000000000000000000010000")
	laterCommit := nodeNameChange(t, orgID, nodeID, now.Add(-time.Second), "000000000000000000020000")
	for _, event := range []audit.Event{newerTime, laterCommit} {
		if err := recorder.Record(ctx, event); err != nil {
			t.Fatalf("record event %s: %v", event.EventID, err)
		}
	}
	reader, err := audit.NewReader(ctx, ledgerDSN)
	if err != nil {
		t.Fatalf("open the reader: %v", err)
	}
	t.Cleanup(reader.Close)

	truncated := nameEntry(t, reader, orgID, nodeID, now, 1)
	if truncated.Status != audit.LastChangePartial || truncated.Change == nil || truncated.Change.EventID != newerTime.EventID {
		t.Fatalf("/name after a truncated read = %+v, want partial with the observed change %s", truncated, newerTime.EventID)
	}
	complete := nameEntry(t, reader, orgID, nodeID, now, 0)
	if complete.Status != audit.LastChangeKnown || complete.Change == nil || complete.Change.EventID != laterCommit.EventID {
		t.Fatalf("/name after a complete read = %+v, want known change %s", complete, laterCommit.EventID)
	}
}

// nameEntry reads the history of nodeID with rowLimit and returns the /name
// entry of the report.
func nameEntry(t *testing.T, reader *audit.Reader, orgID, nodeID uuid.UUID, now time.Time, rowLimit int) audit.LastChangeEntry {
	t.Helper()
	deriver := audit.NewLastChangeDeriver(nodeID, "/name")
	filter := audit.WithNodeHistoryRowLimit(audit.NodeHistoryFilter{
		OrgID: orgID, NodeID: nodeID, Oldest: now.Add(-time.Minute), Latest: now.Add(time.Minute),
	}, rowLimit)
	read, err := reader.ReadNodeHistory(t.Context(), filter, "", deriver.Add)
	if err != nil {
		t.Fatalf("read the history of node %s with row limit %d: %v", nodeID, rowLimit, err)
	}
	if read.Truncated != (rowLimit == 1) {
		t.Fatalf("read with row limit %d = %+v, want truncated %t", rowLimit, read, rowLimit == 1)
	}
	fields := deriver.Report(read.Truncated).Fields
	if len(fields) != 1 {
		t.Fatalf("report fields = %+v, want only /name", fields)
	}
	return fields[0]
}

// nodeNameChange builds a format 2 change event of nodeID through
// audit.ChangeEvent from an operation with an org ID, an operation ID, and a
// start time. The event completes at occurredAt, changes /name, and stores
// commitVersion in its context, as the relay does for an outbox entry.
func nodeNameChange(t *testing.T, orgID, nodeID uuid.UUID, occurredAt time.Time, commitVersion string) audit.Event {
	t.Helper()
	var operation audit.Event
	operation.Verb = string(audit.VerbNodeUpdate)
	operation.Actor.Type = audit.ActorUser
	operation.Actor.ID = uuid.Must(uuid.NewV7())
	operation.Context.OrgID = orgID
	operation.Context.Source = audit.SourceMCP
	operation.Context.OperationID = uuid.Must(uuid.NewV7())
	operation.Context.StartedAt = occurredAt.Add(-time.Millisecond)
	target := audit.Target{
		Kind: audit.TargetKindNode, NodeType: "issue", Reference: "", Name: "", URI: "", RelationType: "",
		ID: nodeID, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	change, err := audit.NewChange(uuid.Must(uuid.NewV7()), target,
		json.RawMessage(`{"name":"before"}`), json.RawMessage(`{"name":"`+commitVersion+`"}`))
	if err != nil {
		t.Fatalf("build the /name change of node %s: %v", nodeID, err)
	}
	event, err := audit.ChangeEvent(operation, change, occurredAt)
	if err != nil {
		t.Fatalf("build the change event of node %s: %v", nodeID, err)
	}
	event.Context.CommitVersion = commitVersion
	return event
}

// removeOrgRows deletes the ledger rows, PII rows, and chain heads of orgID.
func removeOrgRows(admin *pgxpool.Pool, orgID uuid.UUID) {
	ctx := context.Background()
	_, _ = admin.Exec(ctx, `DELETE FROM audit.pii WHERE pii_ref IN (SELECT pii_ref FROM audit.events WHERE org_id = $1)`, orgID)
	_, _ = admin.Exec(ctx, `DELETE FROM audit.events WHERE org_id = $1`, orgID)
	_, _ = admin.Exec(ctx, `DELETE FROM audit.chain_heads WHERE org_id = $1`, orgID)
}
