//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	boundaryTokenBytes   = 2*sessionTokenChunkBound + 1024
	boundaryTokenChunks  = 3
	boundaryVisitedIDs   = 5
	boundaryRetireLimit  = 100
	boundaryFinishSlices = 8
)

type tokenBoundary struct {
	name   string
	offset int
}

var tokenBoundaries = []tokenBoundary{{name: "inside-tokens", offset: -1}, {name: "at-last-token", offset: 0}, {name: "after-tokens", offset: 1}}

// TestSearchSessionAuditSliceKeepsTokenValue runs CleanupSlice with limits that
// end inside, at the end of, and after the query token chunks.
func TestSearchSessionAuditSliceKeepsTokenValue(t *testing.T) {
	for _, boundary := range tokenBoundaries {
		t.Run(boundary.name, func(t *testing.T) {
			stores := sessionStoreStores(t)
			store := stores.SearchSessions(clock.Wall{}, sessionStoreIdle)
			session := storedSearchSession(sessionTokens(boundaryTokenBytes))
			replays := 1
			limit := replays + boundaryTokenChunks + boundary.offset
			mark := closingBoundarySession(t, stores, session, replays)
			observations := observeSessionTransactions(t)
			ctx := integration.WithTestOperation(t, t.Context())
			if done, err := store.CleanupSlice(ctx, session.ID, limit); err != nil || done {
				t.Fatalf("CleanupSlice of session %s at limit %d = %t, %v, want a partly deleted session", session.ID, limit, done, err)
			}
			observations.requireBounded(t, boundary.name, cleanupSliceOperation, session.ID, 1)
			requireTokenRemoval(t, stores, mark, session)
			finishSlices(t, "session "+session.ID.String(), boundaryFinishSlices, func() (bool, error) {
				return store.CleanupSlice(ctx, session.ID, limit)
			})
			requireTokenRemoval(t, stores, mark, session)
		})
	}
}

// TestSearchSessionAuditRetireSliceKeepsTokenValue checks that the retirement
// fence refuses a stale delete attempt, then retires slices that end inside, at
// the end of, and after the query token chunks.
func TestSearchSessionAuditRetireSliceKeepsTokenValue(t *testing.T) {
	for _, boundary := range tokenBoundaries {
		t.Run(boundary.name, func(t *testing.T) {
			stores := sessionStoreStores(t)
			rebuilds := stores.SearchRebuilds(clock.Wall{})
			index := "node-pages-token-" + boundary.name
			beginRetiringRebuild(t, stores, index)
			session := storedSearchSession(sessionTokens(boundaryTokenBytes))
			session.Query.Index, session.Query.Generation = index, retiredGeneration
			mark := closingBoundarySession(t, stores, session, boundaryRetireLimit-boundaryTokenChunks-boundary.offset)
			observations := observeSessionTransactions(t)
			requireStaleDeleteFenced(t, stores, index, session)
			observations.requireBounded(t, boundary.name+" fenced", cleanupSliceOperation, session.ID, 0)
			remaining, err := rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, retireSliceLimit)
			if err != nil || !remaining {
				t.Fatalf("RetireIndexSessions of %s = %t, %v, want a partly deleted session", index, remaining, err)
			}
			observations.requireBounded(t, boundary.name, cleanupSliceOperation, session.ID, 1)
			requireTokenRemoval(t, stores, mark, session)
			finishSlices(t, "index "+index, boundaryFinishSlices, func() (bool, error) {
				remaining, err := rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, retireSliceLimit)
				return !remaining, err
			})
			requireTokenRemoval(t, stores, mark, session)
		})
	}
}

func finishSlices(t *testing.T, label string, maxSlices int, slice func() (bool, error)) {
	t.Helper()
	for range maxSlices {
		done, err := slice()
		if err != nil {
			t.Fatalf("%s: cleanup slice: %v", label, err)
		}
		if done {
			return
		}
	}
	t.Fatalf("%s: cleanup remains after %d more slices", label, maxSlices)
}

// TestSearchSessionAuditSliceRefusesTokenMismatch stores a header with a token
// chunk count or byte count that differs from the stored chunks and requires
// CleanupSlice to refuse the slice.
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
