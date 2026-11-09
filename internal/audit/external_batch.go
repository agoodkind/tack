package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/clock"
)

// BatchOutboxWriter is an OutboxWriter that also commits multiple events
// in one durable transaction in slice order.
type BatchOutboxWriter interface {
	OutboxWriter
	WriteOutboxBatch(ctx context.Context, events []Event) error
}

// OutboxBatchAppender is an OutboxAppender that also appends multiple encoded
// events in one durable transaction in slice order.
type OutboxBatchAppender interface {
	OutboxAppender
	AppendBatch(ctx context.Context, events []json.RawMessage) error
}

// BatchAppendOutbox writes audit events through an OutboxBatchAppender.
type BatchAppendOutbox struct {
	AppendOutbox
	batchAppender OutboxBatchAppender
}

// NewBatchAppendOutbox creates a BatchAppendOutbox that writes events
// through appender.
func NewBatchAppendOutbox(appender OutboxBatchAppender) BatchAppendOutbox {
	return BatchAppendOutbox{AppendOutbox: NewAppendOutbox(appender), batchAppender: appender}
}

// WriteOutboxBatch encodes each event and appends the events in one
// transaction in slice order. It rejects the entire batch if any event exceeds
// the size limit for one outbox record.
func (o BatchAppendOutbox) WriteOutboxBatch(ctx context.Context, events []Event) error {
	if o.batchAppender == nil {
		return serviceRunFailure(
			ctx,
			"append the audit events",
			uuid.Nil,
			errors.New("the outbox appender is nil"),
		)
	}

	records := make([]json.RawMessage, 0, len(events))
	for _, event := range events {
		encoded, err := MarshalEvent(event)
		if err != nil {
			return serviceRunFailure(ctx, "encode the audit event", event.EventID, err)
		}

		if !FitsSingleRecord(encoded) {
			return serviceRunFailure(
				ctx,
				"append the audit event",
				event.EventID,
				fmt.Errorf(
					"the event has %d bytes and does not fit in one outbox record",
					len(encoded),
				),
			)
		}

		records = append(records, encoded)
	}

	if err := o.batchAppender.AppendBatch(ctx, records); err != nil {
		return serviceRunFailure(ctx, "append the audit events", uuid.Nil, err)
	}

	return nil
}

// RunExternalStep records one external operation with one step. It commits
// the operation intent and step intent in one transaction before calling run.
// If the commit fails, it does not call run and returns an error that matches
// ErrEventNotRecorded and ErrExternalIntentNotSaved. run finishes the step
// through StepRecorder and must not begin another step. RunExternalStep then
// saves the operation result as RunExternal does.
func RunExternalStep(
	ctx context.Context,
	outbox BatchOutboxWriter,
	intent Event,
	step Event,
	run func(context.Context, StepRecorder) (OutcomeDetails, error),
) error {
	if outbox == nil {
		return intentFailure(ctx, intent, errors.New("audit outbox is nil"))
	}

	if err := validateExternalIntent(intent); err != nil {
		return intentFailure(ctx, intent, err)
	}

	if step.Context.Target == nil {
		return intentFailure(ctx, intent, errors.New("step requires a target"))
	}

	steps := newStepRecorder(outbox, intent)
	now := clock.Now().UTC()
	stepIntent, err := steps.intentEvent(step, now)
	if err != nil {
		return intentFailure(ctx, intent, err)
	}

	if err := outbox.WriteOutboxBatch(ctx, []Event{intent, stepIntent}); err != nil {
		return intentFailure(ctx, intent, err)
	}

	steps.noteBegun(*step.Context.Target, now)

	return finishExternal(ctx, outbox, intent, steps, run)
}
