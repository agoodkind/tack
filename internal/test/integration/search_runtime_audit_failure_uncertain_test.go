//go:build runtimeaudit

package integration_test

import (
	"testing"

	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// TestSearchRuntimeAuditCleanupCommitTimeoutIsUncertain opens a session
// and waits past its expiry minute. A new search sweeps the expired
// session. A log hook pauses FoundationDB after the close transaction
// prepares its writes. The commit times out with error 1031. The hook
// resumes FoundationDB at the cleanup failure log. The test requires
// one incomplete update row in audit.events with the
// cleanup_result_uncertain code, the sweep operation ID, and the session
// owner. It requires zero failed close rows.
// TestSearchRuntimeAuditCleanupFailureRecorded covers the definite
// failure. The test cannot choose whether the timed-out commit applies.
// The test does not require a search result. The search result does not
// determine this evidence.
func TestSearchRuntimeAuditCleanupCommitTimeoutIsUncertain(t *testing.T) {
	fixture := newFailureFixture(t)
	searcher := integration.QueryService(
		t,
		fixture.QueryFixture,
		failureAuditResponseBytes,
		failureAuditIdle,
		failureAuditAbsolute,
	)
	request := integration.ServiceRequest(t, fixture.QueryFixture, failureAuditQuery)
	expired := fixture.openExpired(t, searcher, request)
	container := testenv.FoundationDBContainer(fixture.Config.FDBClusterFile)
	hooks := installRetireLogHooks(
		t,
		expired.ID.String(),
		map[string]func(){
			retireCommitReady:          func() { setContainerPaused(t, container, true) },
			retireCleanupFailedMessage: func() { setContainerPaused(t, container, false) },
		},
	)
	sweepContext := integration.WithTestOperation(t, t.Context())
	operation, err := audit.RequireMutationContext(sweepContext)
	if err != nil {
		t.Fatalf("read the sweep operation: %v", err)
	}

	if _, err := searcher.Search(sweepContext, request); err != nil {
		t.Logf("the search with the timed-out close commit returned: %v", err)
	}
	hooks.requireFinished(t)
	rows := fixture.audited.waitForRows(
		t,
		fixture.Workspaces[0].OrgID,
		func(current []audit.Row) bool {
			return len(
				cleanupFailures(
					current,
					expired.ID,
					searchdomain.CleanupStep(retireResultUncertain),
				),
			) == 1
		},
	)
	uncertain := onlyRow(
		t,
		cleanupFailures(rows, expired.ID, searchdomain.CleanupStep(retireResultUncertain)),
		"the uncertain close result",
	)
	if uncertain.Outcome != audit.OutcomeIncomplete ||
		uncertain.Action != string(audit.VerbSearchSessionUpdate) ||
		uncertain.Context.OperationID != operation.Context.OperationID ||
		uncertain.Context.Target.OwnerID != expired.PrincipalID {
		t.Fatalf(
			"uncertain close row = %+v, context %+v, want an incomplete update by operation %s",
			uncertain,
			uncertain.Context,
			operation.Context.OperationID,
		)
	}
	if failed := cleanupFailures(rows, expired.ID, searchdomain.CleanupClose); len(failed) != 0 {
		t.Fatalf(
			"session %s has failed close rows %+v after an uncertain commit, want 0",
			expired.ID,
			failed,
		)
	}
}
