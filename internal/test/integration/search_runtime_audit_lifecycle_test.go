//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// lifecycleQuery is the included text of the lifecycle corpus.
	lifecycleQuery = "sable orchard lifecycle audit"
	// lifecycleNodes fills two pages of 25 results and part of a third.
	lifecycleNodes = 60
	// lifecyclePages is the smallest page count of the traversal.
	lifecyclePages = 3
)

// sessionPages is one traversal: the pages in order and the stored session
// after each page that returned a cursor.
type sessionPages struct {
	pages  []integration.SearchPage
	stored []searchdomain.Session
}

// TestSearchRuntimeAuditSessionLifecycle traverses one session through
// tack_search and inspects its permanent events. The session create must
// reference the query text, tokens, and access keys. Each page must record
// the old and new sort, version, and idle deadline, the replay records, and
// one visited record per consumed node. The final page must record completion
// and the removed version entry. The point in time create and delete must
// record their intents and results, and each change must state the node.search
// operation as its parent.
func TestSearchRuntimeAuditSessionLifecycle(t *testing.T) {
	fixture := newSessionAuditFixture(t)
	fixture.putCorpus(t, lifecycleQuery, lifecycleNodes)
	owner := fixture.first.Actors[0]
	traversal := fixture.traverse(t, owner.Token)
	session := traversal.stored[0]
	pageCount := len(traversal.pages)
	if pageCount < lifecyclePages {
		t.Fatalf("the traversal returned %d pages, want at least %d", pageCount, lifecyclePages)
	}
	rows := fixture.waitForRows(t, fixture.first.OrgID, func(current []audit.Row) bool {
		return len(stepRows(current, session.ID, audit.VerbSearchPointInTimeDelete, "delete")) == 2 &&
			len(sessionChanges(current, session.ID, audit.VerbSearchSessionUpdate, audit.TargetSearchSession)) == pageCount
	})
	fixture.requireSessionCreate(t, rows, session, owner.UserID)
	requireSessionUpdates(t, fixture, rows, traversal)
	requireReplayRecords(t, rows, traversal)
	requireVisitedRecords(t, rows, traversal)
	requirePointInTimeSteps(t, rows, session)
	requireSearchParents(t, rows, session.ID)
}

// traverse reads every page of lifecycleQuery using token to authenticate.
func (f sessionAuditFixture) traverse(t *testing.T, token string) sessionPages {
	t.Helper()
	traversal := sessionPages{pages: nil, stored: nil}
	cursor := ""
	for {
		page := f.mustSearch(t, token, lifecycleQuery, cursor)
		traversal.pages = append(traversal.pages, page)
		if page.Complete {
			return traversal
		}
		traversal.stored = append(traversal.stored, sessionOfCursor(t, f.parentEdgeFixture, page.Cursor))
		cursor = page.Cursor
	}
}

func (f sessionAuditFixture) requireSessionCreate(t *testing.T, rows []audit.Row, session searchdomain.Session, ownerID uuid.UUID) {
	t.Helper()
	created := onlyRow(t, sessionChanges(rows, session.ID, audit.VerbSearchSessionCreate, audit.TargetSearchSession), "the session create")
	target := created.Context.Target
	if target.Kind != audit.TargetKindResource || target.ID != session.ID || target.OwnerID != ownerID ||
		target.URI != "tack:search-session/"+session.ID.String() || target.Reference != "" || created.EntityID != session.ID {
		t.Fatalf("session create target = %+v, entity %s, want a resource target of session %s and owner %s", target, created.EntityID, session.ID, ownerID)
	}
	if created.Delta == nil || len(created.Delta.Before) != 0 {
		t.Fatalf("session create delta = %+v, want a new value only", created.Delta)
	}
	after := deltaMembers(t, created.Delta.After)
	f.requireProtectedMember(t, after, "text", encodeJSON(t, session.Query.Text))
	f.requireProtectedMember(t, after, "query_tokens", session.Snapshot.QueryTokens)
	f.requireProtectedMember(t, after, "access_keys", encodeJSON(t, session.Query.Access.Keys))
	if string(after["version_entry"]) != "true" || string(after["complete"]) != "false" || string(after["principal_id"]) != string(encodeJSON(t, ownerID)) {
		t.Fatalf("session create value = %s, want an open session of owner %s", created.Delta.After, ownerID)
	}
}

func requireSessionUpdates(t *testing.T, fixture sessionAuditFixture, rows []audit.Row, traversal sessionPages) {
	t.Helper()
	updates := sessionChanges(rows, traversal.stored[0].ID, audit.VerbSearchSessionUpdate, audit.TargetSearchSession)
	slices.SortFunc(updates, func(left, right audit.Row) int {
		return compareVersions(t, left, right)
	})
	last := len(updates) - 1
	for position, update := range updates {
		before, after := deltaMembers(t, update.Delta.Before), deltaMembers(t, update.Delta.After)
		if string(before["version"]) != string(encodeJSON(t, position)) || string(after["version"]) != string(encodeJSON(t, position+1)) {
			t.Fatalf("update %d changes version %s to %s", position, before["version"], after["version"])
		}
		if string(before["idle_deadline"]) == "" || string(before["idle_deadline"]) == string(after["idle_deadline"]) {
			t.Fatalf("update %d changes idle_deadline %s to %s", position, before["idle_deadline"], after["idle_deadline"])
		}
		if _, found := before["sort"]; found == (position == 0) {
			t.Fatalf("update %d old value = %s, want an old sort after the first page only", position, update.Delta.Before)
		}
		if position < len(traversal.stored) {
			fixture.requireProtectedMember(t, after, "sort", traversal.stored[position].Sort)
		}
		if position > 0 {
			fixture.requireProtectedMember(t, before, "sort", traversal.stored[position-1].Sort)
		}
		_, completed := after["complete"]
		if completed != (position == last) {
			t.Fatalf("update %d new value = %s, want complete on the final page only", position, update.Delta.After)
		}
	}
	finalBefore, finalAfter := deltaMembers(t, updates[last].Delta.Before), deltaMembers(t, updates[last].Delta.After)
	if string(finalBefore["complete"]) != "false" || string(finalAfter["complete"]) != "true" ||
		string(finalBefore["version_entry"]) != "true" || string(finalAfter["version_entry"]) != "false" {
		t.Fatalf("final update = %s to %s, want complete and version_entry to change", updates[last].Delta.Before, updates[last].Delta.After)
	}
}

func compareVersions(t *testing.T, left, right audit.Row) int {
	t.Helper()
	var leftVersion, rightVersion int
	if json.Unmarshal(deltaMembers(t, left.Delta.Before)["version"], &leftVersion) != nil ||
		json.Unmarshal(deltaMembers(t, right.Delta.Before)["version"], &rightVersion) != nil {
		t.Fatalf("decode update versions of %s and %s", left.EventID, right.EventID)
	}
	return leftVersion - rightVersion
}
