//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditrelay"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	retireAttemptFamily         = "search_retire_attempt"
	retireCleanupFailedMessage  = "search.session.cleanup_failed"
	retireAuditFailedMessage    = "search.session.audit_failed"
	retireAttemptSavedMessage   = "search.session.retire_attempt_saved"
	retireSessionDeletedMessage = "search.session.deleted"
)

// retireLogHooks runs an action once when a log record first contains a listed
// message and the session ID. installRetireLogHooks replaces the default logger
// with a text handler on standard error. Cleanup restores the default logger.
type retireLogHooks struct {
	sessionID string
	mutex     sync.Mutex
	pending   map[string]func()
}

func installRetireLogHooks(t *testing.T, sessionID string, actions map[string]func()) *retireLogHooks {
	t.Helper()
	hooks := &retireLogHooks{sessionID: sessionID, mutex: sync.Mutex{}, pending: actions}
	previous, writer, flags := slog.Default(), log.Writer(), log.Flags()
	base := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(retireLogHandler{Handler: base, hooks: hooks}))
	t.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(writer)
		log.SetFlags(flags)
	})
	return hooks
}

// installRetirePauseGate pauses container at the first log record with
// pauseMessage and the session ID. It resumes container at the first audit
// failure log record with the session ID.
func installRetirePauseGate(t *testing.T, container, sessionID, pauseMessage string) *retireLogHooks {
	t.Helper()
	return installRetireLogHooks(t, sessionID, map[string]func(){
		pauseMessage:             func() { setContainerPaused(t, container, true) },
		retireAuditFailedMessage: func() { setContainerPaused(t, container, false) },
	})
}

func (h *retireLogHooks) observe(record slog.Record) {
	matched := h.sessionID == ""
	record.Attrs(func(attr slog.Attr) bool {
		matched = matched || (attr.Key == "session_id" && attr.Value.String() == h.sessionID)
		return true
	})
	if !matched {
		return
	}
	h.mutex.Lock()
	action := h.pending[record.Message]
	delete(h.pending, record.Message)
	h.mutex.Unlock()
	if action != nil {
		action()
	}
}

func (h *retireLogHooks) requireFinished(t *testing.T) {
	t.Helper()
	h.mutex.Lock()
	defer h.mutex.Unlock()
	if len(h.pending) != 0 {
		t.Fatalf("the log hooks of session %s did not run for %d messages", h.sessionID, len(h.pending))
	}
}

func outboxPlainEvents(t *testing.T, stores *fdbadapter.Stores, mark []byte) []audit.Event {
	t.Helper()
	entries, err := stores.OpsOutbox.ReadOutboxFrom(t.Context(), mark, searchAuditPageLimit)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	events := make([]audit.Event, 0, len(entries))
	for _, entry := range entries {
		var event audit.Event
		if err := json.Unmarshal(entry.Event, &event); err != nil {
			t.Fatalf("decode outbox entry %s: %v", entry.Event, err)
		}
		events = append(events, event)
	}
	return events
}

func startRetireRelay(t *testing.T, stack runtimeAuditStack) {
	t.Helper()
	opened, err := auditrelay.Open(t.Context(), auditrelay.Config{
		Brokers: []string{testenv.Kafka(t)}, Topic: stack.topic, YugabyteDSN: testenv.Ledger(t),
		FDBClusterFile: testenv.FoundationDB(t), FDBTransactionTimeout: integration.StoreTransactionTimeout,
		PollInterval: 100 * time.Millisecond, BatchSize: 64,
	})
	if err != nil {
		t.Fatalf("open the audit relay: %v", err)
	}
	opened.Relay.Start(t.Context())
	t.Cleanup(opened.Pool.Close)
	t.Cleanup(func() {
		if err := opened.Relay.Close(); err != nil {
			t.Errorf("close the audit relay: %v", err)
		}
	})
}

func retireFamilyItem(t *testing.T, family string) fdb.KeyValue {
	t.Helper()
	prefix := append(bytes.Clone(fdbadapter.TestPrefixRange()), tuple.Tuple{family}.Pack()...)
	keyRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		t.Fatalf("create the range of family %s: %v", family, err)
	}
	transaction := pendingFixtureTransaction(t)
	defer transaction.Cancel()
	items, err := transaction.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		t.Fatalf("read family %s: %v", family, err)
	}
	if len(items) != 1 {
		t.Fatalf("found %d records of family %s, want 1", len(items), family)
	}
	return items[0]
}

func retireFamilyKey(t *testing.T, family string) fdb.Key {
	t.Helper()
	return retireFamilyItem(t, family).Key
}

func retireFamilyValue(t *testing.T, family string) []byte {
	t.Helper()
	return retireFamilyItem(t, family).Value
}

func retireAttemptKey(t *testing.T, index string) fdb.Key {
	t.Helper()
	elements := tuple.Tuple{retireAttemptFamily, index, uuid.NewString(), string(searchdomain.CleanupClose), uuid.NewString()}
	return fdb.Key(append(bytes.Clone(fdbadapter.TestPrefixRange()), elements.Pack()...))
}

type retireLogHandler struct {
	slog.Handler
	hooks *retireLogHooks
}

func (h retireLogHandler) Handle(ctx context.Context, record slog.Record) error {
	h.hooks.observe(record)
	return h.Handler.Handle(ctx, record)
}

func (h retireLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return retireLogHandler{Handler: h.Handler.WithAttrs(attrs), hooks: h.hooks}
}

func (h retireLogHandler) WithGroup(name string) slog.Handler {
	return retireLogHandler{Handler: h.Handler.WithGroup(name), hooks: h.hooks}
}
