package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/clock"
)

// StepRecorder saves the steps of one external operation in the durable
// outbox. BeginStep saves a step before its effect. FinishStep saves the
// observed result of that step.
type StepRecorder interface {
	BeginStep(ctx context.Context, step Event) error
	FinishStep(ctx context.Context, step Event) error
}

// ExternalStep returns a step event for target with evidence as its Extra.
// The StepRecorder adds the actor, identity, operation, and time fields.
func ExternalStep(target Target, evidence json.RawMessage) Event {
	stepTarget := target
	var event Event
	event.Context.Target = &stepTarget
	event.Extra = evidence
	return event
}

// FinishedStep returns step with the outcome and error of details and with
// evidence as its Extra when evidence is not empty. An empty outcome means
// completed.
func FinishedStep(step Event, details OutcomeDetails, evidence json.RawMessage) Event {
	finished := step
	finished.Outcome = details.Outcome
	if finished.Outcome == "" {
		finished.Outcome = OutcomeCompleted
	}
	finished.Error = nil
	if finished.Outcome != OutcomeCompleted {
		finished.Error = &EventError{Code: details.FailureCode, Message: details.FailureMessage}
	}
	finished.Context.CommittedSteps = details.CommittedSteps
	if len(evidence) != 0 {
		finished.Extra = evidence
	}
	return finished
}

// stepRecorder saves step events with the actor and operation of parent.
type stepRecorder struct {
	outbox   OutboxWriter
	parent   Event
	mutex    sync.Mutex
	order    []string
	labels   map[string]string
	started  map[string]time.Time
	finished map[string]Outcome
}

func newStepRecorder(outbox OutboxWriter, parent Event) *stepRecorder {
	return &stepRecorder{
		outbox: outbox, parent: parent, mutex: sync.Mutex{}, order: nil,
		labels: map[string]string{}, started: map[string]time.Time{}, finished: map[string]Outcome{},
	}
}

// BeginStep saves a pending step intent. When the outbox implements
// IdempotentOutboxWriter, repeated calls for the same target and parent
// operation ID do not write another intent.
func (r *stepRecorder) BeginStep(ctx context.Context, step Event) error {
	if step.Context.Target == nil {
		return externalFailure(ctx, "record external step", r.parent.EventID, errors.New("step requires a target"))
	}
	key := targetKey(*step.Context.Target)
	now := clock.Now().UTC()
	event := r.stepEvent(step, now, now)
	event.EventID = StepIntentEventID(r.parent.Context.OperationID, *step.Context.Target)
	event.Context.Phase = PhaseIntent
	event.Context.CompletedAt = time.Time{}
	event.Context.CommittedSteps = nil
	event.Outcome = OutcomePending
	event.Error = nil
	if err := ValidateEvent(event); err != nil {
		return externalFailure(ctx, "record external step", event.EventID, err)
	}
	if err := writeOnce(ctx, r.outbox, event); err != nil {
		return externalFailure(ctx, "record external step", event.EventID, err)
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if _, begun := r.started[key]; !begun {
		r.order = append(r.order, key)
		r.labels[key] = targetLabel(*step.Context.Target)
		r.started[key] = now
	}
	return nil
}

// FinishStep saves the terminal result of a step that BeginStep saved in
// this attempt.
func (r *stepRecorder) FinishStep(ctx context.Context, step Event) error {
	if step.Context.Target == nil {
		return externalFailure(ctx, "record external step result", r.parent.EventID, errors.New("step requires a target"))
	}
	key := targetKey(*step.Context.Target)
	r.mutex.Lock()
	startedAt, begun := r.started[key]
	r.mutex.Unlock()
	if !begun {
		return externalFailure(ctx, "record external step result", r.parent.EventID,
			fmt.Errorf("step %s did not begin in this attempt", targetLabel(*step.Context.Target)))
	}
	event := r.stepEvent(step, startedAt, clock.Now().UTC())
	event.EventID = uuid.Must(uuid.NewV7())
	event.Context.Phase = PhaseResult
	if err := ValidateEvent(event); err != nil {
		return externalFailure(ctx, "record external step result", event.EventID, err)
	}
	if err := r.outbox.WriteOutbox(ctx, event); err != nil {
		return externalFailure(ctx, "record external step result", event.EventID, err)
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.finished[key] = event.Outcome
	return nil
}

// stepEvent copies the parent actor and context into a step event for the
// target of step.
func (r *stepRecorder) stepEvent(step Event, startedAt, completedAt time.Time) Event {
	target := *step.Context.Target
	event := r.parent
	event.Entity = Entity{Type: target.Kind, NodeType: target.NodeType, ID: target.ID, Identifier: target.Reference, Name: target.Name}
	event.Context.Target = &target
	event.Context.OperationID = StepOperationID(r.parent.Context.OperationID, target)
	event.Context.ParentOperationID = r.parent.Context.OperationID
	event.Context.StartedAt = startedAt
	event.Context.CompletedAt = completedAt
	event.Context.CommittedSteps = step.Context.CommittedSteps
	event.Delta = step.Delta
	event.Extra = step.Extra
	event.Outcome = step.Outcome
	event.Error = step.Error
	event.IdempotencyKey = ""
	event.OccurredAt = completedAt
	return event
}

// stepSummary lists the step labels that committed and the step labels that
// began without a recorded result, in the order the steps began.
type stepSummary struct {
	committed  []string
	unfinished []string
}

func (r *stepRecorder) summary() stepSummary {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	var summary stepSummary
	for _, key := range r.order {
		outcome, finished := r.finished[key]
		switch {
		case !finished:
			summary.unfinished = append(summary.unfinished, r.labels[key])
		case outcome == OutcomeCompleted || outcome == OutcomePartiallyCompleted:
			summary.committed = append(summary.committed, r.labels[key])
		}
	}
	return summary
}
