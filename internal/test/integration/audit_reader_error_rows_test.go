package integration_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// requireReaderErrorRows polls the ledger until it finds the expected rows for
// the operator and checks their text.
func requireReaderErrorRows(
	t *testing.T,
	ledger *nodeAuditLedger,
	current readerErrorDSN,
	operatorID uuid.UUID,
	command readerErrorCommand,
	output string,
) {
	t.Helper()
	now := clock.Now()
	filter := audit.QueryFilter{
		OrgID:     audit.SystemOrgID(),
		Oldest:    now.Add(-time.Hour),
		Latest:    now.Add(time.Hour),
		Action:    "",
		ActorID:   operatorID,
		EntityID:  uuid.Nil,
		RequestID: "",
		TraceID:   "",
		Limit:     command.rows + 1,
	}
	deadline := clock.Now().Add(nodeAuditDeadline)

	for {
		rows, err := ledger.reader.Query(t.Context(), filter)
		if err != nil {
			t.Fatalf("query the rows of operator %s: %v", operatorID, err)
		}

		if len(rows) >= command.rows {
			requireReaderErrorRowText(t, current, command, rows)
			return
		}

		if clock.Now().After(deadline) {
			t.Fatalf(
				"%v with the %s DSN stored %d rows after %s, want %d\n%s",
				command.args,
				current.name,
				len(rows),
				nodeAuditDeadline,
				command.rows,
				output,
			)
		}

		time.Sleep(nodeAuditPollInterval)
	}
}

// requireReaderErrorRowText fails the test if a row contains forbidden text or
// does not record a required failure.
func requireReaderErrorRowText(
	t *testing.T,
	current readerErrorDSN,
	command readerErrorCommand,
	rows []audit.Row,
) {
	t.Helper()
	failureStored := false

	for _, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("encode row %s: %v", row.EventID, err)
		}

		for _, forbidden := range current.forbidden {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf(
					"%v with the %s DSN stored %q in row %s:\n%s",
					command.args,
					current.name,
					forbidden,
					row.EventID,
					encoded,
				)
			}
		}

		if row.Error != nil && strings.Contains(row.Error.Message, current.required) {
			failureStored = true
		}
	}

	if command.storesFail && !failureStored {
		t.Fatalf(
			"%v with the %s DSN stored rows %+v, want a failure that contains %q",
			command.args,
			current.name,
			rows,
			current.required,
		)
	}
}
