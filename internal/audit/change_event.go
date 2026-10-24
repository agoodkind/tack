package audit

import (
	"fmt"
	"log/slog"
	"time"
)

const (
	// TargetNodeTypeDefinition is the Target.NodeType value for a node type definition record.
	TargetNodeTypeDefinition = "node_type"
	// TargetNodeTypePropertyDefinition is the Target.NodeType value for a property definition record.
	TargetNodeTypePropertyDefinition = "property_def"
	// TargetSequenceCounter is the Target.NodeType value of a resource target
	// for one FoundationDB sequence counter.
	TargetSequenceCounter = "sequence"
)

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
	case TargetNodeTypeDefinition:
		return pickVerb(created, deleted, VerbNodeTypeCreate, VerbNodeTypeUpdate, VerbNodeTypeDelete)
	case TargetNodeTypePropertyDefinition:
		return pickVerb(created, deleted, VerbPropertyDefCreate, VerbPropertyDefUpdate, VerbPropertyDefDelete)
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
