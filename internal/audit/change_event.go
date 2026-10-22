package audit

import (
	"fmt"
	"log/slog"
	"time"
)

const (
	// TargetNodeTypeDefinition is the node type of a node target that
	// identifies a node type definition record.
	TargetNodeTypeDefinition = "node_type"
	// TargetNodeTypePropertyDefinition is the node type of a node target that
	// identifies a property definition record.
	TargetNodeTypePropertyDefinition = "property_def"
)

// ChangeEvent returns the format 2 change event that records change as one
// step of operation. The event uses the change event ID and target, the actor,
// context, and extra fields of operation, the completed outcome, and a delta
// with the complete old and new values. It returns an error when the change
// or the resulting event fails validation.
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
		OccurredAt: completedAt.UTC(), Extra: operation.Extra,
	}
	if err := ValidateEvent(event); err != nil {
		return Event{}, changeEventFailure(change, err)
	}
	return event, nil
}

// changeVerb selects the verb from the target and from which of the old and
// new values exist.
func changeVerb(change Change) Verb {
	created, deleted := len(change.Before) == 0, len(change.After) == 0
	if change.Target.Kind == TargetKindRelationship {
		return pickVerb(created, deleted, VerbRelationshipAdd, VerbRelationshipUpdate, VerbRelationshipRemove)
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
