package integration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/audit"
)

func requirePreparationAudit(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	rows, err := audit.NewPoolOutbox(pool).ReadBatch(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}

	operations := make(map[uuid.UUID]bool)
	eventIDs := make(map[uuid.UUID]bool)
	for _, row := range rows {
		event := row.Event
		if event.Verb != string(audit.VerbOpsDatagenSearch) {
			continue
		}

		if event.Context.OrgID != audit.SystemOrgID() || event.Entity.Type != "system" || event.Entity.ID != audit.SystemOrgID() || event.Actor.ID != uuid.MustParse(
			"019dd226-440e-729a-a442-281aaf73ca30",
		) {
			t.Fatalf("preparation audit has an unexpected operator or resource: %+v", event)
		}

		if row.EventID == uuid.Nil || eventIDs[row.EventID] || row.EventID != event.EventID {
			t.Fatalf("preparation audit repeats or changes event identity %s", row.EventID)
		}

		eventIDs[row.EventID] = true

		var extra struct {
			OperationID uuid.UUID `json:"op_id"`
		}
		if err := json.Unmarshal(event.Extra, &extra); err != nil || extra.OperationID == uuid.Nil {
			t.Fatalf("preparation audit omits operation identity: %v", err)
		}

		if event.Outcome != audit.OutcomeAttempted || event.Error != nil {
			t.Fatalf(
				"preparation operation %s recorded outcome %s and error %+v",
				extra.OperationID,
				event.Outcome,
				event.Error,
			)
		}

		if operations[extra.OperationID] {
			t.Fatalf("preparation audit repeats operation %s", extra.OperationID)
		}

		operations[extra.OperationID] = true
	}

	if len(operations) != 6 || len(eventIDs) != 6 {
		t.Fatalf(
			"preparation audit has %d operations and %d events, want six of each",
			len(operations),
			len(eventIDs),
		)
	}
}
