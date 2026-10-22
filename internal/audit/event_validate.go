package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
)

// ValidateEvent checks format rules and rejects unsupported versions.
// Format 0 events cannot set format 2 context fields, terminal outcomes,
// or Protected, and cannot use the unauthenticated actor kind.
// Format 2 events require an outcome, evidence for their operation class,
// and exactly one valid JSON Protected value per delta reference with
// SubjectID equal to Context.Target.ID.
func ValidateEvent(event Event) error {
	return validateEventContract(event, true)
}

// validateStoredEvent checks format rules without comparing Protected
// values with delta references. Ledger rows store the references, and
// audit.pii stores the values.
func validateStoredEvent(event Event) error {
	return validateEventContract(event, false)
}

// validateEventContract checks supported format rules and includes the event
// ID in each error. For format 2, pairProtected also enables validation of
// protected values against delta references.
func validateEventContract(event Event, pairProtected bool) error {
	var err error
	switch event.Context.AuditFormatVersion {
	case AuditFormatLegacy:
		err = validateLegacyEvent(event)
	case AuditFormatComplete:
		err = validateCompleteEvent(event)
		if err == nil && pairProtected {
			err = validateProtected(event)
		}
	default:
		err = fmt.Errorf("audit format version %d is unsupported", event.Context.AuditFormatVersion)
	}
	if err != nil {
		return fmt.Errorf("audit event %s: %s", event.EventID, err.Error())
	}
	return nil
}

// ValidateSpec checks an operation definition for a verb, a target
// extractor, a safe input selector, and a supported class. It rejects read
// definitions that mutate and change definitions that do not mutate.
func ValidateSpec(spec Spec) error {
	switch {
	case spec.Verb == "":
		return errors.New("audit spec: definition requires a verb")
	case spec.Targets == nil:
		return fmt.Errorf("audit spec %s: definition requires a target extractor", spec.Verb)
	case spec.SafeInput == nil:
		return fmt.Errorf("audit spec %s: definition requires a safe input selector", spec.Verb)
	}
	switch spec.Class {
	case OperationRead:
		if spec.Mutates || spec.Atomic {
			return fmt.Errorf("audit spec %s: read definition cannot set Mutates or Atomic", spec.Verb)
		}
	case OperationDatabaseChange, OperationExternal:
		if !spec.Mutates || spec.Reads {
			return fmt.Errorf("audit spec %s: %s definition requires Mutates and cannot set Reads", spec.Verb, spec.Class)
		}
		if spec.Class == OperationExternal && spec.Atomic {
			return fmt.Errorf("audit spec %s: external definition cannot set Atomic", spec.Verb)
		}
	default:
		return fmt.Errorf("audit spec %s: operation class %q is unsupported", spec.Verb, spec.Class)
	}
	return nil
}

// ValidateChange checks a changed record before a writer records it. It
// requires an event ID, a valid target, and an old or new value that is
// valid JSON.
func ValidateChange(change Change) error {
	if change.EventID == uuid.Nil {
		return errors.New("audit change: record change requires an event ID")
	}
	if err := validateTarget(change.Target); err != nil {
		return fmt.Errorf("audit change %s: %s", change.EventID, err.Error())
	}
	if len(change.Before) == 0 && len(change.After) == 0 {
		return fmt.Errorf("audit change %s: record requires an old or new value", change.EventID)
	}
	for _, value := range []json.RawMessage{change.Before, change.After} {
		if len(value) != 0 && !json.Valid(value) {
			return fmt.Errorf("audit change %s: change value must be valid JSON", change.EventID)
		}
	}
	return nil
}

// validateLegacyEvent rejects format 0 events that set format 2 context
// fields, a format 2 outcome, or the unauthenticated actor kind.
func validateLegacyEvent(event Event) error {
	eventContext := event.Context
	hasCompleteField := eventContext.OperationClass != "" || eventContext.Phase != "" ||
		eventContext.OperationID != uuid.Nil || eventContext.ParentOperationID != uuid.Nil ||
		!eventContext.StartedAt.IsZero() || !eventContext.CompletedAt.IsZero() ||
		eventContext.Target != nil || eventContext.Read != nil || len(eventContext.CommittedSteps) != 0
	if hasCompleteField {
		return errors.New("format 0 event cannot set format 2 context fields")
	}
	if isTerminalOutcome(event.Outcome) {
		return fmt.Errorf("format 0 event cannot set format 2 outcome %q", event.Outcome)
	}
	if event.Actor.Type == ActorUnauthenticated {
		return errors.New("format 0 event cannot use the unauthenticated actor kind")
	}
	if len(event.Protected) != 0 {
		return errors.New("format 0 event cannot set protected values")
	}
	return nil
}

func validateCompleteEvent(event Event) error {
	eventContext := event.Context
	switch {
	case event.EventID == uuid.Nil || event.Verb == "" || event.OccurredAt.IsZero():
		return errors.New("format 2 event requires an event ID, a verb, and an occurrence time")
	case eventContext.OrgID == uuid.Nil || eventContext.OperationID == uuid.Nil || eventContext.StartedAt.IsZero():
		return errors.New("format 2 event requires an org ID, an operation ID, and a start time")
	case eventContext.Target == nil:
		return errors.New("format 2 event requires a target")
	case event.Outcome == "":
		return errors.New("format 2 event: outcome required")
	}
	if err := validateActor(event); err != nil {
		return err
	}
	if err := validateTarget(*eventContext.Target); err != nil {
		return err
	}
	if event.Entity.Type != eventContext.Target.Kind || event.Entity.ID != eventContext.Target.ID {
		return errors.New("event entity type and ID must match the target kind and ID")
	}
	if err := validatePhase(eventContext.OperationClass, eventContext.Phase, event.Outcome); err != nil {
		return err
	}
	if err := validateOutcomeFields(event); err != nil {
		return err
	}
	return validateClassEvidence(event)
}

func validatePhase(class OperationClass, phase Phase, outcome Outcome) error {
	phases, known := allowedPhases[class]
	if !known {
		return fmt.Errorf("operation class %q is unsupported", class)
	}
	if !slices.Contains(phases, phase) {
		return fmt.Errorf("phase %q is invalid for operation class %s", phase, class)
	}
	if phase == PhaseIntent {
		if outcome != OutcomePending {
			return errors.New("intent phase event requires the pending outcome")
		}
		return nil
	}
	if !isTerminalOutcome(outcome) {
		return fmt.Errorf("outcome %q is not a terminal outcome", outcome)
	}
	if phase == PhaseChange && outcome != OutcomeCompleted {
		return errors.New("change phase event requires the completed outcome")
	}
	return nil
}

func validateClassEvidence(event Event) error {
	eventContext := event.Context
	if eventContext.OperationClass != OperationRead {
		if eventContext.Read != nil {
			return errors.New("only read class events can set read evidence")
		}
		hasValue := event.Delta != nil && (len(event.Delta.Before) != 0 || len(event.Delta.After) != 0)
		if eventContext.Phase == PhaseChange && !hasValue {
			return errors.New("change phase event requires an old or new value")
		}
		return nil
	}
	if event.Delta != nil {
		return errors.New("read class event cannot set a delta")
	}
	if eventContext.Read == nil {
		if event.Outcome == OutcomeCompleted || event.Outcome == OutcomePartiallyCompleted {
			return errors.New("completed or partially completed read requires read evidence")
		}
		return nil
	}
	return validateReadEvidence(*eventContext.Read)
}
