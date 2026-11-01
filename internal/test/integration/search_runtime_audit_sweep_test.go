//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// sweepAuditQuery is the included text of the sweep corpus.
	sweepAuditQuery = "ochre cistern sweep audit"
	// interruptedSliceKeys stops a cleanup after the replay record and the
	// first token chunk.
	interruptedSliceKeys = 2
)

// sweptSession is one stored session and the replay of its first page.
type sweptSession struct {
	session searchdomain.Session
	page    integration.SearchPage
	replay  searchdomain.PageCommit
}

func (f sessionAuditFixture) openSwept(t *testing.T, token string) sweptSession {
	t.Helper()
	page := f.mustSearch(t, token, sweepAuditQuery, "")
	session := sessionOfCursor(t, f.parentEdgeFixture, page.Cursor)
	store := f.stores.SearchSessions(clock.Wall{}, time.Minute)
	replay, found, err := store.Replay(t.Context(), session.ID, 0)
	if err != nil || !found {
		t.Fatalf("read the replay of session %s: found %t, %v", session.ID, found, err)
	}
	return sweptSession{session: session, page: page, replay: replay}
}

// sweepAfterExpiry waits past the expiry minute of session and runs a search
// as token that sweeps it.
func (f sessionAuditFixture) sweepAfterExpiry(t *testing.T, token string, session searchdomain.Session) {
	t.Helper()
	waitUntilTime(t, session.IdleDeadline.Truncate(time.Minute).Add(time.Minute+sessionAuditMargin))
	f.mustSearch(t, token, sweepAuditQuery, "")
}

// TestSearchRuntimeAuditSweepRemovalValues opens two sessions as one user. It
// stops the cleanup of the second session after one slice, and a search by a
// second user sweeps both after expiry. The delete of the first session must
// name the second user as actor and the owner in principal_id. Its protected
// old values must equal the stored query text, tokens, sort, and access keys,
// and its replay and visited removals must equal the stored records. The
// interrupted session must keep the events of each committed transaction.
func TestSearchRuntimeAuditSweepRemovalValues(t *testing.T) {
	setShortSessions(t)
	fixture := newSessionAuditFixture(t)
	fixture.putCorpus(t, sweepAuditQuery, sessionAuditNodes)
	owner, sweeper := fixture.first.Actors[0], fixture.first.Actors[1]
	swept, interrupted := fixture.openSwept(t, owner.Token), fixture.openSwept(t, owner.Token)
	sliceContext := integration.WithTestOperation(t, t.Context())
	store := fixture.stores.SearchSessions(clock.Wall{}, time.Minute)
	if err := store.BeginCleanup(sliceContext, interrupted.session.ID); err != nil {
		t.Fatalf("close session %s: %v", interrupted.session.ID, err)
	}
	if done, err := store.CleanupSlice(sliceContext, interrupted.session.ID, interruptedSliceKeys); err != nil || done {
		t.Fatalf("one slice of session %s = %t, %v, want a partly deleted session", interrupted.session.ID, done, err)
	}
	fixture.sweepAfterExpiry(t, sweeper.Token, interrupted.session)

	rows := fixture.waitForRows(t, fixture.first.OrgID, func(current []audit.Row) bool {
		return len(sessionChanges(current, swept.session.ID, audit.VerbSearchSessionDelete, audit.TargetSearchSession)) == 1 &&
			len(sessionChanges(current, interrupted.session.ID, audit.VerbSearchSessionDelete, audit.TargetSearchSession)) == 1
	})
	fixture.requireSweptValues(t, rows, swept, sweeper.UserID, owner.UserID)
	requireRecordRemovals(t, rows, swept)
	fixture.requireInterruptedEvents(t, rows, interrupted, sweeper.UserID)
}

