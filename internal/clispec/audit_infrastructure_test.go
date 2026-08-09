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

type auditTestProbe struct {
	infrastructure audit.InfrastructureState
	err            error
	callCount      int
}

func (p *auditTestProbe) Check(context.Context) (audit.InfrastructureState, error) {
	p.callCount++
	return p.infrastructure, p.err
}

func TestRunAuditedInfrastructureDefersAfterAuthenticationFailureWhenLoginIsAbsent(t *testing.T) {
	outbox := &auditTestOutbox{writeErrors: []error{
		&pgconn.PgError{Code: "28000", Message: "role does not exist"},
	}}
	probe := &auditTestProbe{infrastructure: audit.InfrastructureState{OutboxTableExists: true}}
	runCount := 0
	eventsDuringRun := 0
	var runStartedAt time.Time
	err := runAudited(
		context.Background(),
		audit.Spec{Verb: "ops.provision", Mutates: true, CreatesAuditInfrastructure: true},
		testOperatorSource(), "tack-443-deploy-commit", true, outbox, probe.Check,
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
	for _, event := range outbox.events {
		gotCommit, ok := deployCommitOf(t, event)
		if !ok || gotCommit != "tack-443-deploy-commit" {
			t.Fatalf("deploy commit = %q, present = %t", gotCommit, ok)
		}
	}
}

func TestRunAuditedInfrastructureDefersAfterAuthenticationFailureWhenTableIsAbsent(t *testing.T) {
	outbox := &auditTestOutbox{writeErrors: []error{
		&pgconn.PgError{Code: "28000", Message: "role does not exist"},
	}}
	probe := &auditTestProbe{infrastructure: audit.InfrastructureState{OperatorLoginExists: true}}
	runCount := 0
	err := runAudited(
		context.Background(),
		audit.Spec{Verb: "ops.provision", Mutates: true, CreatesAuditInfrastructure: true},
		testOperatorSource(), "", true, outbox, probe.Check,
		func(context.Context) error {
			runCount++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("runAudited: %v", err)
	}
	if runCount != 1 || probe.callCount != 1 || len(outbox.events) != 2 {
		t.Fatalf("runs = %d, probes = %d, events = %d", runCount, probe.callCount, len(outbox.events))
	}
}

func TestRunAuditedInfrastructureRefusesWhenTableAndLoginExist(t *testing.T) {
	writeErr := &pgconn.PgError{Code: "42501", Message: "permission denied"}
	probe := &auditTestProbe{infrastructure: audit.InfrastructureState{
		OutboxTableExists: true, OperatorLoginExists: true,
	}}
	runCount := 0
	err := runAudited(
		context.Background(),
		audit.Spec{Verb: "ops.provision", Mutates: true, CreatesAuditInfrastructure: true},
		testOperatorSource(), "", true, &auditTestOutbox{writeErrors: []error{writeErr}}, probe.Check,
		func(context.Context) error {
			runCount++
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), writeErr.Message) {
		t.Fatalf("error = %v, want original write error", err)
	}
	if runCount != 0 || probe.callCount != 1 {
		t.Fatalf("runs = %d, probes = %d", runCount, probe.callCount)
	}
}

func TestRunAuditedInfrastructureRefusesWhenProbeFails(t *testing.T) {
	probe := &auditTestProbe{err: errors.New("probe unavailable")}
	runCount := 0
	err := runAudited(
		context.Background(),
		audit.Spec{Verb: "ops.provision", Mutates: true, CreatesAuditInfrastructure: true},
		testOperatorSource(), "", true, &auditTestOutbox{writeErrors: []error{errors.New("write failed")}}, probe.Check,
		func(context.Context) error {
			runCount++
			return nil
		},
	)
	if err == nil || runCount != 0 || probe.callCount != 1 {
		t.Fatalf("error = %v, runs = %d, probes = %d", err, runCount, probe.callCount)
	}
}

func TestRunAuditedInfrastructureRefusesWhenProbeIsUnwired(t *testing.T) {
	runCount := 0
	err := runAudited(
		context.Background(),
		audit.Spec{Verb: "ops.provision", Mutates: true, CreatesAuditInfrastructure: true},
		testOperatorSource(), "", true, &auditTestOutbox{writeErrors: []error{errors.New("write failed")}}, nil,
		func(context.Context) error {
			runCount++
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "probe is unwired") || runCount != 0 {
		t.Fatalf("error = %v, runs = %d", err, runCount)
	}
}

func TestRunAuditedWithoutInfrastructureNeverProbes(t *testing.T) {
	probe := &auditTestProbe{}
	runCount := 0
	err := runAudited(
		context.Background(), audit.Spec{Verb: "ops.test", Mutates: true}, testOperatorSource(), "", true,
		&auditTestOutbox{writeErrors: []error{errors.New("write failed")}}, probe.Check,
		func(context.Context) error {
			runCount++
			return nil
		},
	)
	if err == nil || runCount != 0 || probe.callCount != 0 {
		t.Fatalf("error = %v, runs = %d, probes = %d", err, runCount, probe.callCount)
	}
}

func TestRunAuditedHealthyOutboxNeverProbes(t *testing.T) {
	probe := &auditTestProbe{}
	outbox := &auditTestOutbox{}
	eventsDuringRun := 0
	err := runAudited(
		context.Background(),
		audit.Spec{Verb: "ops.provision", Mutates: true, CreatesAuditInfrastructure: true},
		testOperatorSource(), "", true, outbox, probe.Check,
		func(context.Context) error {
			eventsDuringRun = len(outbox.events)
			return nil
		},
	)
	if err != nil || eventsDuringRun != 1 || probe.callCount != 0 || len(outbox.events) != 2 {
		t.Fatalf("error = %v, events during run = %d, probes = %d, events = %d",
			err, eventsDuringRun, probe.callCount, len(outbox.events))
	}
	if outbox.events[0].Outcome != audit.OutcomePending || outbox.events[1].Outcome != audit.OutcomeOK {
		t.Fatalf("outcomes = %q, %q", outbox.events[0].Outcome, outbox.events[1].Outcome)
	}
}

func TestValidateAuditSpecRejectsInfrastructureRead(t *testing.T) {
	err := validateAuditSpec(audit.Spec{Verb: "ops.test", Reads: true, CreatesAuditInfrastructure: true})
	if err == nil || !strings.Contains(err.Error(), "cannot create audit infrastructure for a read") {
		t.Fatalf("validateAuditSpec error = %v", err)
	}
}
