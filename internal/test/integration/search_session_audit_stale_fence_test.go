//go:build runtimeaudit

package integration_test

import (
	"strings"
	"testing"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

func requireStaleDeleteFenced(
	t *testing.T,
	stores *fdbadapter.Stores,
	index string,
	session searchdomain.Session,
) {
	t.Helper()
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	mark := searchOutboxTail(t, stores)
	staleContext := integration.WithTestOperation(t, t.Context())
	operation, err := audit.RequireMutationContext(staleContext)
	if err != nil {
		t.Fatalf("read the stale attempt operation: %v", err)
	}
	hooks := installRetireLogHooks(
		t,
		session.ID.String(),
		map[string]func(){
			retireAttemptSavedMessage: func() {
				waitUntilTime(t, clock.Now().Add(retireAttemptWait))
				_, recoverErr := rebuilds.RetireIndexSessions(
					withFutureOperation(t, t.Context()),
					index,
					retireSliceLimit,
				)
				requireChangeEventFailure(t, "RetireIndexSessions recovery of "+index, recoverErr)
			},
		},
	)
	remaining, err := rebuilds.RetireIndexSessions(staleContext, index, retireSliceLimit)
	hooks.requireFinished(t)
	if err == nil || !remaining || !strings.Contains(err.Error(), retireAttemptFenced) {
		t.Fatalf(
			"RetireIndexSessions of %s with a stale delete attempt = %t, %v, want remaining work and the fence text %q",
			index,
			remaining,
			err,
			retireAttemptFenced,
		)
	}
	for _, verb := range []audit.Verb{audit.VerbSearchSessionUpdate, audit.VerbSearchSessionDelete} {
		if count := sessionChangeEvents(
			t,
			stores,
			mark,
			session,
			operation.Context.OperationID,
			verb,
		); count != 0 {
			t.Fatalf(
				"the fenced operation %s wrote %d %s change events for session %s, want 0",
				operation.Context.OperationID,
				count,
				verb,
				session.ID,
			)
		}
	}
}
