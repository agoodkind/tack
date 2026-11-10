package integration_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	openSessionNodes    = 60
	openSessionEdited   = 4
	openSessionQuery    = "harbor lantern"
	openSessionIdle     = "10s"
	openSessionAbsolute = "60s"
	openSessionResults  = "1"
	openSessionStep     = 3 * time.Second
)

// TestSearchRebuildKeepsOpenSessionOnOldIndex requires a session opened before
// the alias switch to continue reading its original index during retirement.
// New sessions must use the target index, and the worker must preserve the
// source index, its retirement time, and the old session's presence entry
// while that session can continue. The replacement must remain unfinished
// during those continuations.
// After the absolute deadline, continuation must fail and the worker must
// finish the replacement. The serving index and public alias must select the
// target, and the source index settings lookup must fail. Cleanup must remove
// the source retirement time, its presence entries, and the old session record.
func TestSearchRebuildKeepsOpenSessionOnOldIndex(t *testing.T) {
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", openSessionIdle)
	t.Setenv("OPENSEARCH_SESSION_ABSOLUTE_TIMEOUT", openSessionAbsolute)
	t.Setenv("OPENSEARCH_QUERY_MAX_RESULTS", openSessionResults)
	fixture := newParentEdgeFixture(t)
	source := servingIndex(t, fixture)
	putRetiringCorpus(t, fixture)
	requireRetiredSince(t, fixture, source, true)
	target := beginReplacement(t, fixture)
	runReplacementUntil(t, fixture, func(current searchdomain.Rebuild, found bool) bool {
		return found && current.State == searchdomain.RebuildSwitching
	})
	first := searchPageOf(t, fixture, openSessionQuery, "")
	old := sessionOfCursor(t, fixture, first.cursor)
	if old.Query.Index != source {
		t.Fatalf("session %s reads index %s before the switch, want %s", old.ID, old.Query.Index, source)
	}
	cursor := continueThroughRetirement(t, fixture, old, first.cursor, target)
	waitUntilTime(t, old.AbsoluteDeadline.Add(time.Second))
	if _, err := trySearchPage(t, fixture, openSessionQuery, cursor); err == nil {
		t.Fatalf("session %s continued after its absolute deadline %s", old.ID, old.AbsoluteDeadline)
	}
	runReplacementUntil(t, fixture, replacementFinished)
	requireReplaced(t, fixture, target, source)
	requireRetiredSince(t, fixture, source, false)
	if presence := presenceSessions(t, fixture, source); len(presence) != 0 {
		t.Fatalf("index %s keeps presence entries of sessions %v after the replacement", source, presence)
	}
	_, err := fixture.stores.SearchSessions(clock.Wall{}, time.Minute).Load(t.Context(), old.ID)
	if !errors.Is(err, searchdomain.ErrSessionNotFound) {
		t.Fatalf("load session %s after the replacement: %v, want %v", old.ID, err, searchdomain.ErrSessionNotFound)
	}
}

// putRetiringCorpus creates and indexes the session test corpus, then edits
// selected nodes and drains the resulting search work.
func putRetiringCorpus(t *testing.T, fixture parentEdgeFixture) {
	t.Helper()
	edited := make([]uuid.UUID, 0, openSessionEdited)
	for number := range openSessionNodes {
		nodeID := fixture.createLeaf(t, fixture.firstEntry, fmt.Sprintf("%s %02d", openSessionQuery, number))
		if number < openSessionEdited {
			edited = append(edited, nodeID)
		}
	}
	fixture.drain(t)
	for _, nodeID := range edited {
		editLeafText(t, fixture, nodeID, openSessionQuery+" revised")
	}
	fixture.drain(t)
}
