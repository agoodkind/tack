package integration_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

// parentEdgeOperatorID is the operator ID that parentEdgeFixture.runOps
// passes to every command.
var parentEdgeOperatorID = uuid.MustParse("019dd226-440e-729a-a442-281aaf73ca30")

// currentInitiation returns the initiation that the current index
// replacement stores.
func currentInitiation(t *testing.T, fixture parentEdgeFixture) searchdomain.Initiation {
	t.Helper()
	rebuild, found, err := fixture.stores.SearchRebuilds(clock.Wall{}).CurrentRebuild(
		integration.WithTestOperation(t, t.Context()),
	)
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
	rollout, err := rollouts.Current(
		integration.WithTestOperation(t, t.Context()),
		fixture.kind.orgID,
	)
	if err != nil {
		t.Fatalf("reading the access rollout failed: %v", err)
	}

	return rollout.Initiation
}

// requireCommandInitiation requires initiation to name the parent edge
// operator and an intent of verb that the operator outbox stores.
func requireCommandInitiation(
	t *testing.T,
	fixture parentEdgeFixture,
	verb audit.Verb,
	initiation searchdomain.Initiation,
) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), fixture.cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("opening the operator outbox pool failed: %v", err)
	}

	t.Cleanup(pool.Close)

	for _, intent := range eventsInPhase(
		outboxEventsByQuery(
			t,
			pool,
			"runtime_audit_verb_outbox.sql",
			string(verb),
			parentEdgeOperatorID.String(),
		),
		audit.PhaseIntent,
		uuid.Nil,
	) {
		if intent.Context.OperationID == initiation.OperationID &&
			initiation.ActorID == parentEdgeOperatorID {
			return
		}
	}

	t.Fatalf(
		"initiation %+v does not name an intent of %s by operator %s",
		initiation,
		verb,
		parentEdgeOperatorID,
	)
}
