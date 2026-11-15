package integration_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

func requireRollbackAudit(
	t *testing.T,
	fixture rolloutFixture,
	operatorID uuid.UUID,
	authorityID uuid.UUID,
) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), fixture.cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("cannot open outbox pool: %v", err)
	}

	t.Cleanup(pool.Close)
	events := outboxEventsByQuery(
		t,
		pool,
		"runtime_audit_verb_outbox.sql",
		string(audit.VerbOpsSearchAccessRollback),
		operatorID.String(),
	)
	attempts := eventsInPhase(events, audit.PhaseAttempt, uuid.Nil)
	if len(attempts) != 1 || attempts[0].Context.Target == nil ||
		attempts[0].Context.Target.Reference != searchdomain.AccessRolloutTargetReference(
			authorityID.String(),
		) {
		t.Fatalf("rollback command attempts for authority %s = %+v", authorityID, attempts)
	}

	for _, event := range events {
		if event.Context.Phase != audit.PhaseAttempt || event.Outcome != audit.OutcomeAttempted {
			t.Fatalf("rollback command recorded an execution result: %+v", event)
		}
	}
}
