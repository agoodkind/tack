package clispec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

type auditTestSource struct {
	principal audit.OperatorPrincipal
	err       error
}

func (s auditTestSource) Resolve(context.Context) (audit.OperatorPrincipal, error) {
	return s.principal, s.err
}

// auditTestOutbox stands in for the ops_outbox table, including its primary
// key on event id. Accepting a duplicate id here would hide the real failure:
// the table rejects the second row, so a pair of rows sharing an id loses the
// outcome.
type auditTestOutbox struct {
	events      []audit.Event
	seen        map[uuid.UUID]bool
	writeErrors []error
	callNum     int
}

func (o *auditTestOutbox) WriteOutbox(_ context.Context, event audit.Event) error {
	callNum := o.callNum
	o.callNum++
	if callNum < len(o.writeErrors) && o.writeErrors[callNum] != nil {
		return fmt.Errorf("write test outbox: %w", o.writeErrors[callNum])
	}
	if o.seen == nil {
		o.seen = map[uuid.UUID]bool{}
	}
	if o.seen[event.EventID] {
		return fmt.Errorf("duplicate key value violates unique constraint on event_id %s", event.EventID)
	}
	o.seen[event.EventID] = true
	o.events = append(o.events, event)
	return nil
}

// opIDOf reads the correlation id an event carries in Extra.
func opIDOf(t *testing.T, event audit.Event) uuid.UUID {
	t.Helper()
	var extra operatorEventExtra
	if err := json.Unmarshal(event.Extra, &extra); err != nil {
		t.Fatalf("decode event extra %s: %v", event.Extra, err)
	}
	return extra.OpID
}

func testOperatorSource() audit.OperatorIdentitySource {
	return auditTestSource{principal: audit.OperatorPrincipal{
		ID:     uuid.MustParse("019dd226-440e-729a-a442-281aaf73ca30"),
		Email:  "operator@example.com",
		Name:   "Operator User",
		Source: "test",
	}}
}

func TestRunAuditedMutationRecordsIntentThenOutcome(t *testing.T) {
	for _, createsInfrastructure := range []bool{false, true} {
		t.Run(fmt.Sprintf("creates infrastructure %t", createsInfrastructure), func(t *testing.T) {
			outbox := &auditTestOutbox{}
			runCount := 0
			err := runAudited(
				context.Background(),
				audit.Spec{
					Verb:                       "ops.test",
					Mutates:                    true,
					CreatesAuditInfrastructure: createsInfrastructure,
				},
				testOperatorSource(),
				true,
				outbox,
				nil,
				func(context.Context) error {
					runCount++
					return nil
				},
			)
			if err != nil {
				t.Fatalf("runAudited: %v", err)
			}
			if runCount != 1 {
				t.Fatalf("run count = %d, want 1", runCount)
			}
			if len(outbox.events) != 2 {
				t.Fatalf("outbox events = %d, want 2", len(outbox.events))
			}
			if outbox.events[0].Outcome != audit.OutcomePending {
				t.Fatalf("intent outcome = %q, want %q", outbox.events[0].Outcome, audit.OutcomePending)
			}
			if outbox.events[1].Outcome != audit.OutcomeOK {
				t.Fatalf("result outcome = %q, want %q", outbox.events[1].Outcome, audit.OutcomeOK)
			}
			if opIDOf(t, outbox.events[0]) != opIDOf(t, outbox.events[1]) {
				t.Fatalf("operation ids differ: %s and %s",
					opIDOf(t, outbox.events[0]), opIDOf(t, outbox.events[1]))
			}
			if outbox.events[0].EventID == outbox.events[1].EventID {
				t.Fatalf("intent and outcome share event id %s", outbox.events[0].EventID)
			}
		})
	}
}

// TestRunAuditedMutationOutcomeSurvivesOutboxKey pins that both rows actually
// land in an outbox that enforces its primary key. Without this the pair can
// share an event id, the second insert is rejected, and the command reports a
// failure while the ledger keeps only a pending intent.
func TestRunAuditedMutationOutcomeSurvivesOutboxKey(t *testing.T) {
	outbox := &auditTestOutbox{seen: map[uuid.UUID]bool{}}
	err := runAudited(
		context.Background(),
		audit.Spec{Verb: "ops.test.keyed", Mutates: true},
		testOperatorSource(),
		true,
		outbox,
		nil,
		func(context.Context) error { return nil },
	)
	if err != nil {
		t.Fatalf("runAudited: %v", err)
	}
	if len(outbox.events) != 2 {
		t.Fatalf("outbox events = %d, want 2", len(outbox.events))
	}
}

func TestRunAuditedMutationIntentFailureNeverRuns(t *testing.T) {
	outbox := &auditTestOutbox{writeErrors: []error{errors.New("outbox write failed")}}
	runCount := 0
	err := runAudited(
		context.Background(),
		audit.Spec{Verb: "ops.test", Mutates: true},
		testOperatorSource(),
		true,
		outbox,
		nil,
		func(context.Context) error {
			runCount++
			return nil
		},
	)
	if err == nil {
		t.Fatal("runAudited succeeded after intent failure")
	}
	if runCount != 0 {
		t.Fatalf("run count = %d, want 0", runCount)
	}
	if len(outbox.events) != 0 {
		t.Fatalf("outbox events = %d, want 0", len(outbox.events))
	}
}

// TestRunAuditedAtomicIsRejectedUntilSupported pins that a command declaring
// Atomic refuses to run rather than silently falling through to the ordinary
// outbox path. The verification that an atomic command really did record
// inside its own transaction lands with the first such command; until then,
// accepting the flag would hand back a guarantee nothing enforces.
func TestRunAuditedAtomicIsRejectedUntilSupported(t *testing.T) {
	runCount := 0
	err := runAudited(
		context.Background(),
		audit.Spec{Verb: "ops.atomic", Mutates: true, Atomic: true},
		testOperatorSource(),
		true,
		&auditTestOutbox{},
		nil,
		func(context.Context) error {
			runCount++
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "not supported yet") {
		t.Fatalf("error = %v, want atomic rejected", err)
	}
	if runCount != 0 {
		t.Fatalf("run count = %d, want 0", runCount)
	}
}
