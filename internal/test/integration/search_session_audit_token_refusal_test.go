//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/test/integration"
)

const (
	refusalTokenText    = "token chunks"
	refusalChunkCeiling = 17
	refusalReplayRows   = 1
)

type tokenRefusal struct {
	name     string
	retire   bool
	chunks   int
	mutation func(t *testing.T, sessionID uuid.UUID)
}

// TestSearchSessionAuditSliceRefusesTokenMismatch stores a header with a token
// chunk count or byte count that differs from the stored chunks and requires
// CleanupSlice to refuse the slice.
func TestSearchSessionAuditSliceRefusesTokenMismatch(t *testing.T) {
	for _, refusal := range []tokenRefusal{
		{
			name:     "token_chunks",
			retire:   false,
			chunks:   boundaryTokenChunks,
			mutation: headerMember("token_chunks", func(stored int) int { return stored + 1 }),
		},
		{
			name:     "token_bytes",
			retire:   false,
			chunks:   boundaryTokenChunks,
			mutation: headerMember("token_bytes", func(stored int) int { return stored + 1 }),
		},
		{
			name:     "chunk_gap",
			retire:   false,
			chunks:   boundaryTokenChunks - 1,
			mutation: deleteMiddleChunk,
		},
		{
			name:     "token_chunks_above_maximum",
			retire:   false,
			chunks:   boundaryTokenChunks,
			mutation: headerMember("token_chunks", func(int) int { return refusalChunkCeiling }),
		},
		{
			name:     "token_chunks_below_stored",
			retire:   false,
			chunks:   boundaryTokenChunks,
			mutation: headerMember("token_chunks", func(stored int) int { return stored - 1 }),
		},
		{
			name:     "retirement_token_chunks",
			retire:   true,
			chunks:   boundaryTokenChunks,
			mutation: headerMember("token_chunks", func(stored int) int { return stored + 1 }),
		},
	} {
		t.Run(
			refusal.name,
			func(t *testing.T) {
				stores := sessionStoreStores(t)
				session := storedSearchSession(sessionTokens(boundaryTokenBytes))
				index := "node-pages-refusal-" + strings.ReplaceAll(refusal.name, "_", "-")
				if refusal.retire {
					beginRetiringRebuild(t, stores, index)
					session.Query.Index, session.Query.Generation = index, retiredGeneration
				}
				mark := closingBoundarySession(t, stores, session, refusalReplayRows)
				refusal.mutation(t, session.ID)
				observations := observeSessionTransactions(t)
				err := refusedSlice(t, stores, session.ID, index, refusal.retire)
				if err == nil || !strings.Contains(err.Error(), refusalTokenText) {
					t.Fatalf(
						"the slice of session %s after %s = %v, want an error with the text %q",
						session.ID,
						refusal.name,
						err,
						refusalTokenText,
					)
				}
				observations.requireBounded(t, refusal.name, cleanupSliceOperation, session.ID, 0)
				requireChildrenKept(t, stores, mark, session.ID, refusal.chunks)
			},
		)
	}
}

func refusedSlice(
	t *testing.T,
	stores *fdbadapter.Stores,
	sessionID uuid.UUID,
	index string,
	retire bool,
) error {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	if retire {
		remaining, err := stores.SearchRebuilds(clock.Wall{}).RetireIndexSessions(
			ctx,
			index,
			retireSliceLimit,
		)
		if !remaining {
			t.Fatalf(
				"RetireIndexSessions of %s with a refused slice reports no remaining work",
				index,
			)
		}
		return err
	}
	done, err := stores.SearchSessions(
		clock.Wall{},
		sessionStoreIdle,
	).CleanupSlice(
		ctx,
		sessionID,
		maximumCleanupKeys,
	)
	if done {
		t.Fatalf(
			"CleanupSlice of session %s with a refused slice reports the session gone",
			sessionID,
		)
	}
	return err
}

func headerMember(
	member string,
	change func(stored int) int,
) func(t *testing.T, sessionID uuid.UUID) {
	return func(t *testing.T, sessionID uuid.UUID) {
		t.Helper()
		headers := familyRecords(t, sessionHeaderFamily, sessionID)
		if len(headers) != 1 {
			t.Fatalf("session %s has %d headers, want 1", sessionID, len(headers))
		}
		header := deltaMembers(t, headers[0].Value)
		var stored int
		if err := json.Unmarshal(header[member], &stored); err != nil {
			t.Fatalf("decode header member %s = %s: %v", member, header[member], err)
		}
		header[member] = encodeJSON(t, change(stored))
		integration.ReplaceRawValue(t, headers[0].Key, encodeJSON(t, header))
	}
}

func deleteMiddleChunk(t *testing.T, sessionID uuid.UUID) {
	t.Helper()
	chunks := familyRecords(t, sessionChildFamily, sessionID, sessionTokenPart)
	if len(chunks) != boundaryTokenChunks {
		t.Fatalf(
			"session %s stores %d token chunks, want %d",
			sessionID,
			len(chunks),
			boundaryTokenChunks,
		)
	}
	middle := chunks[boundaryTokenChunks/2].Key
	if _, err := pendingFixtureDatabase(t).Transact(func(tr fdb.Transaction) (any, error) {
		tr.Clear(middle)
		return nil, nil
	}); err != nil {
		t.Fatalf("delete the middle token chunk of session %s: %v", sessionID, err)
	}
}

func requireChildrenKept(
	t *testing.T,
	stores *fdbadapter.Stores,
	mark []byte,
	sessionID uuid.UUID,
	chunks int,
) {
	t.Helper()
	for part, want := range map[string]int{
		sessionTokenPart:   chunks,
		sessionReplayPart:  refusalReplayRows,
		sessionVisitedPart: boundaryVisitedIDs,
	} {
		if stored := familyRecords(t, sessionChildFamily, sessionID, part); len(stored) != want {
			t.Fatalf(
				"session %s keeps %d %s records after the refused slice, want %d",
				sessionID,
				len(stored),
				part,
				want,
			)
		}
	}
	if _, removed := sessionTokenValues(t, stores, mark, sessionID); len(removed) != 0 {
		t.Fatalf(
			"session %s recorded %d token removals after the refused slice, want 0",
			sessionID,
			len(removed),
		)
	}
	for _, event := range outboxPlainEvents(t, stores, mark) {
		target := event.Context.Target
		if event.Verb != string(audit.VerbSearchSessionDelete) || target == nil ||
			!strings.Contains(target.URI, sessionID.String()) {
			continue
		}
		if target.NodeType == audit.TargetSearchSessionReplay ||
			target.NodeType == audit.TargetSearchSessionVisited {
			t.Fatalf(
				"session %s recorded the %s delete event %s after the refused slice",
				sessionID,
				target.NodeType,
				event.EventID,
			)
		}
	}
}
