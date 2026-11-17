package integration_test

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

const (
	changeTokenLabel  = "runtime-audit-command-change"
	commandMemberRole = 15
)

// TestRuntimeAuditCommandChanges runs operator commands that change SQL and
// FoundationDB records through the clispec command gate. Each change event
// must use the operation ID that the gate records in the command intent and
// must have the operator as its actor.
func TestRuntimeAuditCommandChanges(t *testing.T) {
	t.Run("sql_token_create", testCommandTokenChange)
	t.Run("foundationdb_default_children", testCommandNodeChange)
	t.Run("foundationdb_reference_repair", testCommandReferenceRepair)
}

func testCommandTokenChange(t *testing.T) {
	pool := ledgerAdminPool(t)
	holder := createSQLAuditUser(t, pool, sqlAuditPrincipal())
	operatorID := uuid.Must(uuid.NewV7())
	cfg := &config.Config{DatabaseURL: testenv.Ledger(t)}
	if output, err := runOpsCommand(
		t,
		cfg,
		pool,
		"--execute",
		"--operator-id",
		operatorID.String(),
		"--operator-email",
		outcomeOperatorEmail,
		"ops",
		"auth",
		"token-create",
		"--email",
		holder.Email,
		"--label",
		changeTokenLabel,
	); err != nil {
		t.Fatalf("token-create failed: %v\n%s", err, output)
	}

	commandID := commandOperationID(
		t,
		outboxEventsByQuery(
			t,
			pool,
			"runtime_audit_verb_outbox.sql",
			string(audit.VerbOpsAuthTokenCreate),
			operatorID.String(),
		),
	)
	changes := 0

	for _, event := range sqlOutboxEvents(t, pool, commandID) {
		if event.Context.Phase != audit.PhaseChange {
			continue
		}

		requireChangeEvent(t, event, commandID, operatorID)
		changes++
	}

	if changes != 1 {
		t.Fatalf(
			"token-create recorded %d change events with operation %s instead of 1",
			changes,
			commandID,
		)
	}
}

func commandOperationID(t *testing.T, events []audit.Event) uuid.UUID {
	t.Helper()
	var intents []audit.Event

	for _, event := range events {
		if event.Outcome == audit.OutcomeAttempted && event.Context.ParentOperationID == uuid.Nil {
			intents = append(intents, event)
		}
	}

	if len(intents) != 1 {
		t.Fatalf("the outbox has %d command intents instead of 1", len(intents))
	}

	operationID := decodeCommandExtra(t, intents[0]).OpID
	if operationID == uuid.Nil {
		t.Fatalf("command intent %s does not record an operation ID", intents[0].EventID)
	}

	return operationID
}
