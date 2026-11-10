//go:build runtimeaudit

package integration_test

import (
	"net/http/httputil"
	"slices"
	"strings"
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// TestSearchRuntimeAuditPartialSnapshotCloseRecordingLossRefusesPage checks that
// search refuses the page and records a failed read when recording the partial
// point in time delete result fails.
func TestSearchRuntimeAuditPartialSnapshotCloseRecordingLossRefusesPage(t *testing.T) {
	fixture := newFailureFixture(t)
	harness, capture := newCaptureHarness(t, fixture.QueryFixture, false)
	before := listOpenPITs(t, fixture)
	t.Cleanup(func() {
		for _, identifier := range addedPITs(listOpenPITs(t, fixture), before) {
			deleteObservedSnapshot(t, fixture, identifier)
		}
	})
	rewrite := capture.engine.Rewrite
	capture.engine.Rewrite = func(outbound *httputil.ProxyRequest) {
		rewrite(outbound)
		outbound.Out.Header.Del("Accept-Encoding")
	}
	capture.engine.ModifyResponse = failPointInTimeShards
	gate := newCleanupLossGate(
		t,
		testenv.FoundationDBContainer(testenv.FoundationDB(t)),
		nil,
		cleanupLossStepLog,
	)
	cleanupLossPauseOnDelete(capture, gate)
	gate.install()

	page, err := integration.TrySearch(harness, failureAuditQuery, "")

	requireCleanupLossRefusal(
		t,
		gate,
		page,
		err,
		[]string{"failed shards", "record external step result"},
		[]string{"FoundationDB error code 1031", "context deadline exceeded"},
	)
	rows := fixture.audited.waitForRows(
		t,
		fixture.Workspaces[0].OrgID,
		func(current []audit.Row) bool {
			return len(cleanupLossReads(current, audit.OutcomeFailed)) == 1
		},
	)
	refused := cleanupLossReads(rows, audit.OutcomeFailed)[0]
	if refused.ActorID != fixture.Workspaces[0].Actors[0].UserID || refused.Error == nil ||
		refused.Error.Code != "audit_recording_failed" {
		t.Fatalf(
			"the failed read event did not preserve the actor or the audit_recording_failed code: %+v",
			refused,
		)
	}
}

// requireCleanupLossRefusal checks the gate signals, search response, and logged
// causes for a cleanup recording failure.
func requireCleanupLossRefusal(
	t *testing.T,
	gate *cleanupLossGate,
	page integration.SearchPage,
	err error,
	failureHas []string,
	failureAny []string,
) {
	t.Helper()
	for name, signal := range map[string]chan struct{}{
		"pause":  gate.pausedSeen,
		"resume": gate.resumeSeen,
	} {
		select {
		case <-signal:
		default:
			t.Fatalf("the gate did not signal %s", name)
		}
	}
	if err == nil || err.Error() != sessionTimeoutUnavailableText {
		t.Fatalf("search returned error %v; expected text %q", err, sessionTimeoutUnavailableText)
	}
	if len(page.IDs) != 0 || page.Cursor != "" {
		t.Fatalf(
			"search returned %d results and cursor %q; expected an empty page",
			len(page.IDs),
			page.Cursor,
		)
	}
	failure := gate.failureText()
	for _, want := range failureHas {
		if !strings.Contains(failure, want) {
			t.Fatalf("the logged error %q did not contain the expected cause %q", failure, want)
		}
	}
	if len(failureAny) > 0 &&
		!slices.ContainsFunc(
			failureAny,
			func(want string) bool { return strings.Contains(failure, want) },
		) {
		t.Fatalf(
			"the logged error %q did not contain the expected cause %q",
			failure,
			strings.Join(failureAny, " or "),
		)
	}
}
