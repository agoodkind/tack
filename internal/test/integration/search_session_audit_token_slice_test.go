//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	boundaryTokenBytes   = 2*(16<<10) + 1024
	boundaryTokenChunks  = 3
	boundaryVisitedIDs   = 5
	boundaryRetireLimit  = 100
	boundaryFinishSlices = 8
)

type tokenBoundary struct {
	name    string
	replays int
	limit   int
}

// TestSearchSessionAuditSliceKeepsTokenValue runs CleanupSlice with limits
// that end inside, at the end of, and after the query token chunks.
func TestSearchSessionAuditSliceKeepsTokenValue(t *testing.T) {
	for _, boundary := range []tokenBoundary{
		{name: "inside-tokens", replays: 1, limit: 2},
		{name: "at-last-token", replays: 1, limit: 1 + boundaryTokenChunks},
		{name: "after-tokens", replays: 1, limit: 3 + boundaryTokenChunks},
	} {
		t.Run(boundary.name, func(t *testing.T) {
			stores := sessionStoreStores(t)
			store := stores.SearchSessions(clock.Wall{}, sessionStoreIdle)
			session := storedSearchSession(sessionTokens(boundaryTokenBytes))
			mark := closingBoundarySession(t, stores, session, boundary.replays)
			observations := observeSessionTransactions(t)
			ctx := integration.WithTestOperation(t, t.Context())
			if done, err := store.CleanupSlice(ctx, session.ID, boundary.limit); err != nil || done {
				t.Fatalf("CleanupSlice of session %s at limit %d = %t, %v, want a partly deleted session", session.ID, boundary.limit, done, err)
			}
			observations.requireBounded(t, boundary.name, cleanupSliceOperation, session.ID, 1)
			requireTokenRemoval(t, stores, mark, session)
			for range boundaryFinishSlices {
				done, err := store.CleanupSlice(ctx, session.ID, boundary.limit)
				if err != nil {
					t.Fatalf("CleanupSlice of session %s after the token slice: %v", session.ID, err)
				}
				if done {
					requireTokenRemoval(t, stores, mark, session)
					return
				}
			}
			t.Fatalf("session %s remains after %d more slices", session.ID, boundaryFinishSlices)
		})
	}
}

// TestSearchSessionAuditRetireSliceKeepsTokenValue fences a stale delete
// attempt and then retires slices that end inside, at the end of, and after
// the query token chunks.
func TestSearchSessionAuditRetireSliceKeepsTokenValue(t *testing.T) {
	for _, boundary := range []tokenBoundary{
		{name: "inside-tokens", replays: boundaryRetireLimit - boundaryTokenChunks + 1, limit: boundaryRetireLimit},
		{name: "at-last-token", replays: boundaryRetireLimit - boundaryTokenChunks, limit: boundaryRetireLimit},
		{name: "after-tokens", replays: boundaryRetireLimit - boundaryTokenChunks - 1, limit: boundaryRetireLimit},
	} {
		t.Run(boundary.name, func(t *testing.T) {
			stores := sessionStoreStores(t)
			rebuilds := stores.SearchRebuilds(clock.Wall{})
			index := "node-pages-token-" + boundary.name
			beginRetiringRebuild(t, stores, index)
			session := storedSearchSession(sessionTokens(boundaryTokenBytes))
			session.Query.Index, session.Query.Generation = index, retiredGeneration
			mark := closingBoundarySession(t, stores, session, boundary.replays)
			observations := observeSessionTransactions(t)
			requireStaleDeleteFenced(t, stores, index, session)
			observations.requireBounded(t, boundary.name+" fenced", cleanupSliceOperation, session.ID, 0)
			remaining, err := rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, retireSliceLimit)
			if err != nil || !remaining {
				t.Fatalf("RetireIndexSessions of %s = %t, %v, want a partly deleted session", index, remaining, err)
			}
			observations.requireBounded(t, boundary.name, cleanupSliceOperation, session.ID, 1)
			requireTokenRemoval(t, stores, mark, session)
			for range boundaryFinishSlices {
				remaining, err = rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, retireSliceLimit)
				if err != nil {
					t.Fatalf("RetireIndexSessions of %s after the token slice: %v", index, err)
				}
				if !remaining {
					requireTokenRemoval(t, stores, mark, session)
					return
				}
			}
			t.Fatalf("index %s reports remaining work after %d more calls", index, boundaryFinishSlices)
		})
	}
}

