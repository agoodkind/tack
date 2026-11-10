package integration_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
)

// readOutboxEvents returns the public.ops_outbox events of verb by actor in
// creation order. It joins the parts of each manifest record before it
// decodes the event.
func readOutboxEvents(t *testing.T, pool *pgxpool.Pool, verb audit.Verb, actor uuid.UUID) []audit.Event {
	t.Helper()
	rows, err := pool.Query(t.Context(), readTestSQL(t, "runtime_audit_outbox_event_ids.sql"), string(verb), actor.String())
	if err != nil {
		t.Fatalf("reading the outbox event IDs failed: %v", err)
	}
	eventIDs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		t.Fatalf("scanning the outbox event IDs failed: %v", err)
	}
	var selected []audit.Event
	for _, eventID := range eventIDs {
		for _, event := range outboxEvents(t, yugabyteOutboxRecords(t, pool, eventID), eventID) {
			if event.Verb == string(verb) && event.Actor.ID == actor {
				selected = append(selected, event)
			}
		}
	}
	return selected
}
