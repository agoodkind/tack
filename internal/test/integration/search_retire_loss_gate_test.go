//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"context"
	"log"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
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
// failure log record with the session ID. Cleanup resumes container if the test
// ends while it remains paused.
func installRetirePauseGate(t *testing.T, container, sessionID, pauseMessage string) *retireLogHooks {
	t.Helper()
	resumed := false
	paused := false
	t.Cleanup(func() {
		if paused && !resumed {
			setContainerPaused(t, container, false)
		}
	})
	return installRetireLogHooks(t, sessionID, map[string]func(){
		pauseMessage: func() {
			setContainerPaused(t, container, true)
			paused = true
		},
		retireAuditFailedMessage: func() {
			setContainerPaused(t, container, false)
			resumed = true
		},
	})
}

func (h *retireLogHooks) observe(record slog.Record) {
	matched := false
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

func retireAttemptRawKey(t *testing.T) fdb.Key {
	t.Helper()
	prefix := append(bytes.Clone(fdbadapter.TestPrefixRange()), tuple.Tuple{retireAttemptFamily}.Pack()...)
	keyRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		t.Fatalf("create the retirement attempt range: %v", err)
	}
	transaction := pendingFixtureTransaction(t)
	defer transaction.Cancel()
	items, err := transaction.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		t.Fatalf("read the retirement attempts: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("found %d retirement attempt records, want 1", len(items))
	}
	return items[0].Key
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
