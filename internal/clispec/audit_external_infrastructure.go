package clispec

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
)

// deferredOutbox keeps the events of an external command in memory while the
// command creates the audit infrastructure that stores them. Only the
// first-time setup exception uses it.
type deferredOutbox struct {
	events []audit.Event
}

// WriteOutbox appends event to the deferred events.
func (o *deferredOutbox) WriteOutbox(_ context.Context, event audit.Event) error {
	o.events = append(o.events, event)
	return nil
}

// runExternalCreatingAuditInfrastructure handles an external command that
// creates the audit infrastructure after outbox did not save its intent. It
// does not run the command when the probe is missing or fails, or when the
// outbox table and the operator login exist and intentErr is not an
// authentication failure. A probe failure returns the probe error joined with
// intentErr. Otherwise it runs the command with every
// event in memory and then writes the events to outbox in order.
func runExternalCreatingAuditInfrastructure(
	ctx context.Context,
	runContext context.Context,
	outbox audit.OutboxWriter,
	probe audit.InfrastructureProbe,
	intent audit.Event,
	run ExternalRun,
	intentErr error,
) error {
	if probe == nil {
		slog.ErrorContext(ctx, "cli.audit_infrastructure_probe_unwired",
			slog.String("operation", intent.Verb), slog.String("err", intentErr.Error()))
		return fmt.Errorf("audit infrastructure probe is unwired: %w", intentErr)
	}
	infrastructure, probeErr := probe(ctx)
	if probeErr != nil {
		slog.ErrorContext(ctx, "cli.audit_infrastructure_probe_failed", slog.String("err", probeErr.Error()))
		return fmt.Errorf("probe the audit infrastructure: %w", errors.Join(probeErr, intentErr))
	}
	if infrastructure.OutboxTableExists && infrastructure.OperatorLoginExists && !isAuthenticationFailure(intentErr) {
		return intentErr
	}
	slog.WarnContext(ctx, "cli.audit_events_deferred",
		slog.String("operation", intent.Verb),
		slog.Bool("outbox_table_exists", infrastructure.OutboxTableExists),
		slog.Bool("operator_login_exists", infrastructure.OperatorLoginExists))
	deferred := &deferredOutbox{events: nil}
	runErr := audit.RunExternal(runContext, deferred, intent, func(stepContext context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
		return run(stepContext, steps)
	})
	for _, event := range deferred.events {
		if err := outbox.WriteOutbox(ctx, event); err != nil {
			return recordedAfterRunError(ctx, "record deferred command events", runErr, err)
		}
	}
	if runErr != nil {
		return fmt.Errorf("run after creating the audit infrastructure: %w", runErr)
	}
	return nil
}
