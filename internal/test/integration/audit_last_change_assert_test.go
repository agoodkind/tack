package integration_test

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// requireField requires the report entry for path to have status and to
// report row as its change with effect, and returns the entry.
func requireField(t *testing.T, report lastChangeReport, path string, status audit.LastChangeStatus, row audit.Row, effect audit.LastChangeEffect) audit.LastChangeEntry {
	t.Helper()
	for _, entry := range report.Fields {
		if entry.Path != path {
			continue
		}
		requireChange(t, path, entry, status, row, effect)
		return entry
	}
	t.Fatalf("the report fields %+v do not include %s", report.Fields, path)
	return audit.LastChangeEntry{}
}

// requireRelationship requires the entry for the blocks relationship from
// sourceID to targetID to have status and a change with effect, and returns
// the entry.
func requireRelationship(t *testing.T, report lastChangeReport, sourceID, targetID uuid.UUID, status audit.LastChangeStatus, effect audit.LastChangeEffect) audit.LastChangeEntry {
	t.Helper()
	for _, entry := range report.Relationships {
		if entry.SourceID != sourceID || entry.RelationType != lastChangeRelationType || entry.TargetID != targetID {
			continue
		}
		if entry.Status != status || entry.Change == nil || entry.Change.Effect != effect {
			t.Fatalf("relationship %s %s %s = %+v, want status %s with a change with effect %s", sourceID, lastChangeRelationType, targetID, entry, status, effect)
		}
		return entry
	}
	t.Fatalf("the report relationships %+v do not include %s %s %s", report.Relationships, sourceID, lastChangeRelationType, targetID)
	return audit.LastChangeEntry{}
}

// requireChange requires entry to have status and to report row with effect,
// including the row's commit version.
func requireChange(t *testing.T, label string, entry audit.LastChangeEntry, status audit.LastChangeStatus, row audit.Row, effect audit.LastChangeEffect) {
	t.Helper()
	change := entry.Change
	if entry.Status != status || change == nil {
		t.Fatalf("%s = %+v, want status %s with a change", label, entry, status)
	}
	if change.EventID != row.EventID || change.Effect != effect || change.CommitVersion != commitVersion(t, row) {
		t.Fatalf("%s change = %+v, want event %s with effect %s and commit version %s", label, change, row.EventID, effect, row.Context.CommitVersion)
	}
	if change.Outcome != audit.OutcomeCompleted || change.ActorID != row.ActorID || !change.EventTime.Equal(row.EventTime) {
		t.Fatalf("%s change = %+v, want the completed row %+v", label, change, row)
	}
}

// requireRefusalUncounted fails when a report entry uses the refused row as
// its change or lists it as a candidate.
func requireRefusalUncounted(t *testing.T, report lastChangeReport, refused audit.Row) {
	t.Helper()
	for _, entry := range slices.Concat(report.Fields, report.Relationships) {
		if (entry.Change != nil && entry.Change.EventID == refused.EventID) || slices.Contains(entry.CandidateEventIDs, refused.EventID) {
			t.Fatalf("entry %+v counts the %s update %s", entry, refused.Outcome, refused.EventID)
		}
	}
}

// requireLegacyRelationshipRow requires the report to list legacyID as its only
// legacy relationship row and no field entry to list legacyID.
func requireLegacyRelationshipRow(t *testing.T, report lastChangeReport, legacyID uuid.UUID) {
	t.Helper()
	if !slices.Equal(report.LegacyRelationshipEventIDs, []uuid.UUID{legacyID}) {
		t.Fatalf("legacy relationship events = %v, want only %s", report.LegacyRelationshipEventIDs, legacyID)
	}
	for _, entry := range report.Fields {
		if slices.Contains(entry.LegacyEventIDs, legacyID) {
			t.Fatalf("field entry %+v lists the legacy relationship row %s", entry, legacyID)
		}
	}
}

// renameRow returns the update row that changes /name to name.
func renameRow(t *testing.T, rows []audit.Row, name string) audit.Row {
	t.Helper()
	for _, row := range rows {
		if row.Delta == nil || !slices.Contains(row.Delta.Changed, "/name") {
			continue
		}
		if after := decodeAuditedIssueFields(t, row.Delta.After); after.Name != nil && *after.Name == name {
			return row
		}
	}
	t.Fatalf("update rows %+v do not change /name to %q", rows, name)
	return audit.Row{}
}

// changedRow returns the only update row that changes path.
func changedRow(t *testing.T, rows []audit.Row, path string) audit.Row {
	t.Helper()
	var matched []audit.Row
	for _, row := range rows {
		if row.Delta != nil && slices.Contains(row.Delta.Changed, path) {
			matched = append(matched, row)
		}
	}
	if len(matched) != 1 {
		t.Fatalf("update rows that change %s = %+v, want exactly one", path, matched)
	}
	return matched[0]
}
