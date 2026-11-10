package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/test/integration"
)

// parentEdgeOperatorID is the operator ID that parentEdgeFixture.runOps
// passes to every command.
var parentEdgeOperatorID = uuid.MustParse("019dd226-440e-729a-a442-281aaf73ca30")

// workerInitiation is the initiating operation that a worker step records.
type workerInitiation struct {
	OperationID uuid.UUID `json:"initiated_operation_id"`
	ActorID     uuid.UUID `json:"initiated_actor_id"`
}

// currentInitiation returns the initiation that the current index
// replacement stores.
func currentInitiation(t *testing.T, fixture parentEdgeFixture) searchdomain.Initiation {
	t.Helper()
	rebuild, found, err := fixture.stores.SearchRebuilds(clock.Wall{}).CurrentRebuild(integration.WithTestOperation(t, t.Context()))
	if err != nil || !found {
		t.Fatalf("reading the index replacement failed or found none (found %t): %v", found, err)
	}
	return rebuild.Initiation
}

// rolloutInitiation returns the initiation that the access rollout of the
// fixture organization stores.
func rolloutInitiation(t *testing.T, fixture parentEdgeFixture) searchdomain.Initiation {
	t.Helper()
	rollouts := fixture.stores.SearchRollouts(clock.Wall{}, fixture.stores.SearchPolicySet())
	rollout, err := rollouts.Current(integration.WithTestOperation(t, t.Context()), fixture.kind.orgID)
	if err != nil {
		t.Fatalf("reading the access rollout failed: %v", err)
	}
	return rollout.Initiation
}

// requireCommandInitiation requires initiation to name the parent edge
// operator and an intent of verb that the operator outbox stores.
func requireCommandInitiation(t *testing.T, fixture parentEdgeFixture, verb audit.Verb, initiation searchdomain.Initiation) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), fixture.cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("opening the operator outbox pool failed: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, intent := range eventsInPhase(outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql", string(verb), parentEdgeOperatorID.String()), audit.PhaseIntent, uuid.Nil) {
		if intent.Context.OperationID == initiation.OperationID && initiation.ActorID == parentEdgeOperatorID {
			return
		}
	}
	t.Fatalf("initiation %+v does not name an intent of %s by operator %s", initiation, verb, parentEdgeOperatorID)
}

// requireWorkerInitiations requires each worker step of verb to record
// initiation, and each worker operation to use the worker service actor and
// its own operation ID.
func requireWorkerInitiations(t *testing.T, events []audit.Event, verb audit.Verb, initiation searchdomain.Initiation) {
	t.Helper()
	workerID := cli.ServiceActorID(appruntime.SearchWorkerService)
	operations := map[uuid.UUID]bool{}
	for _, event := range eventsInPhase(events, audit.PhaseIntent, uuid.Nil) {
		if event.Actor.Type != audit.ActorService || event.Actor.ID != workerID || operations[event.Context.OperationID] {
			t.Fatalf("worker intent %s records actor %s %s and operation %s, which is not a new operation of service %s",
				event.EventID, event.Actor.Type, event.Actor.ID, event.Context.OperationID, workerID)
		}
		operations[event.Context.OperationID] = true
	}
	for _, event := range events {
		if event.Verb != string(verb) || event.Context.ParentOperationID == uuid.Nil {
			continue
		}
		var recorded workerInitiation
		if err := json.Unmarshal(event.Extra, &recorded); err != nil {
			t.Fatalf("decoding the step evidence of event %s failed: %v", event.EventID, err)
		}
		if recorded.OperationID != initiation.OperationID || recorded.ActorID != initiation.ActorID {
			t.Fatalf("worker step %s records initiation %+v instead of %+v", event.EventID, recorded, initiation)
		}
	}
}
