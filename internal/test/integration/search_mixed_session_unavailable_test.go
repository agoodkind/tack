package integration_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// sessionHoldCacheLifetime is the token and membership cache lifetime of both processes. The
	// revocation check ends after the removed member's cached membership expires. The test
	// search occupies the slot of the second process before that expiry.
	sessionHoldCacheLifetime = 5 * time.Second
	// sessionHold is how long the test search occupies the only search slot of the second
	// process. It covers the cache lifetime, one revocation round with two admission waits, the
	// rebuild start, and the first admission wait of the rebuild sessions.
	sessionHold = 40 * time.Second
	// sessionHoldDeadline is the search query deadline of both processes. The test search keeps
	// its slot until the proxy releases its predict request, which happens before this deadline.
	sessionHoldDeadline = "50s"
	// sessionHoldOperatorID is the operator of this test's search-mixed run. Its reindex audit
	// events stay separate from the events of searchMixedOperatorID.
	sessionHoldOperatorID = "019dd226-440e-729a-a442-281aaf73ca31"
)

type sessionUnavailableOutput struct {
	Result struct {
		Sessions struct {
			Unavailable        int `json:"unavailable"`
			ContinuationErrors int `json:"continuation_errors"`
		} `json:"sessions"`
		Rebuild struct {
			Requested   bool   `json:"requested"`
			TargetIndex string `json:"target_index"`
		} `json:"rebuild"`
	} `json:"result"`
}

// TestSearchMixedSessionsRetryUnavailableSearch runs search-mixed with a rebuild against two
// processes. The second process has one search slot, and its engine requests pass through
// predictHoldProxy. After search-mixed removes the member in the revocation check, the test
// starts its own search on the second process, and the proxy keeps that search's predict request
// open until after the rebuild sessions start. The rebuild sessions request pages from the second
// process and receive the unavailable text. The run must succeed and count those responses.
func TestSearchMixedSessionsRetryUnavailableSearch(t *testing.T) {
	cfg := newCohortEngines(t)
	t.Setenv("OPENSEARCH_WORKER_CONCURRENCY", "1")
	t.Setenv("OPENSEARCH_WORKER_IDLE_INTERVAL", "1s")
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", "30s")
	t.Setenv("OPENSEARCH_QUERY_DEADLINE", sessionHoldDeadline)
	cfg.AuthTokenCacheLifetime, cfg.AuthMembershipCacheLifetime = sessionHoldCacheLifetime, sessionHoldCacheLifetime
	seed := clock.Now().UnixNano()
	scale, err := datagen.ParseScale(revocationScale)
	if err != nil {
		t.Fatalf("parse scale: %v", err)
	}
	identities, err := datagen.BootstrapIdentities(integration.WithTestOperation(t, t.Context()), cfg, seed, scale)
	if err != nil {
		t.Fatalf("bootstrap the search-mixed identities: %v", err)
	}
	marker := "session hold " + strconv.FormatInt(seed, 10)
	endpoints, second, proxy := startRevocationProcesses(t, cfg, marker)
	watchContext, stopWatch := context.WithCancel(integration.WithTestOperation(t, t.Context()))
	watched := make(chan error, 1)
	go func() {
		watched <- occupySlotAfterRemoval(watchContext, cfg, proxy, datagen.NewDriver(second, false, seed), identities.Workspaces[0], marker, sessionHold)
	}()
	output, runErr := runSearchMixedCommandAs(t, cfg, sessionHoldOperatorID, "--seed", strconv.FormatInt(seed, 10), "--rate", "6", "--write-rate", "6",
		"--duration", "40s", "--concurrency", "2", "--burst-writes", "4", "--catch-up", "5m", "--rebuild",
		"--endpoints", strings.Join(endpoints, ","), "--commit")
	stopWatch()
	var envelope sessionUnavailableOutput
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatalf("decode the search-mixed result: %v (command error=%v; output=%s)", err, runErr, output)
	}
	deleteSearchMixedIndex(t, cfg, envelope.Result.Rebuild.TargetIndex)
	if err := <-watched; err != nil {
		t.Fatalf("the test could not occupy the search slot during the rebuild sessions: %v (output=%s)", err, output)
	}
	if runErr != nil {
		t.Fatalf("search-mixed failed while the second process refused searches: %v (output=%s)", runErr, output)
	}
	sessions := envelope.Result.Sessions
	if !envelope.Result.Rebuild.Requested || sessions.Unavailable <= 0 || sessions.ContinuationErrors != 0 {
		t.Fatalf("the rebuild sessions must count unavailable responses and finish without continuation errors: %s", output)
	}
}
