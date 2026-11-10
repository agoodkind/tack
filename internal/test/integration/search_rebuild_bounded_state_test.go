package integration_test

import (
	"fmt"
	"maps"
	"testing"

	"github.com/google/uuid"
)

const (
	boundedStateNodes     = 4
	boundedStateFewEdits  = 2
	boundedStateManyEdits = 12
	boundedStateQuery     = "bounded state revision"
	boundedStateIdle      = "3s"
	boundedStateAbsolute  = "6s"
)

var completedStateFamilies = []string{
	"search_work", "search_age", "search_claim", "search_error", "search_attempt", "search_rebuild",
	sessionHeaderFamily, sessionChildFamily, sessionExpiryFamily, sessionPresentFamily, sessionVersionFamily,
}

// TestSearchRebuildAndEditHistoryLeaveBoundedState requires equal key counts
// in every search_ family after two edits per node and after twelve, following
// a full replacement and expired session cleanup in each phase.
// It excludes keys of sessions that cleanup searches create and requires
// work, age, claim, error, attempt, rebuild, and session families to be empty.
func TestSearchRebuildAndEditHistoryLeaveBoundedState(t *testing.T) {
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", boundedStateIdle)
	t.Setenv("OPENSEARCH_SESSION_ABSOLUTE_TIMEOUT", boundedStateAbsolute)
	fixture := newParentEdgeFixture(t)
	nodes := make([]uuid.UUID, 0, boundedStateNodes)
	for number := range boundedStateNodes {
		nodes = append(nodes, fixture.createLeaf(t, fixture.firstEntry, fmt.Sprintf("%s leaf %d", boundedStateText(0), number)))
	}
	few := convergeAfterEdits(t, fixture, nodes, 1, boundedStateFewEdits)
	many := convergeAfterEdits(t, fixture, nodes, boundedStateFewEdits+1, boundedStateManyEdits)
	if !maps.Equal(few, many) {
		t.Fatalf("search keys by family after %d edits of each node = %v, after %d edits = %v",
			boundedStateFewEdits, few, boundedStateManyEdits, many)
	}
}

func boundedStateText(revision int) string {
	return fmt.Sprintf("%s %04d of every node", boundedStateQuery, revision)
}

// convergeAfterEdits applies revisions from first through last, then finishes
// a full index replacement and expired session cleanup. It requires completed
// state families to be empty and returns family counts excluding the sessions
// that cleanup searches create.
func convergeAfterEdits(t *testing.T, fixture parentEdgeFixture, nodes []uuid.UUID, first, last int) map[string]int {
	t.Helper()
	for revision := first; revision <= last; revision++ {
		for _, nodeID := range nodes {
			editLeafText(t, fixture, nodeID, boundedStateText(revision))
		}
		fixture.drain(t)
		fixture.search(t, fixture.first, fmt.Sprintf("%s %04d", boundedStateQuery, revision))
	}
	beginReplacement(t, fixture)
	runReplacementUntil(t, fixture, replacementFinished)
	fixture.drain(t)
	fixture.search(t, fixture.first, fmt.Sprintf("%s after replacement %04d", boundedStateQuery, last))
	live := sweepExpiredSessions(t, fixture, boundedStateQuery)
	counts := countStateFamilies(readStateKeys(t, fixture), live)
	for _, family := range completedStateFamilies {
		if counts[family] != 0 {
			t.Fatalf("after %d edits of each node, %d %s keys remain outside the %d live sessions, want 0",
				last, counts[family], family, len(live))
		}
	}
	return counts
}
