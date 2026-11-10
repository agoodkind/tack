//go:build runtimeaudit

package integration_test

import (
	"context"
	"testing"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	// sweepProofIdle and sweepProofAbsolute allow time for one continuation
	// before the session expires.
	sweepProofIdle     = 15 * time.Second
	sweepProofAbsolute = 30 * time.Second
	// sweepProofRefusalWait limits the wait for the proof to refuse when the
	// ledger does not store a completed header delete.
	sweepProofRefusalWait = 5 * time.Second
)

// TestSearchRuntimeAuditSweepProofRequiresHeaderDelete checks the proof
// using an audit_reader login. After a sweep fails event validation, the
// test checks that the proof rejects a replay removal row and a failed
// delete row. After a later search sweeps the session, the test checks
// that the proof accepts a completed header delete.
func TestSearchRuntimeAuditSweepProofRequiresHeaderDelete(t *testing.T) {
	fixture := newFailureFixture(t)
	searcher := integration.QueryService(
		t,
		fixture.QueryFixture,
		failureAuditResponseBytes,
		sweepProofIdle,
		sweepProofAbsolute,
	)
	request := integration.ServiceRequest(t, fixture.QueryFixture, failureAuditQuery)
	first, err := searcher.Search(integration.WithTestOperation(t, t.Context()), request)
	if err != nil || first.Complete {
		t.Fatalf(
			"open the session: complete %t, %v, want a session with another page",
			first.Complete,
			err,
		)
	}
	continuation := request
	continuation.SessionID, continuation.Version = first.SessionID, first.NextVersion
	if _, err := searcher.Search(
		integration.WithTestOperation(t, t.Context()),
		continuation,
	); err != nil {
		t.Fatalf("continue session %s: %v", first.SessionID, err)
	}
	store := fixture.Stores.SearchSessions(clock.Wall{}, sweepProofIdle)
	session, err := store.Load(t.Context(), first.SessionID)
	if err != nil {
		t.Fatalf("load session %s: %v", first.SessionID, err)
	}
	waitUntilTime(t, session.IdleDeadline.Truncate(time.Minute).Add(time.Minute+sessionAuditMargin))

	failing, err := audit.WithMutationContext(t.Context(), futureOperation(t))
	if err != nil {
		t.Fatalf("attach the failing operation: %v", err)
	}
	_, err = searcher.Search(failing, request)
	requireChangeEventFailure(t, "Search under the failing operation", err)
	orgID := fixture.Workspaces[0].OrgID
	fixture.audited.waitForRows(
		t,
		orgID,
		func(current []audit.Row) bool {
			return len(cleanupFailures(current, session.ID, searchdomain.CleanupDelete)) == 1 &&
				len(
					sessionChanges(
						current,
						session.ID,
						audit.VerbSearchSessionDelete,
						audit.TargetSearchSessionReplay,
					),
				) == 1
		},
	)
	cfg := *fixture.Config
	cfg.AuditReaderDSN = roleLoginDSN(
		t,
		fixture.audited.ledger.stack.admin,
		testenv.Ledger(t),
		auditReaderRole,
	)
	factory := cli.System(&cfg)
	refusal, cancel := context.WithTimeout(t.Context(), sweepProofRefusalWait)
	defer cancel()
	if err := ops.AwaitDatagenSessionHeaderDelete(refusal, factory, orgID, session.ID); err == nil {
		t.Fatalf(
			"the proof accepted session %s after its replay removal and failed delete",
			session.ID,
		)
	}

	if _, err := searcher.Search(
		integration.WithTestOperation(t, t.Context()),
		request,
	); err != nil {
		t.Fatalf("the search that sweeps session %s failed: %v", session.ID, err)
	}
	fixture.audited.waitForRows(
		t,
		orgID,
		func(current []audit.Row) bool {
			return len(
				sessionChanges(
					current,
					session.ID,
					audit.VerbSearchSessionDelete,
					audit.TargetSearchSession,
				),
			) == 1
		},
	)
	if err := ops.AwaitDatagenSessionHeaderDelete(
		t.Context(),
		factory,
		orgID,
		session.ID,
	); err != nil {
		t.Fatalf("the proof refused the completed header delete of session %s: %v", session.ID, err)
	}
}
