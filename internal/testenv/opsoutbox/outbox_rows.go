// Package opsoutbox reads the operator outbox, public.ops_outbox, for tests.
// It sits outside package testenv because the audit package's own tests
// import testenv, and an audit import there would form an import cycle.
package opsoutbox

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// eventsStatement reads the events that match a [Filter]. Each condition
// passes every row when its parameter is empty: $1 is the verb, $2 and $3 are
// the JSON path and its value, and $4 is the creation-time lower bound.
const eventsStatement = `
	SELECT event FROM public.ops_outbox
	 WHERE ($1::text = '' OR event->>'verb' = $1::text)
	   AND (coalesce(cardinality($2::text[]), 0) = 0 OR event #>> $2::text[] = $3::text)
	   AND ($4::timestamptz IS NULL OR created_at >= $4::timestamptz)
	 ORDER BY created_at, event_id`

// Filter selects public.ops_outbox events. A zero field adds no condition.
type Filter struct {
	// Verb matches the event's verb.
	Verb audit.Verb `exhaustruct:"optional"`
	// Path and Value match the event text at the JSON path Path. An empty
	// Path adds no condition.
	Path  []string `exhaustruct:"optional"`
	Value string   `exhaustruct:"optional"`
	// Since matches events created at or after it.
	Since time.Time `exhaustruct:"optional"`
}

// Events reads the public.ops_outbox events that match filter, ordered by
// creation time and event ID. Any read or decode error fails the test.
func Events(t testenv.T, pool *pgxpool.Pool, filter Filter) []audit.Event {
	t.Helper()
	var since *time.Time
	if !filter.Since.IsZero() {
		since = &filter.Since
	}
	rows, err := pool.Query(t.Context(), eventsStatement, string(filter.Verb), filter.Path, filter.Value, since)
	if err != nil {
		failRead(t, "query", err)
	}
	encoded, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		failRead(t, "collect rows", err)
	}
	events := make([]audit.Event, 0, len(encoded))
	for _, body := range encoded {
		var event audit.Event
		if err := json.Unmarshal(body, &event); err != nil {
			_, _ = fmt.Fprintf(t.Output(), "opsoutbox: decode the event %s: %v\n", body, err)
			t.FailNow()
		}
		events = append(events, event)
	}
	return events
}

// failRead writes the failed step of a public.ops_outbox read and fails the
// test.
func failRead(t testenv.T, step string, err error) {
	t.Helper()
	_, _ = fmt.Fprintf(t.Output(), "opsoutbox: read public.ops_outbox (%s): %v\n", step, err)
	t.FailNow()
}
