//go:build runtimeaudit

package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// TestSearchSessionAuditRecordingFailuresNotRecorded calls
// SearchSessionStore.Create and CommitPage without an audit operation and
// with an operation that fails change event validation. Each call must
// return an error that matches audit.ErrEventNotRecorded and contains the
// cause. The stored session must retain its version, and the new session
// must not exist.
func TestSearchSessionAuditRecordingFailuresNotRecorded(t *testing.T) {
	stores := sessionStoreStores(t)
	store := stores.SearchSessions(clock.Wall{}, sessionStoreIdle)
	index := "node-pages-not-recorded"
	stored := retiringSession(t, stores, index)
	fresh := storedSearchSession(sessionTokens(sessionStoreTokenBytes * 16))
	fresh.Query.Index, fresh.Query.Generation = index, retiredGeneration
	commit := searchdomain.PageCommit{Sort: nil, PITID: stored.Snapshot.PITID, Visited: nil, ResultIDs: nil, Complete: false}
	cases := []struct {
		name  string
		ctx   context.Context
		cause string
	}{
		{name: "without an operation", ctx: t.Context(), cause: audit.ErrMissingMutationContext.Error()},
		{name: "under the failing operation", ctx: withFutureOperation(t, t.Context()), cause: changeEventFailure},
	}
	for _, current := range cases {
		_, createErr := store.Create(current.ctx, fresh)
		requireSessionNotRecorded(t, "Create "+current.name, createErr, current.cause)
		_, commitErr := store.CommitPage(current.ctx, stored.ID, stored.Version, commit)
		requireSessionNotRecorded(t, "CommitPage "+current.name, commitErr, current.cause)
	}
	loaded, err := store.Load(t.Context(), stored.ID)
	if err != nil || loaded.Version != stored.Version {
		t.Fatalf("Session %s loaded as %+v with error %v; want version %d", stored.ID, loaded, err, stored.Version)
	}
	if _, err := store.Load(t.Context(), fresh.ID); !errors.Is(err, searchdomain.ErrSessionNotFound) {
		t.Fatalf("Loading session %s returned error %v; want %v", fresh.ID, err, searchdomain.ErrSessionNotFound)
	}
}

func requireSessionNotRecorded(t *testing.T, step string, err error, cause string) {
	t.Helper()
	if !errors.Is(err, audit.ErrEventNotRecorded) || !strings.Contains(err.Error(), cause) {
		t.Fatalf("%s returned error %v; want a match for %v and cause text %q", step, err, audit.ErrEventNotRecorded, cause)
	}
}
