package audit_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// relationshipRow returns a successful format 2 change row that adds the
// blocks relationship from sourceID to targetID.
func relationshipRow(sourceID, targetID uuid.UUID, at time.Time, commitVersion string) audit.Row {
	var row audit.Row
	row.EventID = uuid.Must(uuid.NewV7())
	row.EventTime = at
	row.Action = string(audit.VerbRelationshipAdd)
	row.Outcome = audit.OutcomeCompleted
	row.EntityKind = audit.TargetKindRelationship
	row.Context.AuditFormatVersion = audit.AuditFormatComplete
	row.Context.Phase = audit.PhaseChange
	row.Context.CommitVersion = commitVersion
	row.Context.Target = &audit.Target{
		Kind: audit.TargetKindRelationship, NodeType: "", Reference: "", Name: "", URI: "", RelationType: "blocks",
		ID: uuid.Nil, SourceID: sourceID, TargetID: targetID,
	}
	row.Delta = &audit.Delta{Before: nil, After: json.RawMessage(`{"relation_type":"blocks"}`), Changed: nil}
	return row
}

// TestLastChangeAppliesLegacyRelationshipRowsToSourceEntries derives from a
// field change, a relationship from the node, a relationship to the node, and
// a newer legacy relationship row that stores the node as its entity. The
// relationship from the node must report unknown with the legacy row. The
// relationship to the node and the field entries must stay known without it,
// and the report must list the legacy row as a legacy relationship row.
func TestLastChangeAppliesLegacyRelationshipRowsToSourceEntries(t *testing.T) {
	nodeID, otherID, now := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), time.Now().UTC()
	field := changeRow(nodeID, now, "000000000000000000010000", "a", "b")
	outgoing := relationshipRow(nodeID, otherID, now.Add(time.Second), "000000000000000000020000")
	incoming := relationshipRow(otherID, nodeID, now.Add(2*time.Second), "000000000000000000030000")
	var legacy audit.Row
	legacy.EventID = uuid.Must(uuid.NewV7())
	legacy.EventTime = now.Add(3 * time.Second)
	legacy.Action = string(audit.VerbRelationshipAdd)
	legacy.Outcome = audit.OutcomeOK
	legacy.EntityKind = "relationship"
	legacy.EntityID = nodeID
	deriver := audit.NewLastChangeDeriver(nodeID, "")
	for _, row := range []audit.Row{field, outgoing, incoming, legacy} {
		if err := deriver.Add(row); err != nil {
			t.Fatalf("add row %s: %v", row.EventID, err)
		}
	}
	report := deriver.Report(false)
	want := []uuid.UUID{legacy.EventID}
	if !slices.Equal(report.LegacyRelationshipEventIDs, want) || report.LegacyRowCount != 1 {
		t.Fatalf("report legacy relationship events = %v, legacy rows = %d; want %v and 1", report.LegacyRelationshipEventIDs, report.LegacyRowCount, want)
	}
	for _, entry := range report.Fields {
		if entry.Status != audit.LastChangeKnown || len(entry.LegacyEventIDs) != 0 {
			t.Fatalf("field entry %+v, want known without legacy events", entry)
		}
	}
	for _, entry := range report.Relationships {
		fromNode := entry.SourceID == nodeID
		if fromNode && (entry.Status != audit.LastChangeUnknown || !slices.Equal(entry.LegacyEventIDs, want)) {
			t.Fatalf("relationship from the node = %+v, want unknown with %v", entry, want)
		}
		if !fromNode && (entry.Status != audit.LastChangeKnown || len(entry.LegacyEventIDs) != 0) {
			t.Fatalf("relationship to the node = %+v, want known without legacy events", entry)
		}
	}
	if len(report.Relationships) != 2 {
		t.Fatalf("relationships = %+v, want the relationship from and to the node", report.Relationships)
	}
}
