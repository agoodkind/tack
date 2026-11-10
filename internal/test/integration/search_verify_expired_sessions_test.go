package integration_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	expiredBacklogQuery     = "umber weir expired backlog"
	expiredBacklogNodes     = 30
	expiredBacklogSessions  = 3
	expiredBacklogLeft      = 1
	expiredBacklogIdle      = 3 * time.Second
	expiredBacklogBound     = 10000
	expiredBacklogBatch     = 2000
	expiredBacklogAge       = 10
	expiredBacklogMalformed = "not-a-session-id"
	expiredBacklogCountRead = "count expired search sessions"
	expiredBacklogReference = "opensearch/verification"
)

// TestSearchVerifyReportsExpiredSessionBacklog checks the expired session count.
// It expires three sessions and checks that ops search verify reports three.
// After one search, it checks that one session remains and verify reports one.
func TestSearchVerifyReportsExpiredSessionBacklog(t *testing.T) {
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", expiredBacklogIdle.String())
	t.Setenv("OPENSEARCH_QUERY_DEADLINE", absentSweepDeadline)
	t.Setenv("OPENSEARCH_REQUEST_TIMEOUT", absentSweepRequestTimeout)
	fixture := integration.NewQueryFixture(t)
	putIdleCorpus(t, fixture, expiredBacklogQuery, expiredBacklogNodes)

	// Wait for a new minute. Each search sweeps sessions from earlier minutes,
	// and the third search must not sweep the first two sessions.
	waitForTime(t, clock.Now().Truncate(time.Minute).Add(time.Minute+sweepExpiryMargin))

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
		t.Fatalf(
			"%d of %d expired sessions remain after one sweep, want %d",
			remaining,
			expiredBacklogSessions,
			expiredBacklogLeft,
		)
	}

	requireExpiredBacklog(t, fixture, expiredBacklogLeft, false)
}

// TestSearchVerifyBoundsExpiredSessionCount checks the count and truncation
// boundary using expiry keys without session headers. It checks that 10,000
// keys report the bound without truncation and 10,001 report it with truncation.
// It checks that a malformed key in an earlier minute fails the command,
// produces no output and records a failed read.
func TestSearchVerifyBoundsExpiredSessionCount(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	minute := clock.Now().Unix()/secondsPerMinute - expiredBacklogAge
	writeExpiryKeys(t, minute, expiredBacklogBound)
	requireExpiredBacklog(t, fixture, expiredBacklogBound, false)
	writeExpiryKeys(t, minute, 1)
	requireExpiredBacklog(t, fixture, expiredBacklogBound, true)

	setPrefixedValue(
		t,
		tuple.Tuple{sessionExpiryFamily, minute - 1, 0, expiredBacklogMalformed},
		nil,
	)
	pool := ledgerAdminPool(t)
	operatorID := uuid.Must(uuid.NewV7())
	root, output := searchReadCommand(
		t,
		fixture.Config,
		pool,
		operatorID,
		"ops",
		"search",
		"verify",
	)
	err := root.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), expiredBacklogCountRead) || output.Len() != 0 {
		t.Fatalf(
			"ops search verify with a malformed expiry key = %v, output = %q; want %q without output",
			err,
			output.String(),
			expiredBacklogCountRead,
		)
	}

	requireFailedSearchRead(
		t,
		requireSearchReadEvent(
			t,
			pool,
			operatorID,
			audit.VerbOpsSearchVerify,
			expiredBacklogReference,
		),
		err,
	)
}

func requireExpiredBacklog(
	t *testing.T,
	fixture integration.QueryFixture,
	count int,
	truncated bool,
) {
	t.Helper()
	report, err := integration.RunSearchVerifyCommand(t, fixture.Config)
	if err != nil {
		t.Fatalf("ops search verify failed: %v", err)
	}

	if report.ExpiredSessions != count || report.ExpiredSessionsTruncated != truncated {
		t.Fatalf(
			"ops search verify at %s reported %d expired sessions and truncated %t, want %d and %t",
			clock.Now().UTC().Format(time.RFC3339),
			report.ExpiredSessions,
			report.ExpiredSessionsTruncated,
			count,
			truncated,
		)
	}
}

// writeExpiryKeys stores count expiry keys under the test prefix for the
// given minute without session headers.
func writeExpiryKeys(t *testing.T, minute int64, count int) {
	t.Helper()

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
		transaction.Set(
			prefixedKey(prefix, tuple.Tuple{sessionExpiryFamily, minute, 0, uuid.NewString()}),
			nil,
		)
	}

	if err := transaction.Commit().Get(); err != nil {
		t.Fatalf("write %d expiry keys: %v", count, err)
	}
}
