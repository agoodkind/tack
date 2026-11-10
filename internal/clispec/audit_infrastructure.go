package clispec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

func runAfterCreatingAuditInfrastructure(
	ctx context.Context,
	intent audit.Event,
	outcome audit.Event,
	runContext context.Context,
	outbox audit.OutboxWriter,
	probe audit.InfrastructureProbe,
	run func(context.Context) error,
	intentErr error,
) error {
	if probe == nil {
		return loggedAuditError(ctx, "audit infrastructure probe is unwired", intentErr)
	}
	infrastructure, probeErr := probe(ctx)
	if probeErr != nil {
		slog.ErrorContext(ctx, "cli.audit_infrastructure_probe_failed",
			slog.String("err", probeErr.Error()))
		return loggedAuditError(ctx, "record command intent", intentErr)
	}
	if infrastructure.OutboxTableExists && infrastructure.OperatorLoginExists &&
		!isAuthenticationFailure(intentErr) {
		return loggedAuditError(ctx, "record command intent", intentErr)
	}

	startedAt := clock.Now().UTC()
	runErr := run(runContext)
	intentWithStart, err := withIntentStartedAt(ctx, intent, startedAt)
	if err != nil {
		return recordedAfterRunError(ctx, "encode command intent", runErr, err)
	}
	intentWithStart.OccurredAt = clock.Now().UTC()
	if err := outbox.WriteOutbox(ctx, intentWithStart); err != nil {
		return recordedAfterRunError(ctx, "record command intent", runErr, err)
	}

	outcome.EventID = uuid.Must(uuid.NewV7())
	outcome.OccurredAt = clock.Now().UTC()
	if runErr != nil {
		outcome.Outcome = audit.OutcomeError
		outcome.Error = &audit.EventError{Code: "command_failed", Message: runErr.Error()}
	}
	if err := outbox.WriteOutbox(ctx, outcome); err != nil {
		return recordedAfterRunError(ctx, "record command outcome", runErr, err)
	}
	return runErr
}

func isAuthenticationFailure(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "28P01" || pgErr.Code == "28000"
}

func withIntentStartedAt(
	ctx context.Context,
	intent audit.Event,
	startedAt time.Time,
) (audit.Event, error) {
	var extra operatorEventExtra
	if err := json.Unmarshal(intent.Extra, &extra); err != nil {
		slog.ErrorContext(ctx, "cli.audit_extra_decode_failed", slog.String("err", err.Error()))
		return audit.Event{}, fmt.Errorf("decode operator event extra: %w", err)
	}
	extra.StartedAt = &startedAt
	encodedExtra, err := json.Marshal(extra)
	if err != nil {
		slog.ErrorContext(ctx, "cli.audit_extra_encode_failed", slog.String("err", err.Error()))
		return audit.Event{}, fmt.Errorf("encode operator event extra: %w", err)
	}
	intent.Extra = encodedExtra
	return intent, nil
}

func recordedAfterRunError(ctx context.Context, action string, runErr error, err error) error {
	message := "command ran but its audit record could not be written"
	if runErr != nil {
		message = fmt.Sprintf("command ran and failed (%s), but its audit record could not be written", runErr)
	}
	slog.ErrorContext(ctx, "cli.audit_failed",
		slog.String("action", action),
		slog.String("err", err.Error()),
	)
	return fmt.Errorf("%s: %s: %w", message, action, err)
}
