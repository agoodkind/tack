//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	// sessionStoreTimeout bounds each FoundationDB transaction of these tests.
	sessionStoreTimeout = 5 * time.Second
	sessionStoreIdle    = time.Minute
	// maximumSessionTokens, maximumVisitedIDs, maximumResultIDs, and
	// maximumCleanupKeys are the source bounds of one session transaction.
	maximumSessionTokens = 256 << 10
	maximumVisitedIDs    = 400
	maximumResultIDs     = 25
	maximumCleanupKeys   = 100
	// sessionStoreTokenBytes is the byte count of one element of the token
	// array that sessionTokens builds.
	sessionStoreTokenBytes = 2
	// minimumCreateRecords is the smallest outbox record count of an event
	// above one event part: one part and the manifest.
	minimumCreateRecords = 2
	// minimumSliceRecords is the smallest outbox record count of the first
	// cleanup slice at maximumCleanupKeys: its visited, replay, and session
	// events, without the sixteen token chunks that form one value.
	minimumSliceRecords = 80
)

func sessionStoreStores(t *testing.T) *fdbadapter.Stores {
	t.Helper()
	clusterFile := testenv.FoundationDB(t)
	identity := uuid.New()
	prefix := append([]byte("tack-test:"), identity[:]...)
	fdbadapter.SetTestPrefix(prefix)
	t.Cleanup(func() { clearReadPrefix(t, clusterFile, prefix) })
	stores, err := fdbadapter.NewStores(clusterFile, sessionStoreTimeout, nil)
	if err != nil {
		t.Fatalf("open FoundationDB stores: %v", err)
	}
	return stores
}

func sessionTokens(size int) json.RawMessage {
	count := (size - 1) / sessionStoreTokenBytes
	return json.RawMessage("[" + strings.Repeat("1,", count-1) + "1]")
}

func storedSearchSession(tokens json.RawMessage) searchdomain.Session {
	now := clock.Now().UTC()
	authorityID, principalID, entryPointID := uuid.Must(uuid.NewV7()), uuid.Must(
		uuid.NewV7(),
	), uuid.Must(uuid.NewV7())
	query := searchdomain.Query{
		Text:     "session audit store",
		Index:    "node-pages-store",
		NodeType: "",
		Access: searchdomain.AccessFilter{
			Version: searchaccess.StableVersion,
			Keys:    []string{"key-" + uuid.NewString()},
		},
		Generation: 0,
	}
	return searchdomain.Session{
		ID:           uuid.Must(uuid.NewV7()),
		PrincipalID:  principalID,
		AuthorityID:  authorityID,
		EntryPointID: entryPointID,
		Binding: searchdomain.SessionBinding(
			principalID,
			authorityID,
			entryPointID,
			query,
		),
		Query: query,
		Snapshot: searchdomain.Snapshot{
			PITID:       "pit-store",
			Index:       query.Index,
			QueryTokens: tokens,
		},
		Sort:             nil,
		Version:          0,
		CreatedAt:        now,
		IdleDeadline:     now.Add(sessionStoreIdle),
		AbsoluteDeadline: now.Add(time.Hour),
		Complete:         false,
		Closing:          false,
	}
}

func newIDs(count int) []uuid.UUID {
	ids := make([]uuid.UUID, 0, count)
	for range count {
		ids = append(ids, uuid.Must(uuid.NewV7()))
	}
	return ids
}

// TestSearchSessionAuditRefusesMissingContext calls each session writer
// without an audit operation. Each writer must return
// audit.ErrMissingMutationContext and leave the session and the outbox
// unchanged.
func TestSearchSessionAuditRefusesMissingContext(t *testing.T) {
	stores := sessionStoreStores(t)
	store := stores.SearchSessions(clock.Wall{}, sessionStoreIdle)
	session := storedSearchSession(sessionTokens(sessionStoreTokenBytes * 16))
	if _, err := store.Create(integration.WithTestOperation(t, t.Context()), session); err != nil {
		t.Fatalf("create session %s: %v", session.ID, err)
	}
	before, err := store.Load(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("load session %s: %v", session.ID, err)
	}
	mark := searchOutboxTail(t, stores)
	commit := searchdomain.PageCommit{
		Sort:      json.RawMessage(`[1]`),
		PITID:     "pit-next",
		Visited:   newIDs(1),
		ResultIDs: newIDs(1),
		Complete:  false,
	}
	_, createErr := store.Create(
		t.Context(),
		storedSearchSession(sessionTokens(sessionStoreTokenBytes*16)),
	)
	_, commitErr := store.CommitPage(t.Context(), session.ID, 0, commit)
	cleanupErr := store.BeginCleanup(t.Context(), session.ID)
	_, sliceErr := store.CleanupSlice(t.Context(), session.ID, maximumCleanupKeys)
	for name, writerErr := range map[string]error{
		"Create":       createErr,
		"CommitPage":   commitErr,
		"BeginCleanup": cleanupErr,
		"CleanupSlice": sliceErr,
	} {
		if !errors.Is(writerErr, audit.ErrMissingMutationContext) {
			t.Fatalf(
				"%s without an operation = %v, want audit.ErrMissingMutationContext",
				name,
				writerErr,
			)
		}
	}
	after, err := store.Load(t.Context(), session.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("session after the refused writes = %+v, %v, want %+v", after, err, before)
	}
	if entries, err := stores.OpsOutbox.ReadOutboxFrom(t.Context(), mark, 1); err != nil ||
		len(entries) != 0 {
		t.Fatalf("the refused writes added outbox entries %d, %v", len(entries), err)
	}
}

func outboxRecordsAfter(t *testing.T, stores *fdbadapter.Stores, mark []byte) (int, []byte) {
	t.Helper()
	count := 0
	for {
		entries, err := stores.OpsOutbox.ReadOutboxFrom(t.Context(), mark, searchAuditPageLimit)
		if err != nil {
			t.Fatalf("read the outbox: %v", err)
		}
		if len(entries) == 0 {
			return count, mark
		}
		count += len(entries)
		mark = entries[len(entries)-1].Mark
	}
}
