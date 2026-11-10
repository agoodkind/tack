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

// ExternalStep creates a step event for target with evidence in Extra.
//
// [StepRecorder] supplies the actor, event identity, operation, and time fields
// when it records the step.
//
// A caller can prepare a step as follows:
//
//	step := ExternalStep(target, evidence)
//	err := recorder.BeginStep(ctx, step)
func ExternalStep(target Target, evidence json.RawMessage) Event {
	stepTarget := target
	var event Event
	event.Context.Target = &stepTarget
	event.Extra = evidence

	return event
}

// FinishedStep copies step and applies the outcome and error from details.
//
// An empty outcome means completed. Nonempty evidence replaces Extra; empty
// evidence preserves the step's existing Extra value.
//
// A caller can prepare the result as follows:
//
//	finished := FinishedStep(step, details, evidence)
//	err := recorder.FinishStep(ctx, finished)
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
		outbox:   outbox,
		parent:   parent,
		mutex:    sync.Mutex{},
		order:    nil,
		labels:   map[string]string{},
		started:  map[string]time.Time{},
		finished: map[string]Outcome{},
	}
}

// BeginStep saves a pending step intent.
//
// When the outbox implements [IdempotentOutboxWriter], repeated calls for
// the same target and parent operation ID do not write another intent. The
// method marks the step as begun after the outbox write succeeds.
//
// A caller begins the step before performing its effect:
//
//	step := ExternalStep(target, evidence)
//	if err := recorder.BeginStep(ctx, step); err != nil {
//		return err
//	}
//	// The effect can begin after its intent succeeds.
func (r *stepRecorder) BeginStep(ctx context.Context, step Event) error {
	if step.Context.Target == nil {
		return externalFailure(
			ctx,
			"record external step",
			r.parent.EventID,
			errors.New("step requires a target"),
		)
	}

	now := clock.Now().UTC()
	event, err := r.intentEvent(step, now)
	if err != nil {
		return externalFailure(ctx, "record external step", event.EventID, err)
	}

	if err := writeOnce(ctx, r.outbox, event); err != nil {
		return externalFailure(ctx, "record external step", event.EventID, err)
	}

	r.noteBegun(*step.Context.Target, now)

	return nil
}

// intentEvent builds and validates the pending intent event for step at now.
// The step must have a target.
func (r *stepRecorder) intentEvent(step Event, now time.Time) (Event, error) {
	event := r.stepEvent(step, now, now)
	event.EventID = StepIntentEventID(r.parent.Context.OperationID, *step.Context.Target)
	event.Context.Phase = PhaseIntent
	event.Context.CompletedAt = time.Time{}
	event.Context.CommittedSteps = nil
	event.Outcome = OutcomePending
	event.Error = nil

	return event, ValidateEvent(event)
}

// noteBegun records that the step for target began at startedAt in this
// attempt.
func (r *stepRecorder) noteBegun(target Target, startedAt time.Time) {
	key := targetKey(target)
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if _, begun := r.started[key]; !begun {
		r.order = append(r.order, key)
		r.labels[key] = targetLabel(target)
		r.started[key] = startedAt
	}
}

// FinishStep saves a terminal result for a step begun in this attempt.
//
// This attempt must have recorded the step intent and registered its start.
// [stepRecorder.BeginStep] and [RunExternalStep] both do this. The method
// validates and writes the result before marking the step finished.
//
// A caller records the observed result after the effect:
//
//	finished := FinishedStep(step, details, observedEvidence)
//	err := recorder.FinishStep(ctx, finished)
func (r *stepRecorder) FinishStep(ctx context.Context, step Event) error {
	if step.Context.Target == nil {
		return externalFailure(
			ctx,
			"record external step result",
			r.parent.EventID,
			errors.New("step requires a target"),
		)
	}

	key := targetKey(*step.Context.Target)
	r.mutex.Lock()
	startedAt, begun := r.started[key]
	r.mutex.Unlock()
	if !begun {
		return externalFailure(
			ctx,
			"record external step result",
			r.parent.EventID,
			fmt.Errorf("step %s did not begin in this attempt", targetLabel(*step.Context.Target)),
		)
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

func (r *stepRecorder) stepEvent(step Event, startedAt, completedAt time.Time) Event {
	target := *step.Context.Target
	event := r.parent
	event.Entity = Entity{
		Type:       target.Kind,
		NodeType:   target.NodeType,
		ID:         target.ID,
		Identifier: target.Reference,
		Name:       target.Name,
	}
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
