package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/clock"
)

const serviceStepCode = "step_failed"

// AppendOutbox writes audit events through an OutboxAppender.
type AppendOutbox struct {
	appender OutboxAppender
}

// NewAppendOutbox creates an AppendOutbox that writes events through appender.
func NewAppendOutbox(appender OutboxAppender) AppendOutbox {
	return AppendOutbox{appender: appender}
}

// WriteOutbox encodes event and appends it as a single outbox record.
// It rejects events that exceed the record size limit.
func (o AppendOutbox) WriteOutbox(ctx context.Context, event Event) error {
	if o.appender == nil {
		return serviceRunFailure(ctx, "append the audit event", event.EventID, errors.New("the outbox appender is nil"))
	}
	encoded, err := MarshalEvent(event)
	if err != nil {
		return serviceRunFailure(ctx, "encode the audit event", event.EventID, err)
	}
	if !FitsSingleRecord(encoded) {
		return serviceRunFailure(ctx, "append the audit event", event.EventID,
			fmt.Errorf("the event has %d bytes and does not fit in one outbox record", len(encoded)))
	}
	if err := o.appender.Append(ctx, encoded); err != nil {
		return serviceRunFailure(ctx, "append the audit event", event.EventID, err)
	}
	return nil
}

// RecorderOutbox writes events through a Recorder, such as the synchronous
// Kafka producer of the audit relay.
type RecorderOutbox struct {
	recorder Recorder
}

// NewRecorderOutbox creates a RecorderOutbox that writes events through recorder.
func NewRecorderOutbox(recorder Recorder) RecorderOutbox {
	return RecorderOutbox{recorder: recorder}
}

// WriteOutbox records event through the configured Recorder.
func (o RecorderOutbox) WriteOutbox(ctx context.Context, event Event) error {
	if o.recorder == nil {
		return serviceRunFailure(ctx, "record the audit event", event.EventID, errors.New("the recorder is nil"))
	}
	if err := o.recorder.Record(ctx, event); err != nil {
		return serviceRunFailure(ctx, "record the audit event", event.EventID, err)
	}
	return nil
}

// ServiceActor creates a service actor with the supplied ID and name.
func ServiceActor(id uuid.UUID, name string) Actor {
	return Actor{
		Type: ActorService, ID: id, Email: "", Name: name, SessionID: "",
		IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
	}
}

// RunServiceExternal records an in-process service job as an external operation
// on target. Each run uses a new UUIDv7 operation ID, actor, and the system
// organization.
func RunServiceExternal(
	ctx context.Context,
	outbox OutboxWriter,
	actor Actor,
	verb Verb,
	target Target,
	run func(context.Context, StepRecorder) (OutcomeDetails, error),
) error {
	now := clock.Now().UTC()
	eventTarget := target
	intent := Event{
		Verb: string(verb), EventID: uuid.Must(uuid.NewV7()), Actor: actor,
		Entity: Entity{Type: target.Kind, NodeType: target.NodeType, ID: target.ID, Identifier: target.Reference, Name: target.Name},
		Context: EventContext{
			OrgID: SystemOrgID(), WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: "", TraceID: "", Source: SourceSystem, Tool: "", RPC: "", Reason: "",
			AuditFormatVersion: AuditFormatComplete, OperationClass: OperationExternal, Phase: PhaseIntent,
			OperationID: uuid.Must(uuid.NewV7()), ParentOperationID: uuid.Nil, StartedAt: now, CompletedAt: time.Time{},
			Target: &eventTarget, Read: nil, CommittedSteps: nil,
		},
		Delta: nil, Outcome: OutcomePending, Error: nil, IdempotencyKey: "", OccurredAt: now, Extra: nil,
	}
	return RunExternal(ctx, outbox, intent, run)
}

// RunServiceStep records a service job as an external operation with one step.
// It records planned evidence before calling effect and records the result
// afterward. It calls observe only after effect succeeds.
func RunServiceStep(
	ctx context.Context,
	outbox OutboxWriter,
	actor Actor,
	verb Verb,
	target Target,
	planned json.RawMessage,
	effect func(context.Context) error,
	observe func(context.Context) (json.RawMessage, error),
) error {
	return RunServiceExternal(ctx, outbox, actor, verb, target, func(ctx context.Context, steps StepRecorder) (OutcomeDetails, error) {
		step := ExternalStep(target, planned)
		if err := steps.BeginStep(ctx, step); err != nil {
			slog.ErrorContext(ctx, "audit.service_run.step_failed", slog.String("err", err.Error()))
			return failedServiceStep(err), fmt.Errorf("record the step: %w", err)
		}
		details := OutcomeDetails{Outcome: OutcomeCompleted, FailureCode: "", FailureMessage: "", CommittedSteps: nil}
		var evidence json.RawMessage
		runErr := effect(ctx)
		if runErr == nil {
			evidence, runErr = observe(ctx)
		}
		if runErr != nil {
			details = failedServiceStep(runErr)
		}
		if err := steps.FinishStep(ctx, FinishedStep(step, details, evidence)); err != nil {
			slog.ErrorContext(ctx, "audit.service_run.step_failed", slog.String("err", err.Error()))
			return details, errors.Join(runErr, fmt.Errorf("record the step result: %w", err))
		}
		return details, runErr
	})
}

func failedServiceStep(cause error) OutcomeDetails {
	return OutcomeDetails{Outcome: OutcomeFailed, FailureCode: serviceStepCode, FailureMessage: cause.Error(), CommittedSteps: nil}
}

func serviceRunFailure(ctx context.Context, action string, eventID uuid.UUID, err error) error {
	wrapped := fmt.Errorf("%s %s: %w", action, eventID, err)
	slog.ErrorContext(ctx, "audit.service_run.record_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
