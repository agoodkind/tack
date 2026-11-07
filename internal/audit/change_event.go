package audit

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/clock"
)

const (
	// TargetNodeTypeDefinition is the Target.NodeType value for a node type definition record.
	TargetNodeTypeDefinition = "node_type"
	// TargetNodeTypePropertyDefinition is the Target.NodeType value for a property definition record.
	TargetNodeTypePropertyDefinition = "property_def"
	// TargetSequenceCounter is the Target.NodeType value of a resource target
	// for one FoundationDB sequence counter.
	TargetSequenceCounter = "sequence"
	// TargetSearchSession is the Target.NodeType value of a resource target
	// for one search session record.
	TargetSearchSession = "search_session"
	// TargetSearchSessionReplay is the Target.NodeType value of a resource
	// target for one search session replay record.
	TargetSearchSessionReplay = "search_session_replay"
	// TargetSearchSessionVisited is the Target.NodeType value of a resource
	// target for one search session visited record.
	TargetSearchSessionVisited = "search_session_visited"
	// TargetAuditEventPart is the Target.NodeType value of a resource target
	// for one audit.event_parts row.
	TargetAuditEventPart = "audit_event_part"
)

const searchSessionURIPrefix = "tack:search-session/"

// SearchSessionTarget returns the resource target of one search session record.
// Target.ID contains sessionID. ownerID identifies the person who owns the protected
// values of the session. [uuid.Nil] does not set an owner.
func SearchSessionTarget(sessionID, ownerID uuid.UUID) Target {
	return Target{
		Kind: TargetKindResource, NodeType: TargetSearchSession, Reference: "", Name: "",
		URI: searchSessionURIPrefix + sessionID.String(), RelationType: "",
		ID: sessionID, SourceID: uuid.Nil, TargetID: uuid.Nil, OwnerID: ownerID,
	}
}

// ChangeEvent builds one format 2 change event for change as part of operation.
// It copies the actor, context, and extra fields from operation.
// It uses the event ID and target from change and sets Outcome to OutcomeCompleted.
// Delta records the complete old and new values.
//
// ChangeEvent returns an error if change or the resulting event fails validation.
func ChangeEvent(operation Event, change Change, completedAt time.Time) (Event, error) {
	if err := ValidateChange(change); err != nil {
		return Event{}, changeEventFailure(change, err)
	}
	target := change.Target
	eventContext := operation.Context
	eventContext.AuditFormatVersion = AuditFormatComplete
	eventContext.OperationClass = OperationDatabaseChange
	eventContext.Phase = PhaseChange
	eventContext.CompletedAt = completedAt.UTC()
	eventContext.Target = &target
	eventContext.Read = nil
	eventContext.CommitVersion = ""
	event := Event{
		Verb: string(changeVerb(change)), EventID: change.EventID, Actor: operation.Actor,
		Entity:  Entity{Type: target.Kind, NodeType: target.NodeType, ID: target.ID, Identifier: target.Reference, Name: target.Name},
		Context: eventContext,
		Delta:   &Delta{Before: change.Before, After: change.After, Changed: change.Changed},
		Outcome: OutcomeCompleted, Error: nil, IdempotencyKey: "",
		OccurredAt: completedAt.UTC(), Extra: operation.Extra, Protected: change.Protected,
	}
	if err := ValidateEvent(event); err != nil {
		return Event{}, changeEventFailure(change, err)
	}
	return event, nil
}

// ChangeFailureEvent builds one format 2 database change result event for an
// operation on target that did not commit its change. The event uses the
// system organization when operation does not state an organization.
//
// ChangeFailureEvent returns an error if the event fails validation.
func ChangeFailureEvent(operation Event, target Target, verb Verb, details OutcomeDetails) (Event, error) {
	completedAt := clock.Now().UTC()
	if completedAt.Before(operation.Context.StartedAt) {
		completedAt = operation.Context.StartedAt
	}
	eventTarget := target
	event := operation
	event.Verb, event.EventID, event.OccurredAt = string(verb), uuid.Must(uuid.NewV7()), completedAt
	event.Entity = Entity{Type: target.Kind, NodeType: target.NodeType, ID: target.ID, Identifier: target.Reference, Name: target.Name}
	if event.Context.OrgID == uuid.Nil {
		event.Context.OrgID = systemOrgID
	}
	event.Context.AuditFormatVersion = AuditFormatComplete
	event.Context.OperationClass, event.Context.Phase = OperationDatabaseChange, PhaseResult
	event.Context.CompletedAt, event.Context.Target = completedAt, &eventTarget
	event.Context.Read, event.Context.CommitVersion = nil, ""
	event.Context.CommittedSteps = details.CommittedSteps
	event.Delta, event.Outcome, event.Error = nil, details.Outcome, nil
	if details.Outcome != OutcomeCompleted {
		event.Error = &EventError{Code: details.FailureCode, Message: details.FailureMessage}
	}
	if err := ValidateEvent(event); err != nil {
		slog.Error("audit.change_failure_event_failed", slog.String("event_id", event.EventID.String()), slog.String("err", err.Error()))
		return Event{}, fmt.Errorf("build the change failure event %s: %w", event.EventID, err)
	}
	return event, nil
}

// changeVerb selects the verb for the target's Kind and NodeType based on
// whether the old and new record values exist.
func changeVerb(change Change) Verb {
	created, deleted := len(change.Before) == 0, len(change.After) == 0
	if change.Target.Kind == TargetKindRelationship {
		return pickVerb(created, deleted, VerbRelationshipAdd, VerbRelationshipUpdate, VerbRelationshipRemove)
	}
	if change.Target.Kind == TargetKindResource && change.Target.NodeType == TargetSequenceCounter {
		// The store never deletes a sequence counter.
		return pickVerb(created, deleted, VerbSequenceCreate, VerbSequenceUpdate, VerbSequenceUpdate)
	}
	switch change.Target.NodeType {
	case TargetAuditEventPart:
		return VerbAuditEventPartCreate
	case TargetNodeTypeDefinition:
		return pickVerb(created, deleted, VerbNodeTypeCreate, VerbNodeTypeUpdate, VerbNodeTypeDelete)
	case TargetNodeTypePropertyDefinition:
		return pickVerb(created, deleted, VerbPropertyDefCreate, VerbPropertyDefUpdate, VerbPropertyDefDelete)
	case TargetSearchSession, TargetSearchSessionReplay, TargetSearchSessionVisited:
		return pickVerb(created, deleted, VerbSearchSessionCreate, VerbSearchSessionUpdate, VerbSearchSessionDelete)
	default:
		return pickVerb(created, deleted, VerbNodeCreate, VerbNodeUpdate, VerbNodeDelete)
	}
}

func pickVerb(created, deleted bool, create, update, remove Verb) Verb {
	switch {
	case created:
		return create
	case deleted:
		return remove
	default:
		return update
	}
}

func changeEventFailure(change Change, err error) error {
	slog.Error("audit.change_event_failed", slog.String("event_id", change.EventID.String()), slog.String("err", err.Error()))
	return fmt.Errorf("build the change event %s: %w", change.EventID, err)
}
