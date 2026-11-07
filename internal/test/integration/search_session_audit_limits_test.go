//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	worstCaseQueryBytes  = 4096
	worstCaseAccessKeys  = 1180
	worstCaseSortBytes   = 8000
	worstCaseRetireIndex = "node-pages-worst-case"
)

// TestSearchSessionAuditFitsTransactionLimits runs each session writer at its
// source bounds. Each writer must commit its records and change events inside
// the FoundationDB transaction limits, and the outbox must store the events.
func TestSearchSessionAuditFitsTransactionLimits(t *testing.T) {
	stores := sessionStoreStores(t)
	store := stores.SearchSessions(clock.Wall{}, sessionStoreIdle)
	ctx := integration.WithTestOperation(t, t.Context())
	observations := observeSessionTransactions(t)
	session := worstCaseSession()
	mark := searchOutboxTail(t, stores)
	if _, err := store.Create(ctx, session); err != nil {
		t.Fatalf("Create with %d token bytes: %v", maximumSessionTokens, err)
	}
	observations.requireBounded(t, "public", createSessionOperation, session.ID, 1)
	created, mark := outboxRecordsAfter(t, stores, mark)
	if created < minimumCreateRecords {
		t.Fatalf("Create wrote %d outbox records, want at least %d for a multipart event", created, minimumCreateRecords)
	}
	commitWorstCasePage(t, stores, session.ID, 0)
	commitWorstCasePage(t, stores, session.ID, 1)
	observations.requireBounded(t, "public", commitPageOperation, session.ID, 2)
	committed, mark := outboxRecordsAfter(t, stores, mark)
	if committed < maximumVisitedIDs+minimumCreateRecords {
		t.Fatalf("CommitPage wrote %d outbox records, want at least %d", committed, maximumVisitedIDs+minimumCreateRecords)
	}
	if err := store.BeginCleanup(ctx, session.ID); err != nil {
		t.Fatalf("BeginCleanup: %v", err)
	}
	observations.requireBounded(t, "public", beginCleanupOperation, session.ID, 1)
	if done, err := store.CleanupSlice(ctx, session.ID, maximumCleanupKeys); err != nil || done {
		t.Fatalf("CleanupSlice at its bound = %t, %v, want a partly deleted session", done, err)
	}
	observations.requireBounded(t, "public", cleanupSliceOperation, session.ID, 1)
	if sliced, _ := outboxRecordsAfter(t, stores, mark); sliced < minimumSliceRecords {
		t.Fatalf("BeginCleanup and CleanupSlice wrote %d outbox records, want at least %d", sliced, minimumSliceRecords)
	}
	for {
		done, err := store.CleanupSlice(ctx, session.ID, maximumCleanupKeys)
		if err != nil {
			t.Fatalf("CleanupSlice after the first slice: %v", err)
		}
		if done {
			break
		}
	}
	requireWorstCaseRetirement(t, stores, observations)
}

func requireWorstCaseRetirement(t *testing.T, stores *fdbadapter.Stores, observations *sessionObservations) {
	t.Helper()
	beginRetiringRebuild(t, stores, worstCaseRetireIndex)
	session := worstCaseSession()
	session.Query.Index, session.Query.Generation = worstCaseRetireIndex, retiredGeneration
	session.Binding = searchdomain.SessionBinding(session.PrincipalID, session.AuthorityID, session.EntryPointID, session.Query)
	store := stores.SearchSessions(clock.Wall{}, sessionStoreIdle)
	if _, err := store.Create(integration.WithTestOperation(t, t.Context()), session); err != nil {
		t.Fatalf("Create the retiring session %s: %v", session.ID, err)
	}
	commitWorstCasePage(t, stores, session.ID, 0)
	observations.requireBounded(t, "retirement setup", commitPageOperation, session.ID, 1)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	remaining, err := rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), worstCaseRetireIndex, retireSliceLimit)
	if err != nil || !remaining {
		t.Fatalf("RetireIndexSessions of %s at its bounds = %t, %v, want a partly deleted session", worstCaseRetireIndex, remaining, err)
	}
	observations.requireBounded(t, "retirement", beginCleanupOperation, session.ID, 1)
	observations.requireBounded(t, "retirement", cleanupSliceOperation, session.ID, 1)
	for range boundaryFinishSlices {
		remaining, err = rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), worstCaseRetireIndex, retireSliceLimit)
		if err != nil {
			t.Fatalf("RetireIndexSessions of %s after the first slice: %v", worstCaseRetireIndex, err)
		}
		if !remaining {
			return
		}
	}
	t.Fatalf("index %s reports remaining work after %d more calls", worstCaseRetireIndex, boundaryFinishSlices)
}

func worstCaseSession() searchdomain.Session {
	session := storedSearchSession(sessionTokens(maximumSessionTokens))
	keys := make([]string, 0, worstCaseAccessKeys)
	for range worstCaseAccessKeys {
		keys = append(keys, "key-"+uuid.NewString())
	}
	session.Query.Text, session.Query.Access.Keys = strings.Repeat("q", worstCaseQueryBytes), keys
	session.Binding = searchdomain.SessionBinding(session.PrincipalID, session.AuthorityID, session.EntryPointID, session.Query)
	return session
}

func commitWorstCasePage(t *testing.T, stores *fdbadapter.Stores, sessionID uuid.UUID, version uint64) {
	t.Helper()
	commit := searchdomain.PageCommit{
		Sort: json.RawMessage(`["` + strconv.FormatUint(version, 10) + strings.Repeat("s", worstCaseSortBytes) + `"]`), PITID: "pit-next",
		Visited: newIDs(maximumVisitedIDs), ResultIDs: newIDs(maximumResultIDs), Complete: false,
	}
	store := stores.SearchSessions(clock.Wall{}, sessionStoreIdle)
	if _, err := store.CommitPage(integration.WithTestOperation(t, t.Context()), sessionID, version, commit); err != nil {
		t.Fatalf("CommitPage of session %s version %d at its bounds: %v", sessionID, version, err)
	}
}
