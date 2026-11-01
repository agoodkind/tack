//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// retiredGeneration differs from the restore epoch of a new store. A
	// session of this generation cannot serve another page.
	retiredGeneration = 1
	// retireSliceLimit bounds the sessions of one retirement slice.
	retireSliceLimit = 10
)

// retiringSession creates a session on index that cannot serve another page.
func retiringSession(t *testing.T, stores *fdbadapter.Stores, index string) searchdomain.Session {
	t.Helper()
	session := storedSearchSession(sessionTokens(sessionStoreTokenBytes * 16))
	session.Query.Index, session.Query.Generation = index, retiredGeneration
	store := stores.SearchSessions(clock.Wall{}, sessionStoreIdle)
	if _, err := store.Create(integration.WithTestOperation(t, t.Context()), session); err != nil {
		t.Fatalf("create session %s: %v", session.ID, err)
	}
	return session
}

// cleanupFailureEvents returns the failed cleanup events of sessionID after
// mark.
func cleanupFailureEvents(t *testing.T, stores *fdbadapter.Stores, mark []byte, sessionID uuid.UUID, step searchdomain.CleanupStep) []audit.Event {
	t.Helper()
	entries, err := stores.OpsOutbox.ReadOutboxFrom(t.Context(), mark, searchAuditPageLimit)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	events := []audit.Event{}
	for _, entry := range entries {
		var event audit.Event
		if err := json.Unmarshal(entry.Event, &event); err != nil {
			t.Fatalf("decode outbox entry %s: %v", entry.Event, err)
		}
		if event.Entity.ID == sessionID && event.Error != nil && event.Error.Code == string(step) {
			events = append(events, event)
		}
	}
	return events
}

// TestSearchSessionAuditRetireFailuresRecorded retires two sessions under an
// operation that fails event validation. The first session fails its close
// step, and the second session is already closing and fails its delete step.
// RetireIndexSessions must return each error and record one failed result
// event with the organization and owner of the session.
func TestSearchSessionAuditRetireFailuresRecorded(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	cases := []struct {
		index   string
		closing bool
		step    searchdomain.CleanupStep
		verb    audit.Verb
	}{
		{index: "node-pages-close-failure", closing: false, step: searchdomain.CleanupClose, verb: audit.VerbSearchSessionUpdate},
		{index: "node-pages-delete-failure", closing: true, step: searchdomain.CleanupDelete, verb: audit.VerbSearchSessionDelete},
	}
	for _, current := range cases {
		session := retiringSession(t, stores, current.index)
		if current.closing {
			store := stores.SearchSessions(clock.Wall{}, sessionStoreIdle)
			if err := store.BeginCleanup(integration.WithTestOperation(t, t.Context()), session.ID); err != nil {
				t.Fatalf("close session %s: %v", session.ID, err)
			}
		}
		mark := searchOutboxTail(t, stores)
		_, err := rebuilds.RetireIndexSessions(withFutureOperation(t, t.Context()), current.index, retireSliceLimit)
		requireChangeEventFailure(t, "RetireIndexSessions of "+current.index, err)
		events := cleanupFailureEvents(t, stores, mark, session.ID, current.step)
		if len(events) != 1 {
			t.Fatalf("session %s has %d %s events, want 1", session.ID, len(events), current.step)
		}
		event := events[0]
		if event.Verb != string(current.verb) || event.Outcome != audit.OutcomeFailed || event.Context.OrgID != session.AuthorityID ||
			event.Context.Target == nil || event.Context.Target.OwnerID != session.PrincipalID {
			t.Fatalf("cleanup failure event = %+v, target %+v, want %s in organization %s with owner %s",
				event, event.Context.Target, current.verb, session.AuthorityID, session.PrincipalID)
		}
	}
}
