//go:build runtimeaudit

package integration_test

import (
	"context"
	"strings"
	"testing"

	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

// TestSearchRuntimeAuditRetireCorruptHeaderKeepsReplacement opens a search
// session on the serving index and runs the production search worker until
// the replacement state is RebuildRetiring. The test replaces the session
// header with invalid JSON, then runs worker slices with deadlines until a
// slice returns the decode error. After every slice, the test checks that
// CurrentRebuild finds the replacement and the source index exists.
func TestSearchRuntimeAuditRetireCorruptHeaderKeepsReplacement(t *testing.T) {
	t.Setenv("OPENSEARCH_QUERY_MAX_RESULTS", openSessionResults)
	fixture := newParentEdgeFixture(t)
	source := servingIndex(t, fixture)
	putRetiringCorpus(t, fixture)
	session := sessionOfCursor(t, fixture, searchPageOf(t, fixture, openSessionQuery, "").cursor)
	beginReplacement(t, fixture)
	runReplacementUntil(t, fixture, func(current searchdomain.Rebuild, found bool) bool {
		return found && current.State == searchdomain.RebuildRetiring
	})
	integration.ReplaceRawValue(t, retireFamilyKey(t, sessionHeaderFamily), []byte(invalidHeaderValue))

	rebuilds := fixture.stores.SearchRebuilds(clock.Wall{})
	deadline := clock.Now().Add(replacementDeadline)
	for clock.Now().Before(deadline) {
		sliceContext, cancel := context.WithTimeout(t.Context(), retireLoadDeadline)
		claimed, sliceErr := fixture.worker.RunSlice(sliceContext)
		cancel()
		if _, found, err := rebuilds.CurrentRebuild(t.Context()); err != nil || !found {
			t.Fatalf("the replacement after a corrupt header on session %s: found %t, %v, want it unfinished", session.ID, found, err)
		}
		if _, err := fixture.adapter.IndexSettings(t.Context(), source); err != nil {
			t.Fatalf("read source index %s after a corrupt header on session %s: %v", source, session.ID, err)
		}
		if sliceErr != nil && strings.Contains(sliceErr.Error(), retireLoadDecodeText) {
			return
		}
		if !claimed {
			waitUntilTime(t, clock.Now().Add(replacementPause))
		}
	}
	t.Fatalf("the search worker did not report the corrupt header of session %s within %s", session.ID, replacementDeadline)
}
