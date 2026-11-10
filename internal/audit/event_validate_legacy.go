package audit

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// validateLegacyEvent rejects format 0 events that set format 2 context
// fields, a format 2 outcome, or the unauthenticated actor kind.
func validateLegacyEvent(event Event) error {
	if hasCompleteContextField(event.Context) {
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

// hasCompleteContextField reports whether eventContext sets a context field
// that only format 2 events can set.
func hasCompleteContextField(eventContext EventContext) bool {
	operationFields := eventContext.OperationClass != "" || eventContext.Phase != "" ||
		eventContext.OperationID != uuid.Nil || eventContext.ParentOperationID != uuid.Nil
	timeFields := !eventContext.StartedAt.IsZero() || !eventContext.CompletedAt.IsZero()
	evidenceFields := eventContext.Target != nil || eventContext.Read != nil ||
		len(eventContext.CommittedSteps) != 0 || eventContext.Selection != nil
	requestFields := eventContext.HTTPMethod != "" || eventContext.RequestPath != ""
	return operationFields || timeFields || evidenceFields || requestFields
}
