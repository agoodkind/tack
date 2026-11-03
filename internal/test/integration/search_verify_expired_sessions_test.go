package integration_test

import (
	"errors"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	expiredBacklogQuery    = "umber weir expired backlog"
	expiredBacklogNodes    = 30
	expiredBacklogSessions = 3
	expiredBacklogLeft     = 1
	expiredBacklogIdle     = 3 * time.Second
	expiredBacklogAbsolute = 6 * time.Second
	expiredBacklogBound    = 10000
	expiredBacklogBatch    = 2000
	expiredBacklogAge      = 10
)

// TestSearchVerifyReportsExpiredSessionBacklog expires three sessions and
// checks that ops search verify reports three. It runs one search and checks
// that one session remains and verify reports one. It adds expiry keys past
// the bound and checks that verify reports the bound and truncation.
func TestSearchVerifyReportsExpiredSessionBacklog(t *testing.T) {
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", expiredBacklogIdle.String())
	t.Setenv("OPENSEARCH_SESSION_ABSOLUTE_TIMEOUT", expiredBacklogAbsolute.String())
	fixture := integration.NewQueryFixture(t)
	putIdleCorpus(t, fixture, expiredBacklogQuery, expiredBacklogNodes)

	expired := make([]searchdomain.Session, 0, expiredBacklogSessions)
	for range expiredBacklogSessions {
		page := integration.CallSearch(t, fixture.Harness, expiredBacklogQuery, "")
		expired = append(expired, integration.CursorSession(t, fixture, page.Cursor))
	}
	latest := expired[len(expired)-1].IdleDeadline
	waitForTime(t, latest.Truncate(time.Minute).Add(time.Minute+sweepExpiryMargin))
	requireExpiredBacklog(t, fixture, expiredBacklogSessions, false)

	integration.CallSearch(t, fixture.Harness, expiredBacklogQuery, "")
	store := fixture.Stores.SearchSessions(clock.Wall{}, expiredBacklogIdle)
	remaining := 0
	for _, session := range expired {
		_, err := store.Load(integration.WithTestOperation(t, t.Context()), session.ID)
		switch {
		case err == nil:
			remaining++
		case !errors.Is(err, searchdomain.ErrSessionNotFound):
			t.Fatalf("load session %s after one sweep: %v", session.ID, err)
		}
	}
	if remaining != expiredBacklogLeft {
		t.Fatalf("%d of %d expired sessions remain after one sweep, want %d", remaining, expiredBacklogSessions, expiredBacklogLeft)
	}
	requireExpiredBacklog(t, fixture, expiredBacklogLeft, false)

	writeExpiryKeys(t, expiredBacklogBound)
	requireExpiredBacklog(t, fixture, expiredBacklogBound, true)
}

func requireExpiredBacklog(t *testing.T, fixture integration.QueryFixture, count int, truncated bool) {
	t.Helper()
	report, err := integration.RunSearchVerifyCommand(t, fixture.Config)
	if err != nil {
		t.Fatalf("ops search verify failed: %v", err)
	}
	if report.ExpiredSessions != count || report.ExpiredSessionsTruncated != truncated {
		t.Fatalf("ops search verify reported %d expired sessions and truncated %t, want %d and %t",
			report.ExpiredSessions, report.ExpiredSessionsTruncated, count, truncated)
	}
}

// writeExpiryKeys stores count expiry keys under the test prefix in an earlier
// minute, without session headers.
func writeExpiryKeys(t *testing.T, count int) {
	t.Helper()
	minute := clock.Now().Unix()/secondsPerMinute - expiredBacklogAge
	for written := 0; written < count; written += expiredBacklogBatch {
		writeExpiryKeyBatch(t, minute, min(expiredBacklogBatch, count-written))
	}
}

func writeExpiryKeyBatch(t *testing.T, minute int64, count int) {
	t.Helper()
	prefix := activeTestPrefix(t)
	transaction := pendingFixtureTransaction(t)
	defer transaction.Cancel()
	for range count {
		transaction.Set(prefixedKey(prefix, tuple.Tuple{sessionExpiryFamily, minute, 0, uuid.NewString()}), nil)
	}
	if err := transaction.Commit().Get(); err != nil {
		t.Fatalf("write %d expiry keys: %v", count, err)
	}
}
