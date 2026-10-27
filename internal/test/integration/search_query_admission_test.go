package integration_test

import (
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// admissionQuery is the query text of every search in the admission test.
	admissionQuery = "quarry lantern admission"
	// admissionUnavailableText is the public tack_search response for an
	// unavailable engine.
	admissionUnavailableText = "Search is temporarily unavailable."
	// admissionWait is OPENSEARCH_QUERY_ADMISSION_WAIT of the limited process.
	admissionWait = 2 * time.Second
	// admissionMargin bounds the MCP, authentication, and FoundationDB work of
	// one refused search beyond the admission wait.
	admissionMargin = 5 * time.Second
	// admissionWaiters is the number of searches that start while the first
	// search keeps the only slot.
	admissionWaiters = 3
	// admissionHoldTimeout bounds the wait for the first predict request to
	// arrive at the proxy.
	admissionHoldTimeout = time.Minute
)

// admissionOutcome is the result and duration of one search that waited
// for a slot.
type admissionOutcome struct {
	err     error
	elapsed time.Duration
}

// TestSearchAdmissionRefusesBeyondProcessLimit builds a second process with
// OPENSEARCH_QUERY_CONCURRENCY set to 1 and OPENSEARCH_QUERY_ADMISSION_WAIT
// set to admissionWait. Its engine requests pass through predictHoldProxy,
// and the proxy keeps the predict request of the first search open, so that
// search keeps the only slot. Each of admissionWaiters concurrent tack_search
// calls must return the unavailable text after at least admissionWait and
// within admissionWait plus admissionMargin, and none of them may send a
// predict request. After the proxy forwards the held request, the first
// search must return a page.
func TestSearchAdmissionRefusesBeyondProcessLimit(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	t.Setenv("OPENSEARCH_QUERY_CONCURRENCY", "1")
	t.Setenv("OPENSEARCH_QUERY_ADMISSION_WAIT", admissionWait.String())
	harness, proxy := newPredictHoldHarness(t, fixture)
	proxy.arm()

	admitted := make(chan error, 1)
	go func() {
		_, err := integration.TrySearch(harness, admissionQuery, "")
		admitted <- err
	}()
	select {
	case <-proxy.held:
	case err := <-admitted:
		t.Fatalf("the first search returned before the proxy received its predict request: %v", err)
	case <-time.After(admissionHoldTimeout):
		t.Fatalf("the proxy did not receive the predict request of the first search within %s", admissionHoldTimeout)
	}

	refusals := make(chan admissionOutcome, admissionWaiters)
	for range admissionWaiters {
		go func() {
			started := clock.Now()
			_, err := integration.TrySearch(harness, admissionQuery, "")
			refusals <- admissionOutcome{err: err, elapsed: clock.Since(started)}
		}()
	}
	for number := range admissionWaiters {
		outcome := <-refusals
		if outcome.err == nil || outcome.err.Error() != admissionUnavailableText {
			t.Fatalf("waiting search %d returned error %v, want %q", number+1, outcome.err, admissionUnavailableText)
		}
		if outcome.elapsed < admissionWait || outcome.elapsed > admissionWait+admissionMargin {
			t.Fatalf("waiting search %d returned after %s, want between %s and %s", number+1, outcome.elapsed, admissionWait, admissionWait+admissionMargin)
		}
		t.Logf("waiting search %d returned the unavailable text after %s", number+1, outcome.elapsed)
	}
	if predicts := proxy.predictCount(); predicts != 1 {
		t.Fatalf("the proxy received %d predict requests while one search kept the only slot, want 1", predicts)
	}

	proxy.release()
	if err := <-admitted; err != nil {
		t.Fatalf("the admitted search failed after the proxy forwarded its predict request: %v", err)
	}
}
