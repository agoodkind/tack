package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/clock"
)

// ExternalResultDeadline limits how long RunExternal waits to save the result
// event after the run function returns.
const ExternalResultDeadline = 30 * time.Second

// ErrExternalIntentNotSaved reports that RunExternal did not save the intent
// event and did not call the run function.
var ErrExternalIntentNotSaved = errors.New("audit external: RunExternal did not save the intent event")

// RunExternal saves the intent event in outbox before calling run to change
// another system. The run function records each step through StepRecorder
// and returns the observed outcome. RunExternal then saves a result event
// for the attempt with a new event ID. Only partially_completed and incomplete
// outcomes include committed steps.
//
// When outbox implements IdempotentOutboxWriter, retries with the same intent
// event ID save the operation intent and each step intent only once. Step
// intent IDs derive from the parent operation ID and step target.
// RunExternal returns the run error and joins any error from saving the result.
func RunExternal(
	ctx context.Context,
	outbox OutboxWriter,
	intent Event,
	run func(context.Context, StepRecorder) (OutcomeDetails, error),
) error {
	if outbox == nil {
		return intentFailure(ctx, intent, errors.New("audit outbox is nil"))
	}
	if err := validateExternalIntent(intent); err != nil {
		return intentFailure(ctx, intent, err)
	}
	if err := writeOnce(ctx, outbox, intent); err != nil {
		return intentFailure(ctx, intent, err)
	}
	steps := newStepRecorder(outbox, intent)
	details, runErr := run(ctx, steps)
	result := externalResult(intent, steps.summary(), details, runErr)
	if err := recordExternalResult(ctx, outbox, result); err != nil {
		slog.ErrorContext(ctx, "audit.external.result_failed",
			slog.String("event_id", result.EventID.String()), slog.String("err", err.Error()))
		return errors.Join(runErr, err)
	}
	slog.InfoContext(ctx, "audit.external.recorded",
		slog.String("verb", intent.Verb),
		slog.String("operation_id", intent.Context.OperationID.String()),
		slog.String("outcome", string(result.Outcome)))
	return runErr
}

// recordExternalResult validates result and writes it to outbox with a
// context that ignores the cancellation of ctx and ends after
// ExternalResultDeadline.
func recordExternalResult(ctx context.Context, outbox OutboxWriter, result Event) error {
	if err := ValidateEvent(result); err != nil {
		return externalFailure(ctx, "record external result", result.EventID, err)
	}
	recordContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), ExternalResultDeadline)
	defer cancel()
	if err := outbox.WriteOutbox(recordContext, result); err != nil {
		return externalFailure(ctx, "record external result", result.EventID, err)
	}
	return nil
}

// validateExternalIntent requires a format 2 external intent event with the
// pending outcome.
func validateExternalIntent(intent Event) error {
	eventContext := intent.Context
	if eventContext.AuditFormatVersion != AuditFormatComplete || eventContext.OperationClass != OperationExternal ||
		eventContext.Phase != PhaseIntent {
		return errors.New("external intent requires format 2, the external class, and the intent phase")
	}
	return ValidateEvent(intent)
}

// writeOnce uses WriteOutboxIfAbsent to avoid writing a second row for an
// existing event ID when outbox implements IdempotentOutboxWriter.
// Otherwise, it calls WriteOutbox.
func writeOnce(ctx context.Context, outbox OutboxWriter, event Event) error {
	var err error
	if idempotent, ok := outbox.(IdempotentOutboxWriter); ok {
		_, err = idempotent.WriteOutboxIfAbsent(ctx, event)
	} else {
		err = outbox.WriteOutbox(ctx, event)
	}
	if err != nil {
		slog.ErrorContext(ctx, "audit.external.write_failed",
			slog.String("event_id", event.EventID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("write audit event %s: %w", event.EventID, err)
	}
	return nil
}

func externalResult(intent Event, steps stepSummary, details OutcomeDetails, runErr error) Event {
	now := clock.Now().UTC()
	result := intent
	result.EventID = uuid.Must(uuid.NewV7())
	result.OccurredAt = now
	result.Context.Phase = PhaseResult
	result.Context.CompletedAt = now
	if result.Context.CompletedAt.Before(result.Context.StartedAt) {
		result.Context.CompletedAt = result.Context.StartedAt
	}
	resolved := resolveExternalOutcome(details, runErr, steps)
	result.Outcome = resolved.Outcome
	result.Context.CommittedSteps = resolved.CommittedSteps
	result.Error = nil
	if resolved.Outcome != OutcomeCompleted {
		result.Error = &EventError{Code: resolved.FailureCode, Message: resolved.FailureMessage}
	}
	return result
}

func intentFailure(ctx context.Context, intent Event, err error) error {
	return externalFailure(ctx, "record external intent", intent.EventID, errors.Join(ErrExternalIntentNotSaved, err))
}

func externalFailure(ctx context.Context, action string, eventID uuid.UUID, err error) error {
	wrapped := fmt.Errorf("%s %s: %w", action, eventID, err)
	slog.ErrorContext(ctx, "audit.external.record_failed",
		slog.String("action", action), slog.String("event_id", eventID.String()), slog.String("err", wrapped.Error()))
	return wrapped
}
