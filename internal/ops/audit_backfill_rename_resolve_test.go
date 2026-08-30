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
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// absentNodeReader resolves every node except the ones named absent, which it
// reports as not found the way the store does after a delete.
type absentNodeReader struct {
	absent map[uuid.UUID]bool
	err    error
}

func (r absentNodeReader) Resolve(_ context.Context, nodeID uuid.UUID) (*node.NodeResolve, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.absent[nodeID] {
		return nil, domain.ErrNotFound
	}
	return &node.NodeResolve{
		OrgID:    uuid.MustParse("19c6e449-f2c2-5063-939a-4adb0cb233f8"),
		NodeType: "issue",
	}, nil
}

func firstEvidenceNode(t *testing.T, renames []referenceRenameEvidence) uuid.UUID {
	t.Helper()
	nodeID, err := uuid.Parse(renames[0].NodeID)
	if err != nil {
		t.Fatalf("parse evidence node: %v", err)
	}
	return nodeID
}

// TestRenameClassSurvivesADeletedNode is TACK-466: deleting one of the 104
// renamed nodes used to abort the whole reconstruction on a resolve error, so
// the operator got no counts at all. The rename still happened, so the event is
// still owed; only the node's type is lost, and the event says so.
func TestRenameClassSurvivesADeletedNode(t *testing.T) {
	t.Parallel()
	renames, err := loadReferenceRenameEvidence(t.Context())
	if err != nil {
		t.Fatalf("load evidence: %v", err)
	}
	deleted := firstEvidenceNode(t, renames)
	reader := absentNodeReader{absent: map[uuid.UUID]bool{deleted: true}, err: nil}

	resolutions, err := resolveRenameEvidence(context.Background(), reader, renames)
	if err != nil {
		t.Fatalf("resolve with one node deleted: %v", err)
	}
	if len(resolutions.Unresolvable) != 1 || resolutions.Unresolvable[0] != deleted {
		t.Fatalf("unresolvable = %v, want exactly the deleted node %s", resolutions.Unresolvable, deleted)
	}
	if resolutions.OrgID == uuid.Nil {
		t.Fatal("the org must still come from the nodes that do resolve")
	}

	event, err := referenceRenameEvent(context.Background(), resolutions,
		audit.OperatorPrincipal{ID: uuid.Must(uuid.NewV7())}, renames[0], time.Now().UTC())
	if err != nil {
		t.Fatalf("build the deleted node's event: %v", err)
	}
	if event.Context.OrgID != resolutions.OrgID {
		t.Fatalf("event org = %s, want the shared org %s", event.Context.OrgID, resolutions.OrgID)
	}
	if event.Entity.ID != deleted {
		t.Fatalf("event entity = %s, want the deleted node %s", event.Entity.ID, deleted)
	}
	if event.Entity.NodeType != "" {
		t.Fatalf("node type = %q, want empty: live state is the only source and the node is gone", event.Entity.NodeType)
	}
	var extra referenceRenameExtra
	if err := json.Unmarshal(event.Extra, &extra); err != nil {
		t.Fatalf("decode extra: %v", err)
	}
	if !extra.SubjectAbsent {
		t.Fatal("the event must mark that its node no longer exists")
	}
	if extra.NewReference != renames[0].NewReference {
		t.Fatalf("new reference = %q, want the rename the evidence records", extra.NewReference)
	}
}

// TestRenameClassCountsAbsentSubjectsWithoutLoweringDerived pins that a
// deletion never shrinks the reconstruction. The repair renamed 104 nodes, so
// 104 events are owed whatever happened to the nodes since.
func TestRenameClassCountsAbsentSubjectsWithoutLoweringDerived(t *testing.T) {
	t.Parallel()
	renames, err := loadReferenceRenameEvidence(t.Context())
	if err != nil {
		t.Fatalf("load evidence: %v", err)
	}
	reader := absentNodeReader{absent: map[uuid.UUID]bool{firstEvidenceNode(t, renames): true}, err: nil}
	resolutions, err := resolveRenameEvidence(context.Background(), reader, renames)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	events := make([]audit.Event, 0, len(renames))
	for _, rename := range renames {
		event, eventErr := referenceRenameEvent(context.Background(), resolutions,
			audit.OperatorPrincipal{ID: uuid.Must(uuid.NewV7())}, rename, time.Now().UTC())
		if eventErr != nil {
			t.Fatalf("build event: %v", eventErr)
		}
		events = append(events, event)
	}
	count := auditBackfillCount{
		Class: "reference renames", Derived: len(events), Recorded: recordedReferenceRenames,
		DeletedSubjects: 0, AbsentSubjects: len(resolutions.Unresolvable),
	}
	if count.Derived != recordedReferenceRenames {
		t.Fatalf("derived = %d, want the recorded %d even with a node deleted", count.Derived, recordedReferenceRenames)
	}
	if count.AbsentSubjects != 1 {
		t.Fatalf("absent subjects = %d, want 1", count.AbsentSubjects)
	}
	plan := auditBackfillPlan{Classes: []auditBackfillClass{{Count: count, Events: events}}}
	if err := plan.Validate(context.Background()); err != nil {
		t.Fatalf("the class must still validate: %v", err)
	}
}

// TestResolveRenameEvidenceRefusesAReadFailure pins the distinction that makes
// the tolerance safe: a node that is absent is counted, but a read that failed
// is not evidence of absence and must stop the run.
func TestResolveRenameEvidenceRefusesAReadFailure(t *testing.T) {
	t.Parallel()
	renames, err := loadReferenceRenameEvidence(t.Context())
	if err != nil {
		t.Fatalf("load evidence: %v", err)
	}
	reader := absentNodeReader{absent: nil, err: errors.New("foundationdb unavailable")}

	if _, err := resolveRenameEvidence(context.Background(), reader, renames); err == nil ||
		!strings.Contains(err.Error(), "foundationdb unavailable") {
		t.Fatalf("err = %v, want the read failure surfaced", err)
	}
}

// TestResolveRenameEvidenceRefusesWhenEveryNodeIsGone pins the one case that
// cannot degrade: with no node resolving, nothing names the org the
// reconstructed events belong to, so the run refuses rather than guess.
func TestResolveRenameEvidenceRefusesWhenEveryNodeIsGone(t *testing.T) {
	t.Parallel()
	renames, err := loadReferenceRenameEvidence(t.Context())
	if err != nil {
		t.Fatalf("load evidence: %v", err)
	}
	absent := map[uuid.UUID]bool{}
	for _, rename := range renames {
		absent[uuid.MustParse(rename.NodeID)] = true
	}
	reader := absentNodeReader{absent: absent, err: nil}

	if _, err := resolveRenameEvidence(context.Background(), reader, renames); err == nil ||
		!strings.Contains(err.Error(), "no renamed node resolves") {
		t.Fatalf("err = %v, want the refusal naming the missing org", err)
	}
}
