package audit_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// changeRow returns a successful format 2 change row of nodeID that changes
// /name from before to after. An empty commitVersion makes a row from a SQL
// change stream.
func changeRow(nodeID uuid.UUID, at time.Time, commitVersion, before, after string) audit.Row {
	var row audit.Row
	row.EventID = uuid.Must(uuid.NewV7())
	row.EventTime = at
	row.Action = string(audit.VerbNodeUpdate)
	row.Outcome = audit.OutcomeCompleted
	row.EntityID = nodeID
	row.Context.AuditFormatVersion = audit.AuditFormatComplete
	row.Context.Phase = audit.PhaseChange
	row.Context.CommitVersion = commitVersion
	row.Context.Target = &audit.Target{
		Kind: audit.TargetKindNode, NodeType: "issue", Reference: "", Name: "", URI: "", RelationType: "",
		ID: nodeID, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	row.Delta = &audit.Delta{
		Before: json.RawMessage(`{"name":"` + before + `"}`), After: json.RawMessage(`{"name":"` + after + `","props":{"priority":"high"}}`),
		Changed: []string{"/name", "/props/priority"},
	}
	return row
}

// deriveFields adds rows to a deriver for nodeID and returns the field
// entries by path.
func deriveFields(t *testing.T, nodeID uuid.UUID, truncated bool, rows ...audit.Row) map[string]audit.LastChangeEntry {
	t.Helper()
	deriver := audit.NewLastChangeDeriver(nodeID, "")
	for _, row := range rows {
		if err := deriver.Add(row); err != nil {
			t.Fatalf("add row %s: %v", row.EventID, err)
		}
	}
	entries := map[string]audit.LastChangeEntry{}
	for _, entry := range deriver.Report(truncated).Fields {
		entries[entry.Path] = entry
	}
	return entries
}

// TestLastChangeOrdersFoundationDBRowsByCommitVersion gives the newer commit
// version the older event time. The higher commit version must win.
func TestLastChangeOrdersFoundationDBRowsByCommitVersion(t *testing.T) {
	nodeID, now := uuid.Must(uuid.NewV7()), time.Now().UTC()
	newer := changeRow(nodeID, now, "000000000000000000020000", "b", "c")
	older := changeRow(nodeID, now.Add(time.Second), "000000000000000000010000", "a", "b")
	name := deriveFields(t, nodeID, false, older, newer)["/name"]
	if name.Change == nil || name.Change.EventID != newer.EventID || name.Status != audit.LastChangeKnown {
		t.Fatalf("/name = %+v, want known change %s", name, newer.EventID)
	}
}

// TestLastChangeReportsAmbiguousOrderAcrossDomains mixes a row with a commit
// version and a row without one. The entry must list both candidates and
// report no change.
func TestLastChangeReportsAmbiguousOrderAcrossDomains(t *testing.T) {
	nodeID, now := uuid.Must(uuid.NewV7()), time.Now().UTC()
	versioned := changeRow(nodeID, now, "000000000000000000010000", "a", "b")
	unversioned := changeRow(nodeID, now.Add(time.Second), "", "b", "c")
	name := deriveFields(t, nodeID, false, versioned, unversioned)["/name"]
	want := []uuid.UUID{versioned.EventID, unversioned.EventID}
	if name.Status != audit.LastChangeAmbiguousOrder || name.Change != nil || !slices.Equal(name.CandidateEventIDs, want) {
		t.Fatalf("/name = %+v, want ambiguous_order with candidates %v and no change", name, want)
	}
}

// TestLastChangeReportsPartialWithoutHistoryStart reads one update without
// the node's creation. A path of the record without a change must report
// partial, and a truncated read must also report partial for it.
func TestLastChangeReportsPartialWithoutHistoryStart(t *testing.T) {
	nodeID := uuid.Must(uuid.NewV7())
	update := changeRow(nodeID, time.Now().UTC(), "000000000000000000010000", "a", "b")
	update.Delta.Before = json.RawMessage(`{"name":"a","description":"kept"}`)
	update.Delta.After = json.RawMessage(`{"name":"b","description":"kept"}`)
	update.Delta.Changed = []string{"/name"}
	for _, truncated := range []bool{false, true} {
		fields := deriveFields(t, nodeID, truncated, update)
		if fields["/description"].Status != audit.LastChangePartial || fields["/name"].Status != audit.LastChangeKnown {
			t.Fatalf("truncated %t: fields = %+v, want /description partial and /name known", truncated, fields)
		}
	}
}
