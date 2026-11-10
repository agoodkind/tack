//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/test/integration"
)

const (
	retireClearReady     = "search.session.retire_clear_ready"
	retireClearAbsent    = "search.session.retire_clear_absent"
	retireClearAbsentErr = "the retirement attempt record is absent after a retried clear; this clear or recovery removed it"
	retireClearOperation = "clear search session retirement attempt"
	retireFinishCalls    = 4
	byteLimitRecords     = 13
	byteLimitFirstPass   = 12
	byteLimitSlice       = 32
)

// TestSearchSessionAuditRetireClearRetryFindsRecordAbsent uses a log hook to
// clear the attempt record after the clear transaction reads it. The commit
// fails with FoundationDB conflict error 1020, and the transaction retries.
// The retry finds the record absent. The call must return the error text of
// errRetireClearAbsent without the fence text or audit.ErrEventNotRecorded.
// It must also report remaining work. The close step has one change event.
// Later calls must finish retirement without an error.
func TestSearchSessionAuditRetireClearRetryFindsRecordAbsent(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	index := "node-pages-clear-retry"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	mark := searchOutboxTail(t, stores)
	ownerContext := integration.WithTestOperation(t, t.Context())
	operation, err := audit.RequireMutationContext(ownerContext)
	if err != nil {
		t.Fatalf("read the owner operation: %v", err)
	}
	hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
		retireClearReady:  func() { clearAttemptRecord(t, retireFamilyKey(t, retireAttemptFamily)) },
		retireClearAbsent: func() {},
	})
	observations := observeSessionTransactions(t)

	remaining, err := rebuilds.RetireIndexSessions(ownerContext, index, retireSliceLimit)
	hooks.requireFinished(t)
	if cleared := observations.requireBounded(t, "retried clear", retireClearOperation, session.ID, 1); cleared[0].reads != 1 {
		t.Fatalf("the retried clear of session %s reports %d reads, want the 1 read of its committed attempt", session.ID, cleared[0].reads)
	}
	if !remaining || err == nil || !strings.Contains(err.Error(), retireClearAbsentErr) ||
		strings.Contains(err.Error(), retireAttemptFenced) || errors.Is(err, audit.ErrEventNotRecorded) {
		t.Fatalf("RetireIndexSessions of %s with an absent record on retry = %t, %v, want remaining work and only the text %q", index, remaining, err, retireClearAbsentErr)
	}
	if count := sessionChangeEvents(t, stores, mark, session, operation.Context.OperationID, audit.VerbSearchSessionUpdate); count != 1 {
		t.Fatalf("session %s has %d update change events under operation %s, want 1", session.ID, count, operation.Context.OperationID)
	}
	for range retireFinishCalls {
		remaining, err = rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, retireSliceLimit)
		if err != nil {
			t.Fatalf("RetireIndexSessions of %s after the absent record: %v", index, err)
		}
		if !remaining {
			return
		}
	}
	t.Fatalf("RetireIndexSessions of %s reports remaining work after %d calls", index, retireFinishCalls)
}

// TestSearchSessionAuditRetireRecoveryByteLimit stores 13 invalid records
// of 90,000 bytes each. The test sets a record limit above 13. The first
// pass must recover 12 records and report remaining work. The second pass
// must recover the last record and finish.
func TestSearchSessionAuditRetireRecoveryByteLimit(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	index := "node-pages-byte-limit"
	beginRetiringRebuild(t, stores, index)
	mark := searchOutboxTail(t, stores)
	for range byteLimitRecords {
		integration.ReplaceRawValue(t, retireAttemptKey(t, index), bytes.Repeat([]byte("x"), oversizedRecordBytes))
	}
	remaining, err := rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, byteLimitSlice)
	if err != nil || !remaining {
		t.Fatalf("RetireIndexSessions of %s above its byte limit = %t, %v, want remaining work", index, remaining, err)
	}
	if events := invalidAttemptEvents(t, stores, mark); len(events) != byteLimitFirstPass {
		t.Fatalf("index %s has %d invalid attempt events after one pass, want %d", index, len(events), byteLimitFirstPass)
	}
	remaining, err = rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, byteLimitSlice)
	if err != nil || remaining {
		t.Fatalf("RetireIndexSessions of %s below its byte limit = %t, %v, want a finished retirement", index, remaining, err)
	}
	if events := invalidAttemptEvents(t, stores, mark); len(events) != byteLimitRecords {
		t.Fatalf("index %s has %d invalid attempt events after two passes, want %d", index, len(events), byteLimitRecords)
	}
}

func attemptRecordValues(t *testing.T, want int) [][]byte {
	t.Helper()
	prefix := append(bytes.Clone(fdbadapter.TestPrefixRange()), tuple.Tuple{retireAttemptFamily}.Pack()...)
	keyRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		t.Fatalf("create the attempt record range: %v", err)
	}
	transaction := pendingFixtureTransaction(t)
	defer transaction.Cancel()
	items, err := transaction.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		t.Fatalf("read the attempt records: %v", err)
	}
	values := make([][]byte, 0, len(items))
	for _, item := range items {
		values = append(values, item.Value)
	}
	if len(values) != want {
		t.Fatalf("found %d attempt records, want %d", len(values), want)
	}
	return values
}

func clearAttemptRecord(t *testing.T, key fdb.Key) {
	t.Helper()
	if _, err := pendingFixtureDatabase(t).Transact(func(tr fdb.Transaction) (any, error) {
		tr.Clear(key)
		return nil, nil
	}); err != nil {
		t.Fatalf("clear the attempt record: %v", err)
	}
}
