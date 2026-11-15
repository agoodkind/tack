package integration

import (
	"encoding/json"
	"testing"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
)

// modelRepairOutboxPage is the number of outbox entries that one read returns.
const modelRepairOutboxPage = 500

// ModelRepairEvents returns the position after the last entry of the
// FoundationDB operator outbox and the events after mark that the model
// repair service recorded. A nil mark reads from the first entry.
func ModelRepairEvents(
	t *testing.T,
	stores *fdbadapter.Stores,
	mark []byte,
) ([]byte, []audit.Event) {
	t.Helper()
	var events []audit.Event

	for {
		entries, err := stores.OpsOutbox.ReadOutboxFrom(
			WithTestOperation(t, t.Context()),
			mark,
			modelRepairOutboxPage,
		)
		if err != nil {
			t.Fatalf("reading the FoundationDB operator outbox failed: %v", err)
		}

		if len(entries) == 0 {
			return mark, events
		}

		for _, entry := range entries {
			var event audit.Event
			if json.Unmarshal(entry.Event, &event) == nil &&
				event.Verb == string(audit.VerbSearchModelRepair) {
				events = append(events, event)
			}
		}

		mark = entries[len(entries)-1].Mark
	}
}
