package integration_test

import (
	"encoding/json"
	"testing"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/test/integration"
)

const searchAuditPageLimit = 500

// TestSearchWorkerRecordsStructuralSlices verifies that requested index work
// records its final result under the initiating command and actor. Internal
// indexing and worker slices do not record separate compliance events.
func TestSearchWorkerRecordsStructuralSlices(t *testing.T) {
	fixture := newParentEdgeFixture(t)
	mark := searchOutboxTail(t, fixture.stores)
	fixture.createLeaf(t, fixture.firstEntry, "amber structure leaf")
	fixture.drain(t)
	if events := searchWorkerEvents(t, fixture.stores, mark); len(events) != 0 {
		t.Fatalf("page indexing recorded %d worker audit events instead of zero", len(events))
	}

	beginReplacement(t, fixture)
	replacement := currentInitiation(t, fixture)
	requireCommandInitiation(t, fixture, audit.VerbOpsSearchReindex, replacement)
	runReplacementUntil(t, fixture, replacementFinished)
	authority := fixture.kind.orgID.String()
	if _, err := fixture.runOps(
		t,
		"ops",
		"search",
		"access-rollout",
		"--authority",
		authority,
		"--candidate",
		searchaccess.RotatedVersion,
	); err != nil {
		t.Fatalf("the ops search access-rollout command failed: %v", err)
	}

	rollout := rolloutInitiation(t, fixture)
	requireCommandInitiation(t, fixture, audit.VerbOpsSearchAccessRollout, rollout)
	runRolloutUntilStable(t, fixture)

	if events := searchWorkerEvents(t, fixture.stores, mark); len(events) != 0 {
		t.Fatalf("internal index work recorded %d compliance events", len(events))
	}

	requirePermanentSearchResults(t, replacement, rollout)
}

func runRolloutUntilStable(t *testing.T, fixture parentEdgeFixture) {
	t.Helper()
	rollouts := fixture.stores.SearchRollouts(clock.Wall{}, fixture.stores.SearchPolicySet())

	deadline := clock.Now().Add(replacementDeadline)
	for clock.Now().Before(deadline) {
		current, err := rollouts.Current(
			integration.WithTestOperation(t, t.Context()),
			fixture.kind.orgID,
		)
		if err != nil {
			t.Fatalf("reading the access rollout failed: %v", err)
		}

		if current.Phase == searchdomain.AccessStable {
			return
		}

		runSliceOrWait(t, fixture)
	}

	t.Fatalf("the access rollout did not return to the stable phase within %s", replacementDeadline)
}

func searchOutboxTail(t *testing.T, stores *fdbadapter.Stores) []byte {
	t.Helper()
	var mark []byte

	for {
		entries, err := stores.OpsOutbox.ReadOutboxFrom(
			integration.WithTestOperation(t, t.Context()),
			mark,
			searchAuditPageLimit,
		)
		if err != nil {
			t.Fatalf("reading the outbox failed: %v", err)
		}

		if len(entries) == 0 {
			return mark
		}

		mark = entries[len(entries)-1].Mark
	}
}

func searchWorkerEvents(t *testing.T, stores *fdbadapter.Stores, mark []byte) []audit.Event {
	t.Helper()
	var events []audit.Event

	for {
		entries, err := stores.OpsOutbox.ReadOutboxFrom(
			integration.WithTestOperation(t, t.Context()),
			mark,
			searchAuditPageLimit,
		)
		if err != nil {
			t.Fatalf("reading the outbox failed: %v", err)
		}

		if len(entries) == 0 {
			return events
		}

		for _, entry := range entries {
			var event audit.Event
			if err := json.Unmarshal(entry.Event, &event); err != nil {
				t.Fatalf("decoding outbox entry %s failed: %v", entry.Event, err)
			}

			if (event.Verb == string(
				audit.VerbSearchIndexStructure,
			) || event.Verb == string(audit.VerbSearchAccessRolloutPhase)) &&
				event.Context.OperationClass == audit.OperationExternal {
				events = append(events, event)
			}
		}

		mark = entries[len(entries)-1].Mark
	}
}
