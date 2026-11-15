package audit

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/testenv"
)

func TestOutboxReadBatchAndDelete(t *testing.T) {
	dsn := testenv.Ledger(t)

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}

	ctx := context.Background()
	events := []Event{
		outboxTestEvent("first"),
		outboxTestEvent("second"),
		outboxTestEvent("third"),
	}
	// ReadBatch sorts rows by creation time. This test uses old timestamps
	// to sort its events before rows from concurrent tests in the shared
	// database. The limited read below checks the first two events.
	createdAt := []time.Time{
		time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2000, time.January, 1, 0, 0, 1, 0, time.UTC),
		time.Date(2000, time.January, 1, 0, 0, 2, 0, time.UTC),
	}
	t.Cleanup(func() {
		for _, event := range events {
			_, _ = pool.Exec(
				ctx,
				`DELETE FROM public.ops_outbox WHERE event_id = $1`,
				event.EventID,
			)
		}

		pool.Close()
	})

	for i, event := range events {
		eventJSON, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal event %d: %v", i, err)
		}

		_, err = pool.Exec(
			ctx,
			`
			INSERT INTO public.ops_outbox (event_id, event, created_at)
			VALUES ($1, $2, $3)
		`,
			event.EventID,
			eventJSON,
			createdAt[i],
		)
		if err != nil {
			t.Fatalf("insert event %d: %v", i, err)
		}
	}

	// Later assertions filter rows by this test's event IDs because
	// concurrent tests can write other rows to the shared database.
	outbox := NewPoolOutbox(pool)
	batch, err := outbox.ReadBatch(ctx, 2)
	if err != nil {
		t.Fatalf("read limited batch: %v", err)
	}

	if len(batch) != 2 {
		t.Fatalf("limited batch length = %d, want 2", len(batch))
	}

	for i, row := range batch {
		if row.EventID != events[i].EventID {
			t.Errorf("limited batch event %d = %s, want %s", i, row.EventID, events[i].EventID)
		}

		if !reflect.DeepEqual(row.Event, events[i]) {
			t.Errorf("limited batch event %d = %#v, want %#v", i, row.Event, events[i])
		}
	}

	testOutboxRows := readOwnOutboxRows(ctx, t, outbox, events)
	if len(testOutboxRows) != len(events) {
		t.Fatalf("own rows found = %d, want %d", len(testOutboxRows), len(events))
	}

	for i, row := range testOutboxRows {
		if !reflect.DeepEqual(row.Event, events[i]) {
			t.Errorf("own row %d = %#v, want %#v", i, row.Event, events[i])
		}
	}

	if err := outbox.Delete(ctx, events[1].EventID); err != nil {
		t.Fatalf("delete second event: %v", err)
	}

	remainingOutboxRows := readOwnOutboxRows(ctx, t, outbox, events)
	if len(remainingOutboxRows) != 2 {
		t.Fatalf("own rows after delete = %d, want 2", len(remainingOutboxRows))
	}

	if remainingOutboxRows[0].EventID != events[0].EventID ||
		remainingOutboxRows[1].EventID != events[2].EventID {
		t.Fatalf(
			"remaining event IDs = %s, %s, want %s, %s",
			remainingOutboxRows[0].EventID,
			remainingOutboxRows[1].EventID,
			events[0].EventID,
			events[2].EventID,
		)
	}
}

// outboxReadLimit limits the rows read from the shared database before
// readOwnOutboxRows filters them by this test's event IDs.
const outboxReadLimit = 1000

// readOwnOutboxRows reads a batch and selects rows with this test's event IDs
// without changing their order.
func readOwnOutboxRows(
	ctx context.Context,
	t *testing.T,
	outbox *PoolOutbox,
	events []Event,
) []OutboxRow {
	t.Helper()
	rows, err := outbox.ReadBatch(ctx, outboxReadLimit)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}

	wanted := make(map[uuid.UUID]bool, len(events))
	for _, event := range events {
		wanted[event.EventID] = true
	}

	found := make([]OutboxRow, 0, len(events))

	for _, row := range rows {
		if wanted[row.EventID] {
			found = append(found, row)
		}
	}

	return found
}

func outboxTestEvent(name string) Event {
	return Event{
		EventID: uuid.Must(uuid.NewV7()),
		Verb:    "ops.test",
		Actor: Actor{
			Type: ActorOperator,
			ID:   uuid.Must(uuid.NewV7()),
		},
		Entity: Entity{
			Type: "ops",
			ID:   uuid.Must(uuid.NewV7()),
			Name: name,
		},
		Context: EventContext{
			OrgID:  uuid.Must(uuid.NewV7()),
			Source: SourceSystem,
		},
		Outcome:    OutcomeOK,
		OccurredAt: time.Date(2026, time.June, 8, 12, 0, 0, 0, time.UTC),
	}
}