// requireSweptValues requires the session delete to store every old value and
// the owner under the actor of the sweep.
func (f sessionAuditFixture) requireSweptValues(t *testing.T, rows []audit.Row, swept sweptSession, actorID, ownerID uuid.UUID) {
	t.Helper()
	session := swept.session
	deleted := onlyRow(t, sessionChanges(rows, session.ID, audit.VerbSearchSessionDelete, audit.TargetSearchSession), "the session delete")
	if deleted.ActorID != actorID || deleted.EntityID != session.ID || deleted.Context.Target.OwnerID != ownerID {
		t.Fatalf("session delete has actor %s, entity %s, target %+v, want actor %s and owner %s", deleted.ActorID, deleted.EntityID, deleted.Context.Target, actorID, ownerID)
	}
	if deleted.Delta == nil || len(deleted.Delta.After) != 0 {
		t.Fatalf("session delete delta = %+v, want an old value only", deleted.Delta)
	}
	before := deltaMembers(t, deleted.Delta.Before)
	if string(before["principal_id"]) != string(encodeJSON(t, ownerID)) || string(before["closing"]) != "true" {
		t.Fatalf("session delete old value = %s, want a closing session of owner %s", deleted.Delta.Before, ownerID)
	}
	f.requireProtectedMember(t, before, "text", encodeJSON(t, session.Query.Text))
	f.requireProtectedMember(t, before, "query_tokens", session.Snapshot.QueryTokens)
	f.requireProtectedMember(t, before, "sort", session.Sort)
	f.requireProtectedMember(t, before, "access_keys", encodeJSON(t, session.Query.Access.Keys))
}

// requireRecordRemovals requires removal values equal to the stored replay and
// visited records.
func requireRecordRemovals(t *testing.T, rows []audit.Row, swept sweptSession) {
	t.Helper()
	sessionID := swept.session.ID
	replayRow := onlyRow(t, sessionChanges(rows, sessionID, audit.VerbSearchSessionDelete, audit.TargetSearchSessionReplay), "the replay delete")
	var replay struct {
		ResultIDs []uuid.UUID `json:"result_ids"`
		Complete  bool        `json:"complete"`
	}
	if err := json.Unmarshal(replayRow.Delta.Before, &replay); err != nil {
		t.Fatalf("decode replay value %s: %v", replayRow.Delta.Before, err)
	}
	if !slices.Equal(replay.ResultIDs, swept.replay.ResultIDs) || replay.Complete != swept.replay.Complete {
		t.Fatalf("replay removal = %+v, want %+v", replay, swept.replay)
	}
	visited := map[uuid.UUID]bool{}
	for _, row := range sessionChanges(rows, sessionID, audit.VerbSearchSessionDelete, audit.TargetSearchSessionVisited) {
		var value struct {
			NodeID uuid.UUID `json:"node_id"`
		}
		if err := json.Unmarshal(row.Delta.Before, &value); err != nil {
			t.Fatalf("decode visited value %s: %v", row.Delta.Before, err)
		}
		visited[value.NodeID] = true
	}
	for _, nodeID := range swept.page.IDs {
		if !visited[nodeID] {
			t.Fatalf("session %s does not record the removal of visited node %s", sessionID, nodeID)
		}
	}
}

// requireInterruptedEvents requires the closing update, the token removal of
// the stopped slice, and the later delete of the header under the sweep actor.
// The later delete omits query_tokens, because the stopped slice removed them.
func (f sessionAuditFixture) requireInterruptedEvents(t *testing.T, rows []audit.Row, interrupted sweptSession, sweeperID uuid.UUID) {
	t.Helper()
	session := interrupted.session
	updates := sessionChanges(rows, session.ID, audit.VerbSearchSessionUpdate, audit.TargetSearchSession)
	closed, tokensRemoved := false, false
	for _, update := range updates {
		before, after := deltaMembers(t, update.Delta.Before), deltaMembers(t, update.Delta.After)
		closed = closed || string(after["closing"]) == "true"
		if _, found := before["query_tokens"]; found {
			tokensRemoved = true
			f.requireProtectedMember(t, before, "query_tokens", session.Snapshot.QueryTokens)
			if _, kept := after["query_tokens"]; kept {
				t.Fatalf("token removal new value = %s, want query_tokens absent", update.Delta.After)
			}
		}
	}
	if !closed || !tokensRemoved {
		t.Fatalf("session %s records closing %t and token removal %t, want both: %s", session.ID, closed, tokensRemoved, describeRows(updates))
	}
	onlyRow(t, sessionChanges(rows, session.ID, audit.VerbSearchSessionDelete, audit.TargetSearchSessionReplay), "the replay delete of the stopped slice")
	deleted := onlyRow(t, sessionChanges(rows, session.ID, audit.VerbSearchSessionDelete, audit.TargetSearchSession), "the header delete")
	before := deltaMembers(t, deleted.Delta.Before)
	if _, found := before["query_tokens"]; found || deleted.ActorID != sweeperID {
		t.Fatalf("header delete has actor %s and old value %s, want actor %s without query_tokens", deleted.ActorID, deleted.Delta.Before, sweeperID)
	}
}
