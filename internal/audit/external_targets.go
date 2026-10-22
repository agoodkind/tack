package audit

import (
	"strings"

	"github.com/google/uuid"
)

// StepOperationID returns the stable operation ID of the step with target
// inside the operation parentOperationID.
func StepOperationID(parentOperationID uuid.UUID, target Target) uuid.UUID {
	return uuid.NewSHA1(parentOperationID, []byte("step\x00"+targetKey(target)))
}

// StepIntentEventID returns the stable event ID of the intent of the step
// with target inside the operation parentOperationID.
func StepIntentEventID(parentOperationID uuid.UUID, target Target) uuid.UUID {
	return uuid.NewSHA1(StepOperationID(parentOperationID, target), []byte("intent"))
}

// targetKey returns a string that identifies target within one operation.
func targetKey(target Target) string {
	return strings.Join([]string{
		target.Kind, target.NodeType, target.Reference, target.URI, target.RelationType,
		target.ID.String(), target.SourceID.String(), target.TargetID.String(),
	}, "\x00")
}

// targetLabel returns the readable name of a step target: the reference,
// URI, name, or ID.
func targetLabel(target Target) string {
	switch {
	case target.Reference != "":
		return target.Reference
	case target.URI != "":
		return target.URI
	case target.Name != "":
		return target.Name
	default:
		return target.ID.String()
	}
}
