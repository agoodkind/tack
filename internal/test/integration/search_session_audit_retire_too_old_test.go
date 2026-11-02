//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	// writeLifeKnob sets the maximum age in versions of a read snapshot
	// for a transaction with read conflict ranges. The FoundationDB resolver
	// subtracts this value from the commit version. It rejects an older
	// read snapshot as too old.
	writeLifeKnob     = "max_write_transaction_life_versions"
	writeLifeVersions = "1000000"
	// writeLifeWait exceeds the one second of versions allowed by
	// writeLifeVersions. It stays below the five second transaction timeout.
	writeLifeWait         = 2 * time.Second
	transactionTooOldCode = "1007"
	transactionLogGroup   = "transaction"
	retriedCodesAttribute = "retried_error_codes"
)

// TestSearchSessionAuditRetireTooOldCommitIsDefinite starts a fresh
// FoundationDB cluster with a write transaction lifetime of one million
// versions. After the close transaction prepares its writes, a log hook
// waits past that lifetime and replaces the session header. The commit
// fails with FoundationDB error 1007, and the transaction retries. The
// retry finds an invalid header. The test requires code 1007 in the
// logged retried error codes and one failed close event. It requires
// the audit log to contain zero uncertain events.
func TestSearchSessionAuditRetireTooOldCommitIsDefinite(t *testing.T) {
	cluster := testenv.FreshFoundationDBWithKnobs(t, map[string]string{writeLifeKnob: writeLifeVersions})
	identity := uuid.New()
	fdbadapter.SetTestPrefix(append([]byte("tack-test:"), identity[:]...))
	t.Cleanup(func() { fdbadapter.SetTestPrefix(nil) })
	stores, err := fdbadapter.NewStores(cluster, sessionStoreTimeout, nil)
	if err != nil {
		t.Fatalf("open FoundationDB stores: %v", err)
	}
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	index := "node-pages-too-old"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	mark := searchOutboxTail(t, stores)
	hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
		retireCommitReady: func() {
			waitUntilTime(t, clock.Now().Add(writeLifeWait))
			replaceSessionHeader(t, cluster)
		},
	})
	capture := &retriedCodeCapture{Handler: slog.Default().Handler(), codes: nil}
	slog.SetDefault(slog.New(capture))

	if _, err := rebuilds.RetireIndexSessions(integration.WithTestOperation(t, t.Context()), index, retireSliceLimit); err == nil {
		t.Fatalf("RetireIndexSessions of %s after a too-old commit and an invalid header returned no error", index)
	}
	hooks.requireFinished(t)
	if !strings.Contains(strings.Join(capture.codes, ","), transactionTooOldCode) {
		t.Fatalf("the session transaction logged retried error codes %v, want %s", capture.codes, transactionTooOldCode)
	}
	failed := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupClose)
	if len(failed) != 1 || failed[0].Outcome != audit.OutcomeFailed {
		t.Fatalf("session %s failed close events = %+v, want 1 failed event", session.ID, failed)
	}
	if uncertain := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupStep(retireResultUncertain)); len(uncertain) != 0 {
		t.Fatalf("session %s has %d uncertain events after a too-old commit, want 0", session.ID, len(uncertain))
	}
}

// replaceSessionHeader stores invalid bytes in the only session header
// of the fresh cluster at cluster.
func replaceSessionHeader(t *testing.T, cluster string) {
	t.Helper()
	database, err := fdbadapter.Open(cluster, sessionStoreTimeout)
	if err != nil {
		t.Fatalf("open the fresh FoundationDB cluster: %v", err)
	}
	prefix := append(bytes.Clone(fdbadapter.TestPrefixRange()), tuple.Tuple{sessionHeaderFamily}.Pack()...)
	keyRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		t.Fatalf("create the session header range: %v", err)
	}
	if _, err := database.Transact(func(tr fdb.Transaction) (any, error) {
		items, readErr := tr.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
		if readErr != nil {
			return nil, readErr
		}
		if len(items) != 1 {
			t.Fatalf("found %d session headers, want 1", len(items))
		}
		tr.Set(items[0].Key, []byte(invalidHeaderValue))
		return nil, nil
	}); err != nil {
		t.Fatalf("replace the session header: %v", err)
	}
}

// retriedCodeCapture stores the retried error codes from each session
// transaction failure log. It then passes the log record to Handler.
type retriedCodeCapture struct {
	slog.Handler
	codes []string
}

func (c *retriedCodeCapture) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == retireStorageFailed {
		record.Attrs(func(attr slog.Attr) bool {
			if attr.Key != transactionLogGroup {
				return true
			}
			for _, detail := range attr.Value.Group() {
				if detail.Key == retriedCodesAttribute {
					c.codes = append(c.codes, detail.Value.String())
				}
			}
			return true
		})
	}
	return c.Handler.Handle(ctx, record)
}
