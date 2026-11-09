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

// NewBatchAppendOutbox creates an outbox that writes through appender.
//
// The returned [BatchAppendOutbox] uses appender for single-event writes and
// durable batches.
//
// A caller can create the outbox as follows:
//
//	outbox := NewBatchAppendOutbox(appender)
//	err := outbox.WriteOutboxBatch(ctx, []Event{intent, stepIntent})
func NewBatchAppendOutbox(appender OutboxBatchAppender) BatchAppendOutbox {
	return BatchAppendOutbox{AppendOutbox: NewAppendOutbox(appender), batchAppender: appender}
}

// WriteOutboxBatch encodes and appends events in one transaction.
//
// The method preserves slice order. It rejects the entire batch when any
// event exceeds the size limit for one outbox record. Every event must pass
// encoding and size checks before the method appends the batch.
//
// A caller can append a batch as follows:
//
//	// The operation intent precedes the step intent in the transaction.
//	err := outbox.WriteOutboxBatch(ctx, []Event{intent, stepIntent})
func (o BatchAppendOutbox) WriteOutboxBatch(ctx context.Context, events []Event) error {
	if o.batchAppender == nil {
		return serviceRunFailure(ctx, "append the audit events", uuid.Nil, errors.New("the outbox appender is nil"))
	}
	records := make([]json.RawMessage, 0, len(events))
	for _, event := range events {
		encoded, err := MarshalEvent(event)
		if err != nil {
			return serviceRunFailure(ctx, "encode the audit event", event.EventID, err)
		}
		if !FitsSingleRecord(encoded) {
			return serviceRunFailure(ctx, "append the audit event", event.EventID,
				fmt.Errorf("the event has %d bytes and does not fit in one outbox record", len(encoded)))
		}
		records = append(records, encoded)
	}
	if err := o.batchAppender.AppendBatch(ctx, records); err != nil {
		return serviceRunFailure(ctx, "append the audit events", uuid.Nil, err)
	}
	return nil
}

// RunExternalStep records one external operation with one step.
//
// The function commits the operation and step intents together before run.
// A commit failure prevents run and returns an error that matches
// [ErrEventNotRecorded] and [ErrExternalIntentNotSaved]. The run function
// must finish the step through [StepRecorder] and must not begin another
// step. RunExternalStep saves the operation result as [RunExternal] does.
//
// A caller must finish the existing step instead of beginning it again:
//
//	run := func(ctx context.Context, steps StepRecorder) (OutcomeDetails, error) {
//		details, effectErr := performEffect(ctx)
//		stepErr := steps.FinishStep(ctx, FinishedStep(step, details, nil))
//		return details, errors.Join(effectErr, stepErr)
//	}
//	err := RunExternalStep(ctx, outbox, intent, step, run)
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
