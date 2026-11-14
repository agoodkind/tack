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

// auditTestOutbox represents ops_outbox and its unique event ID constraint.
// Rejecting duplicate IDs detects lost outcome evidence when an outcome reuses
// the intent's ID.
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
		return fmt.Errorf(
			"duplicate key value violates unique constraint on event_id %s",
			event.EventID,
		)
	}

	o.seen[event.EventID] = true
	o.events = append(o.events, event)

	return nil
}

// opIDOf reads the correlation ID from the event's Extra field.
func opIDOf(t *testing.T, event audit.Event) uuid.UUID {
	t.Helper()
	var extra operatorEventExtra
	if err := json.Unmarshal(event.Extra, &extra); err != nil {
		t.Fatalf("decode event extra %s: %v", event.Extra, err)
	}

	return extra.OpID
}

func testOperatorSource() audit.OperatorIdentitySource {
	return auditTestSource{
		principal: audit.OperatorPrincipal{
			ID:     uuid.MustParse("019dd226-440e-729a-a442-281aaf73ca30"),
			Email:  "operator@example.com",
			Name:   "Operator User",
			Source: "test",
		},
	}
}

func TestRunAuditedMutationRecordsAttempt(t *testing.T) {
	for _, createsInfrastructure := range []bool{false, true} {
		t.Run(
			fmt.Sprintf("creates infrastructure %t", createsInfrastructure),
			func(t *testing.T) {
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
					"",
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

				if len(outbox.events) != 1 {
					t.Fatalf("outbox events = %d, want 1", len(outbox.events))
				}

				if outbox.events[0].Outcome != audit.OutcomeAttempted {
					t.Fatalf(
						"attempt outcome = %q, want %q",
						outbox.events[0].Outcome,
						audit.OutcomeAttempted,
					)
				}

				if opIDOf(t, outbox.events[0]) == uuid.Nil {
					t.Fatal("attempt has no operation id")
				}
			},
		)
	}
}

func TestRunAuditedMutationIntentFailureNeverRuns(t *testing.T) {
	outbox := &auditTestOutbox{writeErrors: []error{errors.New("outbox write failed")}}
	runCount := 0
	err := runAudited(
		context.Background(),
		audit.Spec{Verb: "ops.test", Mutates: true},
		testOperatorSource(),
		"",
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

// TestRunAuditedAtomicIsRejectedUntilSupported checks that unsupported Atomic
// commands cannot run with ordinary outbox recording in place of transactional evidence.
func TestRunAuditedAtomicIsRejectedUntilSupported(t *testing.T) {
	runCount := 0
	err := runAudited(
		context.Background(),
		audit.Spec{Verb: "ops.atomic", Mutates: true, Atomic: true},
		testOperatorSource(),
		"",
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