// TestSearchSessionAuditSliceRefusesTokenMismatch stores a header with a
// token chunk count or byte count that differs from the stored chunks and
// requires CleanupSlice to refuse the slice.
func TestSearchSessionAuditSliceRefusesTokenMismatch(t *testing.T) {
	for _, member := range []string{"token_chunks", "token_bytes"} {
		t.Run(member, func(t *testing.T) {
			stores := sessionStoreStores(t)
			store := stores.SearchSessions(clock.Wall{}, sessionStoreIdle)
			session := storedSearchSession(sessionTokens(boundaryTokenBytes))
			mark := closingBoundarySession(t, stores, session, 1)
			headers := familyRecords(t, sessionHeaderFamily, session.ID)
			if len(headers) != 1 {
				t.Fatalf("session %s has %d headers, want 1", session.ID, len(headers))
			}
			header := deltaMembers(t, headers[0].Value)
			var stored int
			if err := json.Unmarshal(header[member], &stored); err != nil {
				t.Fatalf("decode header member %s = %s: %v", member, header[member], err)
			}
			header[member] = encodeJSON(t, stored+1)
			integration.ReplaceRawValue(t, headers[0].Key, encodeJSON(t, header))
			observations := observeSessionTransactions(t)
			done, err := store.CleanupSlice(integration.WithTestOperation(t, t.Context()), session.ID, boundaryRetireLimit)
			if err == nil || done || !strings.Contains(err.Error(), "token chunks") {
				t.Fatalf("CleanupSlice of session %s with a changed %s = %t, %v, want a token chunk error", session.ID, member, done, err)
			}
			observations.requireBounded(t, member, cleanupSliceOperation, session.ID, 0)
			if chunks := familyRecords(t, sessionChildFamily, session.ID, sessionTokenPart); len(chunks) != boundaryTokenChunks {
				t.Fatalf("session %s keeps %d token chunks after the refused slice, want %d", session.ID, len(chunks), boundaryTokenChunks)
			}
			if _, removed := sessionTokenValues(t, stores, mark, session.ID); len(removed) != 0 {
				t.Fatalf("session %s recorded %d token removals after the refused slice, want 0", session.ID, len(removed))
			}
		})
	}
}

func closingBoundarySession(t *testing.T, stores *fdbadapter.Stores, session searchdomain.Session, replays int) []byte {
	t.Helper()
	store := stores.SearchSessions(clock.Wall{}, sessionStoreIdle)
	ctx := integration.WithTestOperation(t, t.Context())
	mark := searchOutboxTail(t, stores)
	if _, err := store.Create(ctx, session); err != nil {
		t.Fatalf("create session %s: %v", session.ID, err)
	}
	commit := searchdomain.PageCommit{Sort: json.RawMessage(`[1]`), PITID: "pit-next", Visited: newIDs(boundaryVisitedIDs), ResultIDs: newIDs(1), Complete: false}
	if _, err := store.CommitPage(ctx, session.ID, 0, commit); err != nil {
		t.Fatalf("commit a page of session %s: %v", session.ID, err)
	}
	if replays > 1 {
		addReplayRecords(t, session.ID, replays-1)
	}
	if err := store.BeginCleanup(ctx, session.ID); err != nil {
		t.Fatalf("close session %s: %v", session.ID, err)
	}
	if chunks := familyRecords(t, sessionChildFamily, session.ID, sessionTokenPart); len(chunks) != boundaryTokenChunks {
		t.Fatalf("session %s stores %d token chunks, want %d", session.ID, len(chunks), boundaryTokenChunks)
	}
	return mark
}

func requireTokenRemoval(t *testing.T, stores *fdbadapter.Stores, mark []byte, session searchdomain.Session) {
	t.Helper()
	created, removed := sessionTokenValues(t, stores, mark, session.ID)
	if len(created) != 1 || !bytes.Equal(created[0], session.Snapshot.QueryTokens) {
		t.Fatalf("session %s recorded %d created token values, want 1 equal to the %d bytes Create received", session.ID, len(created), len(session.Snapshot.QueryTokens))
	}
	if len(removed) != 1 || !bytes.Equal(removed[0], created[0]) {
		t.Fatalf("session %s recorded %d removed token values, want 1 equal to the %d bytes Create stored", session.ID, len(removed), len(created[0]))
	}
}

func requireStaleDeleteFenced(t *testing.T, stores *fdbadapter.Stores, index string, session searchdomain.Session) {
	t.Helper()
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	mark := searchOutboxTail(t, stores)
	staleContext := integration.WithTestOperation(t, t.Context())
	operation, err := audit.RequireMutationContext(staleContext)
	if err != nil {
		t.Fatalf("read the stale attempt operation: %v", err)
	}
	hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
		retireAttemptSavedMessage: func() {
			waitUntilTime(t, clock.Now().Add(retireAttemptWait))
			_, recoverErr := rebuilds.RetireIndexSessions(withFutureOperation(t, t.Context()), index, retireSliceLimit)
			requireChangeEventFailure(t, "RetireIndexSessions recovery of "+index, recoverErr)
		},
	})
	remaining, err := rebuilds.RetireIndexSessions(staleContext, index, retireSliceLimit)
	hooks.requireFinished(t)
	if err == nil || !remaining || !strings.Contains(err.Error(), retireAttemptFenced) {
		t.Fatalf("RetireIndexSessions of %s with a stale delete attempt = %t, %v, want remaining work and the fence text %q", index, remaining, err, retireAttemptFenced)
	}
	for _, verb := range []audit.Verb{audit.VerbSearchSessionUpdate, audit.VerbSearchSessionDelete} {
		if count := sessionChangeEvents(t, stores, mark, session, operation.Context.OperationID, verb); count != 0 {
			t.Fatalf("the fenced operation %s wrote %d %s change events for session %s, want 0", operation.Context.OperationID, count, verb, session.ID)
		}
	}
}
