package ops

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestReferenceShapeHonorsUnrecordedDeletions pins TACK-473: an org whose
// ledger records post-repair deletions that name no node cannot hold the whole
// corpus, because the reconstruction counts those deletions on top of what is
// present. The generator leaves that many plain issues out, the same ones
// every run, and never one the repair keeps a counter or a collision on.
func TestReferenceShapeHonorsUnrecordedDeletions(t *testing.T) {
	shape := referenceShapeUnderTest(t)
	deletions := referenceShapeDeletions{Recorded: nil, Unrecorded: 4}

	reduced, removed, err := applyReferenceShapeDeletions(shape, deletions)
	if err != nil {
		t.Fatalf("applyReferenceShapeDeletions: %v", err)
	}
	if len(removed) != 4 || len(reduced.Issues) != len(shape.Issues)-4 {
		t.Fatalf("removed %d issues leaving %d, want 4 leaving %d",
			len(removed), len(reduced.Issues), len(shape.Issues)-4)
	}
	highWater := make(map[string]int, len(shape.Projects))
	for _, project := range shape.Projects {
		highWater[project.Identifier] = project.HighWater
	}
	colliding := make(map[string]bool, len(shape.Groups))
	for _, group := range shape.Groups {
		colliding[group.Reference] = true
	}
	for _, issue := range removed {
		reference := referenceShapeReference(issue.Project, issue.Sequence)
		if issue.Colliding || colliding[reference] {
			t.Fatalf("removed %s, which the repair must rename or keep", reference)
		}
		if issue.Sequence == highWater[issue.Project] {
			t.Fatalf("removed %s, which seeds its scope's counter", reference)
		}
	}
	if err := validateReferenceShapeScopes(reduced); err != nil {
		t.Fatalf("the reduced corpus must still seed every counter: %v", err)
	}
	if reduced.Renames != shape.Renames || len(reduced.Groups) != len(shape.Groups) {
		t.Fatal("unrecorded deletions must not touch the collisions")
	}

	again, _, err := applyReferenceShapeDeletions(shape, deletions)
	if err != nil {
		t.Fatalf("second application: %v", err)
	}
	for index := range reduced.Issues {
		if reduced.Issues[index].ID != again.Issues[index].ID {
			t.Fatal("the same deletions must leave out the same issues every run")
		}
	}
}

// TestReferenceShapeHonorsARecordedDeletionOfARenamedNode pins the case
// TACK-466 is proven with: the ledger names a deleted node the repair renamed.
// That node stays absent, its collision has one holder fewer, and the shape's
// rename count drops with it, so the live counts the commit checks still agree.
func TestReferenceShapeHonorsARecordedDeletionOfARenamedNode(t *testing.T) {
	shape := referenceShapeUnderTest(t)
	group := shape.Groups[0]
	deleted := group.Renamed[0]

	reduced, removed, err := applyReferenceShapeDeletions(shape,
		referenceShapeDeletions{Recorded: []uuid.UUID{deleted}, Unrecorded: 0})
	if err != nil {
		t.Fatalf("applyReferenceShapeDeletions: %v", err)
	}
	if len(removed) != 1 || removed[0].ID != deleted {
		t.Fatalf("removed = %+v, want the deleted node alone", removed)
	}
	for _, issue := range reduced.Issues {
		if issue.ID == deleted {
			t.Fatal("a node the ledger records as deleted must not be written")
		}
	}
	if reduced.Renames != shape.Renames-1 {
		t.Fatalf("renames = %d, want %d", reduced.Renames, shape.Renames-1)
	}
	for _, reducedGroup := range reduced.Groups {
		for _, renamed := range reducedGroup.Renamed {
			if renamed == deleted {
				t.Fatal("a deleted node must leave its collision")
			}
		}
	}
	wantGroups := len(shape.Groups)
	if len(group.Renamed) == 1 {
		wantGroups--
	}
	if len(reduced.Groups) != wantGroups {
		t.Fatalf("groups = %d, want %d", len(reduced.Groups), wantGroups)
	}
}

// TestReferenceShapeRefusesADeletionItCannotPlace pins two refusals: a
// recorded deletion of a node the shape never held, which the reconstruction
// would count past the recorded number, and more unrecorded deletions than
// there are plain issues to leave out.
func TestReferenceShapeRefusesADeletionItCannotPlace(t *testing.T) {
	shape := referenceShapeUnderTest(t)

	_, _, err := applyReferenceShapeDeletions(shape,
		referenceShapeDeletions{Recorded: []uuid.UUID{uuid.New()}, Unrecorded: 0})
	if err == nil || !strings.Contains(err.Error(), "never held") {
		t.Fatalf("err = %v, want the unknown node refused", err)
	}

	_, _, err = applyReferenceShapeDeletions(shape,
		referenceShapeDeletions{Recorded: nil, Unrecorded: len(shape.Issues)})
	if err == nil || !strings.Contains(err.Error(), "plain issues") {
		t.Fatalf("err = %v, want the shortage refused", err)
	}
}

// TestReferenceShapeCommitExpectsTheReducedKeyCount pins that the commit
// check wants the keys of the corpus it wrote, not the full recorded count,
// once deletions are honored: present plus deleted is what the reconstruction
// derives, so present alone must be the recorded count less the deletions.
func TestReferenceShapeCommitExpectsTheReducedKeyCount(t *testing.T) {
	result := datagenReferenceShapeResult{
		Collisions: 102, Renames: 103, LiveCollisions: 102, LiveRenames: 103,
		CounterKeys:               recordedCounterSeeds,
		ReferenceKeys:             recordedReferenceKeys + recordedFollowupReferenceKey - 4,
		DeletedSubjectsUnrecorded: 4,
	}
	if err := checkReferenceShape(result); err != nil {
		t.Fatalf("a corpus short by its four honored deletions must pass: %v", err)
	}
	result.ReferenceKeys = recordedReferenceKeys + recordedFollowupReferenceKey
	if err := checkReferenceShape(result); err == nil {
		t.Fatal("a corpus holding every key while the ledger records deletions must refuse")
	}
}
