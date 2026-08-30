package ops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
)

// fakeRowQuerier returns a fixed set of ledger rows and records the filter it
// was asked for, so a test can assert the read is bounded the way the
// derivation claims.
type fakeRowQuerier struct {
	rows   []audit.Row
	err    error
	filter audit.QueryFilter
}

func (q *fakeRowQuerier) Query(_ context.Context, filter audit.QueryFilter) ([]audit.Row, error) {
	q.filter = filter
	if q.err != nil {
		return nil, q.err
	}
	return q.rows, nil
}

func deletedKeysTestTypes() []*node.NodeType {
	return []*node.NodeType{
		{
			TypeKey: "issue", Slug: "issue", Name: "Issue",
			ReferenceTemplates: []node.ReferenceTemplate{{
				Name: "reference", IsPrimary: true, Generated: "sequence",
				Parts: []node.ReferencePart{
					{Kind: node.ReferencePartScopeRef, Value: node.FeatureIsScope},
					{Kind: node.ReferencePartLiteral, Value: "-"},
					{Kind: node.ReferencePartProperty, Value: "sequence"},
				},
			}},
		},
		{TypeKey: "comment", Slug: "comment", Name: "Comment", ReferenceTemplates: nil},
	}
}

func deletedKeysTestPrincipal() audit.OperatorPrincipal {
	return audit.OperatorPrincipal{
		ID: uuid.MustParse("019ff315-bc5d-7a56-b12a-1a35f280c4dd"), Email: "operator@example.com",
		Name: "Operator User", Source: "test",
	}
}

func deleteRow(t *testing.T, tool string, entityID uuid.UUID, outcome audit.Outcome) audit.Row {
	t.Helper()
	return audit.Row{
		OrgID: uuid.Nil, EventTime: time.Date(2026, time.August, 14, 0, 38, 35, 92867000, time.UTC),
		EventID: uuid.NewSHA1(referenceRepairBackfillNamespace, []byte("delete-row:"+tool)),
		Seq:     1, Shard: 1, ActorID: uuid.Nil, ActorKind: 1,
		Action: string(audit.VerbNodeDelete), Outcome: outcome, EntityKind: "mcp_tool", EntityID: entityID,
		Context: audit.EventContext{
			OrgID: uuid.Nil, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: "", TraceID: "", Source: audit.SourceMCP, Tool: tool, RPC: "", Reason: "",
		},
		Delta: nil, Error: nil, Extra: nil, PIIRef: nil, PrevHash: nil, RowHash: nil,
		HashVersion: 1, IdempotencyKey: "",
	}
}

// TestDeletedSubjectKeyEventsCountsOnlyReferenceBearingDeletes pins what the
// derivation counts: a deleted issue contributes one key event per template,
// a deleted comment contributes none because its type renders no key, and a
// delete the ledger recorded as failed contributes none because it removed
// nothing. Production's own window holds exactly the first two shapes.
func TestDeletedSubjectKeyEventsCountsOnlyReferenceBearingDeletes(t *testing.T) {
	t.Parallel()
	orgID := uuid.MustParse("019dc5ad-0408-7e43-9c4d-d3e6736ac058")
	occurredAt := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	querier := &fakeRowQuerier{
		rows: []audit.Row{
			deleteRow(t, "tack_delete_issue", uuid.Nil, audit.OutcomeUnrecorded),
			deleteRow(t, "tack_delete_comment", uuid.Nil, audit.OutcomeUnrecorded),
		},
		err: nil, filter: audit.QueryFilter{},
	}

	events, err := deletedSubjectKeyEvents(
		context.Background(), querier, deletedKeysTestTypes(), deletedKeysTestPrincipal(),
		orgID, occurredAt, referenceRepairStart,
	)
	if err != nil {
		t.Fatalf("deletedSubjectKeyEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 (the deleted issue only)", len(events))
	}
	// The event count alone cannot catch a type filter that lets a
	// reference-free type through, because such a type renders no template and
	// so adds no event either way. The subject set is where the filter decides,
	// so assert there: the deleted comment must never become a subject.
	subjects, err := deletedReferenceSubjects(
		context.Background(), querier, deletedKeysTestTypes(), orgID, occurredAt, referenceRepairStart,
	)
	if err != nil {
		t.Fatalf("deletedReferenceSubjects: %v", err)
	}
	if len(subjects) != 1 || subjects[0].NodeType.TypeKey != "issue" {
		t.Fatalf("subjects = %+v, want only the deleted issue", subjects)
	}
	if querier.filter.Action != string(audit.VerbNodeDelete) || !querier.filter.Oldest.Equal(referenceRepairStart) {
		t.Fatalf("filter = %+v, want node.delete bounded at the repair start", querier.filter)
	}
	if querier.filter.Limit != deletedSubjectQueryLimit {
		t.Fatalf("filter limit = %d, want %d", querier.filter.Limit, deletedSubjectQueryLimit)
	}

	var extra reconstructionExtra
	if err := json.Unmarshal(events[0].Extra, &extra); err != nil {
		t.Fatalf("decode extra: %v", err)
	}
	if !extra.SubjectIdentityUnrecorded {
		t.Fatal("a delete row carrying the zero id must mark the subject identity unrecorded")
	}
	if extra.SubjectDeletionEventID == "" || extra.SubjectDeletedAt == "" {
		t.Fatalf("extra must name the deletion it was derived from: %+v", extra)
	}
	if extra.Class != "reference keys" || extra.HistoricalTime != referenceRepairDate {
		t.Fatalf("extra = %+v, want the reference-key class at the repair date", extra)
	}
	if events[0].Entity.ID != uuid.Nil || events[0].Entity.NodeType != "issue" {
		t.Fatalf("entity = %+v, want the issue type with the zero id", events[0].Entity)
	}
	if !strings.HasPrefix(events[0].IdempotencyKey, deletedSubjectKeyPrefix) {
		t.Fatalf("idempotency key %q must carry the deleted-subject prefix", events[0].IdempotencyKey)
	}
}

