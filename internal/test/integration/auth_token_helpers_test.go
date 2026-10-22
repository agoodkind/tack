package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/user"
)

func testChangeOperation(verb audit.Verb, orgID uuid.UUID) audit.Event {
	principal := authTokenTestPrincipal()
	return postgres.NewChangeOperation(verb, postgres.OperatorActor(principal), orgID, audit.SourceSystem)
}

func authTokenTestUser(t *testing.T, env *TestEnv) *user.User {
	t.Helper()
	now := time.Now().UTC()
	operation := testChangeOperation(audit.VerbUserCreate, audit.SystemOrgID())
	created, err := postgres.NewUserRepo(env.Ops.Pool).Create(env.Ctx, operation, &user.User{
		ID: uuid.Must(uuid.NewV7()), Email: "token-" + uuid.NewString() + "@example.invalid",
		DisplayName: "Token Holder", AvatarURL: nil, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create the token holder: %v", err)
	}
	return created
}

func authTokenTestPrincipal() audit.OperatorPrincipal {
	return audit.OperatorPrincipal{
		ID: uuid.Must(uuid.NewV7()), Email: "operator@example.invalid", Name: "Operator", Source: "test",
	}
}

// outboxRowsFor reads every outbox row for one token and verb straight from
// the table by the event's own fields, so rows other tests left behind cannot
// hide the ones this test wrote. It removes them after the test.
func outboxRowsFor(t *testing.T, env *TestEnv, verb audit.Verb, tokenID uuid.UUID) []audit.Event {
	t.Helper()
	rows, err := env.Ops.Pool.Query(env.Ctx, `
		SELECT event_id, event FROM public.ops_outbox
		 WHERE event->>'verb' = $1 AND event->'entity'->>'id' = $2`,
		string(verb), tokenID.String())
	if err != nil {
		t.Fatalf("read the outbox for token %s: %v", tokenID, err)
	}
	defer rows.Close()
	found := make([]audit.Event, 0)
	for rows.Next() {
		var eventID uuid.UUID
		var raw []byte
		if err := rows.Scan(&eventID, &raw); err != nil {
			t.Fatalf("scan an outbox row: %v", err)
		}
		var event audit.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("decode an outbox row: %v", err)
		}
		found = append(found, event)
		t.Cleanup(func() {
			_, _ = env.Ops.Pool.Exec(context.Background(), `DELETE FROM public.ops_outbox WHERE event_id = $1`, eventID)
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read outbox rows: %v", err)
	}
	return found
}

func requireOneChange(t *testing.T, rows []audit.Event, principal audit.OperatorPrincipal) audit.Event {
	t.Helper()
	if len(rows) != 1 {
		t.Fatalf("outbox rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Context.Phase != audit.PhaseChange || row.Outcome != audit.OutcomeCompleted || row.Actor.ID != principal.ID {
		t.Fatalf("outbox row phase %s, outcome %s, actor %s; want a completed change by %s",
			row.Context.Phase, row.Outcome, row.Actor.ID, principal.ID)
	}
	return row
}
