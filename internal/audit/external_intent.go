package audit

import (
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/clock"
)

// ExternalIntent builds a pending format 2 external-operation intent.
//
// The event targets target in orgID and copies the actor, operation ID,
// parent operation ID, and start time from operation. ExternalIntent assigns
// a new event ID.
//
// A caller can create the intent as follows:
//
//	intent := ExternalIntent(operation, verb, target, orgID)
//	fmt.Println(intent.Outcome, intent.Context.Phase)
//	// Output: pending intent
func ExternalIntent(operation Event, verb Verb, target Target, orgID uuid.UUID) Event {
	eventTarget := target
	intent := operation
	intent.Verb, intent.EventID, intent.OccurredAt = string(verb), uuid.Must(uuid.NewV7()), clock.Now().UTC()
	intent.Entity = Entity{Type: target.Kind, NodeType: target.NodeType, ID: target.ID, Identifier: target.Reference, Name: target.Name}
	intent.Context.OrgID = orgID
	intent.Context.AuditFormatVersion = AuditFormatComplete
	intent.Context.OperationClass, intent.Context.Phase = OperationExternal, PhaseIntent
	intent.Context.CompletedAt, intent.Context.Target = time.Time{}, &eventTarget
	intent.Context.Read, intent.Context.CommitVersion, intent.Context.CommittedSteps = nil, "", nil
	intent.Delta, intent.Outcome, intent.Error = nil, OutcomePending, nil
	intent.IdempotencyKey, intent.Protected = "", nil
	return intent
}