// TestDeletedSubjectKeyEventsNamesTheDeletedNodeWhenRecorded pins the other
// half: a delete row that did record which node it removed produces an event
// naming that node, with no unrecorded-identity marker.
func TestDeletedSubjectKeyEventsNamesTheDeletedNodeWhenRecorded(t *testing.T) {
	t.Parallel()
	nodeID := uuid.MustParse("019dc5ed-eac1-7ab4-b86b-cebc6ce06de8")
	querier := &fakeRowQuerier{
		rows: []audit.Row{deleteRow(t, "tack_delete_issue", nodeID, audit.OutcomeOK)},
		err:  nil, filter: audit.QueryFilter{},
	}

	events, err := deletedSubjectKeyEvents(
		context.Background(), querier, deletedKeysTestTypes(), deletedKeysTestPrincipal(),
		uuid.New(), time.Now().UTC(), referenceRepairStart,
	)
	if err != nil {
		t.Fatalf("deletedSubjectKeyEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Entity.ID != nodeID {
		t.Fatalf("entity id = %s, want the deleted node %s", events[0].Entity.ID, nodeID)
	}
	var extra reconstructionExtra
	if err := json.Unmarshal(events[0].Extra, &extra); err != nil {
		t.Fatalf("decode extra: %v", err)
	}
	if extra.SubjectIdentityUnrecorded {
		t.Fatal("a delete row naming its node must not mark the identity unrecorded")
	}
}

// TestDeletedSubjectKeyEventsSkipsFailedDeletes pins that a delete the ledger
// recorded as failed adds nothing: its node still exists and the live
// derivation already counted it, so counting it again would push the derived
// total past what the repair recorded.
func TestDeletedSubjectKeyEventsSkipsFailedDeletes(t *testing.T) {
	t.Parallel()
	querier := &fakeRowQuerier{
		rows: []audit.Row{deleteRow(t, "tack_delete_issue", uuid.New(), audit.OutcomeError)},
		err:  nil, filter: audit.QueryFilter{},
	}

	events, err := deletedSubjectKeyEvents(
		context.Background(), querier, deletedKeysTestTypes(), deletedKeysTestPrincipal(),
		uuid.New(), time.Now().UTC(), referenceRepairStart,
	)
	if err != nil {
		t.Fatalf("deletedSubjectKeyEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %d, want 0 for a failed delete", len(events))
	}
}

// TestDeletedSubjectKeyEventsRefusesATruncatedRead pins that the derivation
// refuses rather than undercount when the read fills its limit, because a
// truncated result cannot prove it saw every deletion.
func TestDeletedSubjectKeyEventsRefusesATruncatedRead(t *testing.T) {
	t.Parallel()
	rows := make([]audit.Row, deletedSubjectQueryLimit)
	for i := range rows {
		rows[i] = deleteRow(t, "tack_delete_issue", uuid.New(), audit.OutcomeOK)
	}
	querier := &fakeRowQuerier{rows: rows, err: nil, filter: audit.QueryFilter{}}

	_, err := deletedSubjectKeyEvents(
		context.Background(), querier, deletedKeysTestTypes(), deletedKeysTestPrincipal(),
		uuid.New(), time.Now().UTC(), referenceRepairStart,
	)
	if err == nil || !strings.Contains(err.Error(), "row read limit") {
		t.Fatalf("err = %v, want the truncated-read refusal", err)
	}
}

// TestDeletedSubjectKeyEventsSurfacesTheReadError pins that a ledger read
// failure stops the run. Treating it as zero deletions would silently lower
// the reconstructed count, which is the one outcome this ticket forbids.
func TestDeletedSubjectKeyEventsSurfacesTheReadError(t *testing.T) {
	t.Parallel()
	querier := &fakeRowQuerier{rows: nil, err: errors.New("ledger unreachable"), filter: audit.QueryFilter{}}

	_, err := deletedSubjectKeyEvents(
		context.Background(), querier, deletedKeysTestTypes(), deletedKeysTestPrincipal(),
		uuid.New(), time.Now().UTC(), referenceRepairStart,
	)
	if err == nil || !strings.Contains(err.Error(), "ledger unreachable") {
		t.Fatalf("err = %v, want the read failure surfaced", err)
	}
}

// TestDeletedSubjectKeyEventsAreStableAcrossRuns pins idempotency at the
// identity level: the same deletion rebuilds the same event id, so a rerun
// writes nothing new even though the reconstruction time moved.
func TestDeletedSubjectKeyEventsAreStableAcrossRuns(t *testing.T) {
	t.Parallel()
	row := deleteRow(t, "tack_delete_issue", uuid.Nil, audit.OutcomeUnrecorded)
	orgID := uuid.New()
	first := &fakeRowQuerier{rows: []audit.Row{row}, err: nil, filter: audit.QueryFilter{}}
	second := &fakeRowQuerier{rows: []audit.Row{row}, err: nil, filter: audit.QueryFilter{}}

	firstEvents, err := deletedSubjectKeyEvents(
		context.Background(), first, deletedKeysTestTypes(), deletedKeysTestPrincipal(),
		orgID, time.Date(2026, time.August, 30, 1, 0, 0, 0, time.UTC), referenceRepairStart,
	)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	secondEvents, err := deletedSubjectKeyEvents(
		context.Background(), second, deletedKeysTestTypes(), deletedKeysTestPrincipal(),
		orgID, time.Date(2026, time.August, 31, 9, 30, 0, 0, time.UTC), referenceRepairStart,
	)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(firstEvents) != 1 || len(secondEvents) != 1 {
		t.Fatalf("events = %d and %d, want 1 each", len(firstEvents), len(secondEvents))
	}
	if firstEvents[0].EventID != secondEvents[0].EventID {
		t.Fatalf("event id changed between runs: %s then %s", firstEvents[0].EventID, secondEvents[0].EventID)
	}
}

// TestReferenceKeyClassCountsPresentPlusDeletedSubjects pins the arithmetic
// production is blocked on: the 2026-08-07 class validates when the keys of
// nodes present today plus the keys of nodes the ledger shows were deleted
// since add up to the number the repair recorded, and refuses when the deleted
// subjects are dropped. Never lower the recorded number to make it agree.
func TestReferenceKeyClassCountsPresentPlusDeletedSubjects(t *testing.T) {
	t.Parallel()
	const presentKeys = recordedReferenceKeys - 1
	withDeleted := auditBackfillPlan{Classes: []auditBackfillClass{{
		Count: auditBackfillCount{
			Class: "reference keys 2026-08-07", Derived: presentKeys + 1,
			Recorded: recordedReferenceKeys, DeletedSubjects: 1,
		},
		Events: nil,
	}}}
	if err := withDeleted.Validate(context.Background()); err != nil {
		t.Fatalf("present %d plus 1 deleted subject must validate against %d: %v",
			presentKeys, recordedReferenceKeys, err)
	}

	withoutDeleted := auditBackfillPlan{Classes: []auditBackfillClass{{
		Count: auditBackfillCount{
			Class: "reference keys 2026-08-07", Derived: presentKeys,
			Recorded: recordedReferenceKeys, DeletedSubjects: 0,
		},
		Events: nil,
	}}}
	if err := withoutDeleted.Validate(context.Background()); err == nil {
		t.Fatal("dropping the deleted subjects must refuse, not pass with a lower count")
	}

	// A node created after the repair and then deleted has no repair key, so
	// counting it would push the derived total past what the repair recorded.
	// The check stays exact in that direction too.
	overCounted := auditBackfillPlan{Classes: []auditBackfillClass{{
		Count: auditBackfillCount{
			Class: "reference keys 2026-08-07", Derived: recordedReferenceKeys + 1,
			Recorded: recordedReferenceKeys, DeletedSubjects: 2,
		},
		Events: nil,
	}}}
	if err := overCounted.Validate(context.Background()); err == nil {
		t.Fatal("a derived count above the recorded number must refuse")
	}
}
