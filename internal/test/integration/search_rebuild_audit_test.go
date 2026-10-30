package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/test/integration"
)

const searchAuditPageLimit = 500

// TestSearchWorkerRecordsStructuralSlices verifies that the worker records
// completed steps for index replacement and access rollout, and does not
// record events for page indexing. Each worker operation uses the worker
// service actor and its own operation ID. Each step records the operation
// and operator of the command that began the replacement or rollout.
func TestSearchWorkerRecordsStructuralSlices(t *testing.T) {
	fixture := newParentEdgeFixture(t)
	mark := searchOutboxTail(t, fixture.stores)
	fixture.createLeaf(t, fixture.firstEntry, "amber structure leaf")
	fixture.drain(t)
	if events := searchWorkerEvents(t, fixture.stores, mark); len(events) != 0 {
		t.Fatalf("page indexing recorded %d worker audit events instead of zero", len(events))
	}

	source := servingIndex(t, fixture)
	target := beginReplacement(t, fixture)
	replacement := currentInitiation(t, fixture)
	requireCommandInitiation(t, fixture, audit.VerbOpsSearchReindex, replacement)
	runReplacementUntil(t, fixture, replacementFinished)
	rolloutMark := searchOutboxTail(t, fixture.stores)
	authority := fixture.kind.orgID.String()
	if _, err := fixture.runOps(t, "ops", "search", "access-rollout", "--authority", authority, "--candidate", searchaccess.RotatedVersion); err != nil {
		t.Fatalf("the ops search access-rollout command failed: %v", err)
	}
	rollout := rolloutInitiation(t, fixture)
	requireCommandInitiation(t, fixture, audit.VerbOpsSearchAccessRollout, rollout)
	runRolloutUntilStable(t, fixture)

	events := searchWorkerEvents(t, fixture.stores, mark)
	requireWorkerInitiations(t, events, audit.VerbSearchIndexStructure, replacement)
	requireWorkerInitiations(t, searchWorkerEvents(t, fixture.stores, rolloutMark), audit.VerbSearchAccessRolloutPhase, rollout)
	completed := completedStepTargets(t, events)
	for _, reference := range []string{
		"opensearch/index/" + target, "opensearch/public-alias", "opensearch/index/" + source,
		"foundationdb/search/access-rollout/" + authority,
	} {
		if !completed[reference] {
			t.Fatalf("the worker events do not include a completed step on %s; completed steps: %v", reference, completed)
		}
	}
	requirePermanentSearchWorkerEvents(t, fixture.stores, events)
}

func runRolloutUntilStable(t *testing.T, fixture parentEdgeFixture) {
	t.Helper()
	rollouts := fixture.stores.SearchRollouts(clock.Wall{}, fixture.stores.SearchPolicySet())
	deadline := clock.Now().Add(replacementDeadline)
	for clock.Now().Before(deadline) {
		current, err := rollouts.Current(integration.WithTestOperation(t, t.Context()), fixture.kind.orgID)
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

func completedStepTargets(t *testing.T, events []audit.Event) map[string]bool {
	t.Helper()
	completed := map[string]bool{}
	for _, event := range events {
		switch audit.Verb(event.Verb) {
		case audit.VerbSearchIndexStructure, audit.VerbSearchAccessRolloutPhase:
		default:
			t.Fatalf("worker event %s uses verb %s; the test allows only the structural verbs", event.EventID, event.Verb)
		}
		eventContext := event.Context
		if eventContext.Phase == audit.PhaseResult && eventContext.ParentOperationID != uuid.Nil &&
			event.Outcome == audit.OutcomeCompleted && eventContext.Target != nil {
			completed[eventContext.Target.Reference] = true
		}
	}
	return completed
}

func searchOutboxTail(t *testing.T, stores *fdbadapter.Stores) []byte {
	t.Helper()
	var mark []byte
	for {
		entries, err := stores.OpsOutbox.ReadOutboxFrom(integration.WithTestOperation(t, t.Context()), mark, searchAuditPageLimit)
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
	workerID := cli.ServiceActorID(appruntime.SearchWorkerService)
	var events []audit.Event
	for {
		entries, err := stores.OpsOutbox.ReadOutboxFrom(integration.WithTestOperation(t, t.Context()), mark, searchAuditPageLimit)
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
			if event.Actor.ID == workerID {
				events = append(events, event)
			}
		}
		mark = entries[len(entries)-1].Mark
	}
}
