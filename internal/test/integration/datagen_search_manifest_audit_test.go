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
	type operation struct {
		pending, successful, failed int
	}
	operations := make(map[uuid.UUID]operation)
	eventIDs := make(map[uuid.UUID]bool)
	for _, row := range rows {
		event := row.Event
		if event.Verb != string(audit.VerbOpsDatagenSearch) {
			continue
		}
		if event.Context.OrgID != audit.SystemOrgID() || event.Entity.Type != "system" || event.Entity.ID != audit.SystemOrgID() || event.Actor.ID != uuid.MustParse("019dd226-440e-729a-a442-281aaf73ca30") {
			t.Fatalf("preparation audit has an unexpected operator or resource: %+v", event)
		}
		if eventIDs[row.EventID] || row.EventID != event.EventID {
			t.Fatalf("preparation audit repeats or changes event identity %s", row.EventID)
		}
		eventIDs[row.EventID] = true
		var extra struct {
			OperationID uuid.UUID `json:"op_id"`
		}
		if err := json.Unmarshal(event.Extra, &extra); err != nil || extra.OperationID == uuid.Nil {
			t.Fatalf("preparation audit omits operation identity: %v", err)
		}
		pair := operations[extra.OperationID]
		switch event.Outcome {
		case audit.OutcomePending:
			pair.pending++
		case audit.OutcomeOK:
			pair.successful++
		case audit.OutcomeError:
			if event.Error == nil || event.Error.Code != "command_failed" {
				t.Fatal("preparation refusal audit omits the command error")
			}
			pair.failed++
		default:
			t.Fatalf("preparation audit has unexpected outcome %s", event.Outcome)
		}
		operations[extra.OperationID] = pair
	}
	if len(operations) != 6 || len(eventIDs) != 12 {
		t.Fatalf("preparation audit has %d operations and %d events, want six and twelve", len(operations), len(eventIDs))
	}
	var successful, failed int
	for identifier, pair := range operations {
		if pair.pending != 1 || pair.successful+pair.failed != 1 {
			t.Fatalf("preparation operation %s has unmatched intent/outcome: %+v", identifier, pair)
		}
		successful += pair.successful
		failed += pair.failed
	}
	if successful != 1 || failed != 5 {
		t.Fatalf("preparation audit has %d successes and %d refusals, want one and five", successful, failed)
	}
	t.Log("preparation audit verified six operations with matched pending intents and one OK/five error outcomes")
}
