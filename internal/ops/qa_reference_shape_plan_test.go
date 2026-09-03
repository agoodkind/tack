package ops

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// referenceShapeUnderTest derives the corpus from the embedded rename
// evidence, which is the only input the command has.
func referenceShapeUnderTest(t *testing.T) referenceShape {
	t.Helper()
	renames, err := loadReferenceRenameEvidence(t.Context())
	if err != nil {
		t.Fatalf("load rename evidence: %v", err)
	}
	shape, err := deriveReferenceShape(renames)
	if err != nil {
		t.Fatalf("derive reference shape: %v", err)
	}
	return shape
}

// TestReferenceShapeMatchesTheRecordedRepair is the whole point of the corpus:
// the counts the ledger reconstruction refuses to write unless they match are
// the counts this corpus produces. A shape that misses any of them makes the
// reconstruction unprovable on a testbed.
func TestReferenceShapeMatchesTheRecordedRepair(t *testing.T) {
	shape := referenceShapeUnderTest(t)

	if len(shape.Issues) != recordedReferenceKeys+recordedFollowupReferenceKey {
		t.Fatalf("issues = %d, want %d", len(shape.Issues),
			recordedReferenceKeys+recordedFollowupReferenceKey)
	}
	if len(shape.Projects) != recordedCounterSeeds {
		t.Fatalf("scopes = %d, want %d", len(shape.Projects), recordedCounterSeeds)
	}
	if shape.Renames != recordedReferenceRenames-recordedFollowupReferenceKey {
		t.Fatalf("renames = %d, want %d", shape.Renames,
			recordedReferenceRenames-recordedFollowupReferenceKey)
	}
}

// TestReferenceShapeReproducesTheRecordedRenames walks the corpus the way the
// repair does: collisions in rendered-reference order, the nodes inside one
// collision oldest first, each renamed node taking the next value from its
// scope's counter. The references that fall out have to be the ones the
// evidence recorded, or the testbed run proves something other than the repair
// production performed.
func TestReferenceShapeReproducesTheRecordedRenames(t *testing.T) {
	shape := referenceShapeUnderTest(t)
	renames, err := loadReferenceRenameEvidence(t.Context())
	if err != nil {
		t.Fatalf("load rename evidence: %v", err)
	}
	recorded := make(map[uuid.UUID]string, len(renames))
	for _, rename := range renames {
		if historicalReferenceRenameTime(rename) != referenceRepairDate {
			continue
		}
		nodeID, parseErr := uuid.Parse(rename.NodeID)
		if parseErr != nil {
			t.Fatalf("evidence node id %q: %v", rename.NodeID, parseErr)
		}
		recorded[nodeID] = rename.NewReference
	}

	counters := make(map[string]int, len(shape.Projects))
	for _, project := range shape.Projects {
		counters[project.Identifier] = project.HighWater
	}
	replayed := 0
	for _, group := range shape.Groups {
		for _, renamed := range group.Renamed {
			counters[group.Project]++
			want, ok := recorded[renamed]
			if !ok {
				t.Fatalf("node %s is renamed by the corpus and absent from the evidence", renamed)
			}
			got := referenceShapeReference(group.Project, counters[group.Project])
			if got != want {
				t.Fatalf("node %s takes %s, evidence records %s", renamed, got, want)
			}
			replayed++
		}
	}
	if replayed != len(recorded) {
		t.Fatalf("replayed %d renames, evidence records %d", replayed, len(recorded))
	}
}

// TestReferenceShapeCollidesOnlyOnEvidenceReferences pins that the corpus is
// bad data in exactly one way. A sequence repeated anywhere else would make
// the repair rename an issue production never renamed.
func TestReferenceShapeCollidesOnlyOnEvidenceReferences(t *testing.T) {
	shape := referenceShapeUnderTest(t)
	holders := make(map[string]int, len(shape.Issues))
	for _, issue := range shape.Issues {
		holders[referenceShapeReference(issue.Project, issue.Sequence)]++
	}
	colliding := make(map[string]bool, len(shape.Groups))
	for _, group := range shape.Groups {
		colliding[group.Reference] = true
		if holders[group.Reference] != len(group.Renamed)+1 {
			t.Fatalf("%s is held by %d issues, want %d",
				group.Reference, holders[group.Reference], len(group.Renamed)+1)
		}
	}
	for reference, count := range holders {
		if count > 1 && !colliding[reference] {
			t.Fatalf("%s is held by %d issues and the evidence never names it", reference, count)
		}
	}
}

// TestReferenceShapeCommitRefusesWhenTheOrgHoldsNoCollisions pins TACK-475:
// a commit whose report says 102 collisions while the org holds none used to
// exit 0, and the repair run against it then had nothing to rename. The check
// now compares what the shape describes with what the duplicate scan found.
func TestReferenceShapeCommitRefusesWhenTheOrgHoldsNoCollisions(t *testing.T) {
	result := datagenReferenceShapeResult{
		Collisions:     102,
		LiveCollisions: 0,
		CounterKeys:    recordedCounterSeeds,
		ReferenceKeys:  recordedReferenceKeys + recordedFollowupReferenceKey,
	}

	err := checkReferenceShape(result)

	if err == nil {
		t.Fatal("a corpus with no live collisions must not be reported as written")
	}
	if !strings.Contains(err.Error(), "holds 0 colliding references") {
		t.Fatalf("err = %v, want the live count named", err)
	}
	result.LiveCollisions = 102
	if err := checkReferenceShape(result); err != nil {
		t.Fatalf("a corpus whose live collisions match the shape must pass: %v", err)
	}
}
