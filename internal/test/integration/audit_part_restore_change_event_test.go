package integration_test

import (
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/testenv"
)

const partRestoreSyntheticHeader = `{"synthetic":"change event"}`

type changeEventRefusal struct {
	name     string
	mutate   func(call *partRestoreCall)
	fragment string
}

func changeEventRefusals() []changeEventRefusal {
	return []changeEventRefusal{
		{
			name: "synthetic header with no parts",
			mutate: func(c *partRestoreCall) {
				c.header, c.parts = []byte(partRestoreSyntheticHeader), [][]byte{}
			},
			fragment: "field in event that the consumer does not decode",
		},
		{
			name:     "another change ID",
			mutate:   func(c *partRestoreCall) { c.changeID = uuid.Must(uuid.NewV7()) },
			fragment: "change event ID is not the derived change ID",
		},
		{
			name:     "part messages with a single-record header",
			mutate:   func(c *partRestoreCall) { c.parts = [][]byte{[]byte(partRestoreSyntheticHeader)} },
			fragment: "single-record change event includes part messages",
		},
	}
}

func TestAuditPartRestoreRefusesUnrelatedChangeEvents(t *testing.T) {
	admin := ledgerAdminPool(t)
	operator := rolePool(t, testenv.Ledger(t), "audit_operator")
	for _, current := range changeEventRefusals() {
		t.Run(current.name, func(t *testing.T) {
			fixture := newPartRestoreRawFixture(t, admin)
			call := fixture.call
			current.mutate(&call)
			before := readPartRestoreCounts(t, admin, call.changeID)
			requirePartRestoreRefusal(t, call.run(t, operator), current.fragment)
			if after := readPartRestoreCounts(t, admin, call.changeID); after != before {
				t.Fatalf("counts after a refusal = %+v, want %+v (no part row and no outbox row)", after, before)
			}
		})
	}
}
