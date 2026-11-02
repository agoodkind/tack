//go:build runtimeaudit

package integration_test

import (
	"net/http"
	"net/http/httputil"
	"strings"
	"sync/atomic"
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	servingIndexReadMessage  = "store.search_index.read"
	externalRecordFailedText = "audit.external.record_failed"
)

// TestSearchRuntimeAuditIntentFailureRefusesSnapshot pauses the test
// FoundationDB container with a log hook after search reads the serving
// index. The pause causes the point in time create intent write to the real
// outbox to fail. A second log hook resumes the container when the external
// record failure log appears. The test checks that tack_search returns the
// fixed unavailable text, the read row stores audit_recording_failed, and the
// capture proxy receives zero point in time create requests during that call.
// A later search through the same proxy must succeed and send one create
// request, proving that the count detects a create request.
func TestSearchRuntimeAuditIntentFailureRefusesSnapshot(t *testing.T) {
	fixture := newFailureFixture(t)
	harness, capture := newCaptureHarness(t, fixture.QueryFixture, false)
	before := listOpenPITs(t, fixture)
	t.Cleanup(func() {
		for _, identifier := range addedPITs(listOpenPITs(t, fixture), before) {
			deleteObservedSnapshot(t, fixture, identifier)
		}
	})
	var creates atomic.Int64
	rewrite := capture.engine.Rewrite
	capture.engine.Rewrite = func(outbound *httputil.ProxyRequest) {
		if outbound.In.Method == http.MethodPost && strings.HasSuffix(outbound.In.URL.Path, pointInTimePathSuffix) {
			creates.Add(1)
		}
		rewrite(outbound)
	}
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	hooks := installRetireLogHooks(t, "", map[string]func(){
		servingIndexReadMessage:  func() { setContainerPaused(t, container, true) },
		externalRecordFailedText: func() { setContainerPaused(t, container, false) },
	})

	_, err := integration.TrySearch(harness, failureAuditQuery, "")
	hooks.requireFinished(t)
	if err == nil || err.Error() != sessionTimeoutUnavailableText {
		t.Fatalf("tack_search returned error %v; want unavailable text %q", err, sessionTimeoutUnavailableText)
	}
	if count := creates.Load(); count != 0 {
		t.Fatalf("The proxy received %d point in time create requests during the refused search; want 0", count)
	}
	rows := fixture.audited.waitForRows(t, fixture.Workspaces[0].OrgID, func(current []audit.Row) bool {
		return len(cleanupLossReads(current, audit.OutcomeFailed)) == 1
	})
	refused := cleanupLossReads(rows, audit.OutcomeFailed)[0]
	if refused.Error == nil || refused.Error.Code != "audit_recording_failed" {
		t.Fatalf("Read row = %+v; want a failed read row with code audit_recording_failed", refused)
	}

	integration.CallSearch(t, harness, failureAuditQuery, "")
	if count := creates.Load(); count != 1 {
		t.Fatalf("The proxy received %d point in time create requests during the later search; want 1", count)
	}
}
