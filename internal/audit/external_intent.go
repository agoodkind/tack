package audit

import (
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/clock"
)

// ExternalIntent builds the pending format 2 intent event of one external
// operation on target in orgID. It keeps the actor, operation ID, parent
// operation ID, and start time of operation and uses a new event ID.
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
