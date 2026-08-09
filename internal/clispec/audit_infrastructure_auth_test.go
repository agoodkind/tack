package clispec

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

func TestRunAuditedInfrastructureDefersAfterAuthenticationFailureWhenBothExist(t *testing.T) {
	outbox := &auditTestOutbox{writeErrors: []error{
		&pgconn.PgError{Code: "28P01", Message: "password authentication failed"},
	}}
	probe := &auditTestProbe{infrastructure: audit.InfrastructureState{
		OutboxTableExists: true, OperatorLoginExists: true,
	}}
	runCount := 0
	eventsDuringRun := 0
	var runStartedAt time.Time
	err := runAudited(
		context.Background(),
		audit.Spec{Verb: "ops.provision", Mutates: true, CreatesAuditInfrastructure: true},
		testOperatorSource(), true, outbox, probe.Check,
		func(context.Context) error {
			runCount++
			eventsDuringRun = len(outbox.events)
			runStartedAt = clock.Now().UTC()
			return nil
		},
	)
	if err != nil {
		t.Fatalf("runAudited: %v", err)
	}
	if runCount != 1 || eventsDuringRun != 0 || probe.callCount != 1 || len(outbox.events) != 2 {
		t.Fatalf("runs = %d, events during run = %d, probes = %d, events = %d",
			runCount, eventsDuringRun, probe.callCount, len(outbox.events))
	}
	if outbox.events[0].Outcome != audit.OutcomePending || outbox.events[1].Outcome != audit.OutcomeOK {
		t.Fatalf("outcomes = %q, %q", outbox.events[0].Outcome, outbox.events[1].Outcome)
	}
	var extra operatorEventExtra
	if err := json.Unmarshal(outbox.events[0].Extra, &extra); err != nil {
		t.Fatalf("decode intent extra: %v", err)
	}
	if extra.StartedAt == nil || extra.StartedAt.After(runStartedAt) {
		t.Fatalf("intent start = %v, run start = %s", extra.StartedAt, runStartedAt)
	}
}

func TestRunAuditedInfrastructureRefusesPlainErrorWhenTableAndLoginExist(t *testing.T) {
	writeErr := errors.New("write failed")
	probe := &auditTestProbe{infrastructure: audit.InfrastructureState{
		OutboxTableExists: true, OperatorLoginExists: true,
	}}
	runCount := 0
	err := runAudited(
		context.Background(),
		audit.Spec{Verb: "ops.provision", Mutates: true, CreatesAuditInfrastructure: true},
		testOperatorSource(), true, &auditTestOutbox{writeErrors: []error{writeErr}}, probe.Check,
		func(context.Context) error {
			runCount++
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), writeErr.Error()) {
		t.Fatalf("error = %v, want original write error", err)
	}
	if runCount != 0 || probe.callCount != 1 {
		t.Fatalf("runs = %d, probes = %d", runCount, probe.callCount)
	}
}
