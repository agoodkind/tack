package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/user"
)

// refusingOutbox is a ledger that cannot be written, which is the case an
// issue must not survive: a token nobody can account for.
type refusingOutbox struct{}

func (refusingOutbox) WriteOutbox(context.Context, audit.Event) error {
	return errors.New("ledger unreachable")
}

// outcomeRefusingOutbox accepts the intent row and refuses the ok row, which
// is the window where a token exists that the ledger has not confirmed.
type outcomeRefusingOutbox struct {
	inner audit.OutboxWriter
}

func (o outcomeRefusingOutbox) WriteOutbox(ctx context.Context, event audit.Event) error {
	if event.Outcome == audit.OutcomeOK {
		return errors.New("ledger unreachable after the write")
	}
	return o.inner.WriteOutbox(ctx, event)
}

// cancelAfterIntentOutbox stores the intent row and then cancels the
// attempt's context, so the write that follows the intent fails: the shape
// of a command killed between its intent and its work.
type cancelAfterIntentOutbox struct {
	inner  audit.OutboxWriter
	cancel context.CancelFunc
}

func (o cancelAfterIntentOutbox) WriteOutbox(ctx context.Context, event audit.Event) error {
	err := o.inner.WriteOutbox(ctx, event)
	if event.Outcome == audit.OutcomePending {
		o.cancel()
	}
	return err
}

func authTokenTestUser(t *testing.T, env *TestEnv) *user.User {
	t.Helper()
	now := time.Now().UTC()
	created, err := postgres.NewUserRepo(env.Ops.Pool).Create(env.Ctx, &user.User{
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

// pendingRowsForHolder counts the pending issue rows naming one holder and
// removes them after the test.
func pendingRowsForHolder(t *testing.T, env *TestEnv, email string) int {
	t.Helper()
	rows, err := env.Ops.Pool.Query(env.Ctx, `
		SELECT event_id FROM public.ops_outbox
		 WHERE event->>'verb' = $1 AND event->'entity'->>'identifier' = $2 AND event->>'outcome' = $3`,
		string(audit.VerbAuthTokenCreate), email, string(audit.OutcomePending))
	if err != nil {
		t.Fatalf("read pending rows: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var eventID uuid.UUID
		if err := rows.Scan(&eventID); err != nil {
			t.Fatalf("scan pending row: %v", err)
		}
		count++
		t.Cleanup(func() {
			_, _ = env.Ops.Pool.Exec(context.Background(), `DELETE FROM public.ops_outbox WHERE event_id = $1`, eventID)
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read pending rows: %v", err)
	}
	return count
}

func countOutcomes(rows []audit.Event) map[audit.Outcome]int {
	counts := make(map[audit.Outcome]int)
	for _, row := range rows {
		counts[row.Outcome]++
	}
	return counts
}

func requireIntentAndOutcome(t *testing.T, rows []audit.Event, principal audit.OperatorPrincipal, what string) audit.Event {
	t.Helper()
	counts := countOutcomes(rows)
	if counts[audit.OutcomePending] != 1 || counts[audit.OutcomeOK] != 1 {
		t.Fatalf("%s rows by outcome = %v, want one pending and one ok", what, counts)
	}
	var confirmed audit.Event
	for _, row := range rows {
		if row.Actor.ID != principal.ID {
			t.Fatalf("%s row names actor %s, want operator %s", what, row.Actor.ID, principal.ID)
		}
		if row.Outcome == audit.OutcomeOK {
			confirmed = row
		}
	}
	return confirmed
}
