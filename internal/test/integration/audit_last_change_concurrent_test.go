package integration_test

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// TestAuditLastChangeReportsHighestCommitVersionAfterConcurrentUpdates sends
// concurrent tack_update_issue requests through the audited MCP harness. Each
// request sets a different description of the same issue. The production
// relay and the audit consumer write the change rows. `audit last-change`
// must report the description change with the highest FoundationDB commit
// version as known, and the new value of that change must equal the
// description that the issue stores afterwards.
func TestAuditLastChangeReportsHighestCommitVersionAfterConcurrentUpdates(t *testing.T) {
	run := runConcurrentDescriptionUpdates(t)
	latest := run.changes[len(run.changes)-1]
	entry := requireField(t, run.report, descriptionPath, audit.LastChangeKnown, latest, audit.LastChangeSet)
	if len(entry.CandidateEventIDs) != 0 || len(entry.LegacyEventIDs) != 0 {
		t.Fatalf("%s = %+v, want a known change without candidates or legacy rows", descriptionPath, entry)
	}
	if after := recordDescription(t, latest.Delta.After); after != run.final {
		t.Fatalf("description after change %s = %q, the issue stores %q", latest.EventID, after, run.final)
	}
}

// TestAuditLastChangeRecordsOneChangePerCommittedConcurrentWrite sends the
// same concurrent updates. Conflicting FoundationDB transactions retry, and
// each attempt clears the changes of the earlier attempt before it writes its
// own. The ledger must contain exactly one description change row for each
// successful update, with distinct event IDs and new values that match the
// successful updates. In commit version order, each row must start from the
// value that the row before it wrote, which an aborted attempt would not do.
// `audit last-change` must report only a committed change row.
func TestAuditLastChangeRecordsOneChangePerCommittedConcurrentWrite(t *testing.T) {
	run := runConcurrentDescriptionUpdates(t)
	changes := descriptionChanges(t, run)
	if len(changes) != len(run.written) {
		t.Fatalf("description change rows = %d, want one for each of the %d successful updates", len(changes), len(run.written))
	}
	eventIDs := make([]uuid.UUID, 0, len(changes))
	values := make([]string, 0, len(changes))
	previous := concurrentInitialDescription
	for _, row := range changes {
		if before := recordDescription(t, row.Delta.Before); before != previous {
			t.Fatalf("change %s starts from %q, want %q from the change before it", row.EventID, before, previous)
		}
		previous = recordDescription(t, row.Delta.After)
		if slices.Contains(eventIDs, row.EventID) {
			t.Fatalf("event %s appears in more than one change row", row.EventID)
		}
		eventIDs = append(eventIDs, row.EventID)
		values = append(values, previous)
	}
	slices.Sort(values)
	written := slices.Clone(run.written)
	slices.Sort(written)
	if !slices.Equal(values, written) {
		t.Fatalf("new descriptions of the change rows = %v, want the successful updates %v", values, written)
	}
	entry := requireField(t, run.report, descriptionPath, audit.LastChangeKnown, changes[len(changes)-1], audit.LastChangeSet)
	if !slices.Contains(eventIDs, entry.Change.EventID) {
		t.Fatalf("%s change %s is not a committed change row", descriptionPath, entry.Change.EventID)
	}
}
