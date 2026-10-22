package audit

import (
	"strings"

	"github.com/google/uuid"
)

// StepOperationID derives a stable step operation ID from parentOperationID
// and target.
func StepOperationID(parentOperationID uuid.UUID, target Target) uuid.UUID {
	return uuid.NewSHA1(parentOperationID, []byte("step\x00"+targetKey(target)))
}

// StepIntentEventID derives a stable step intent event ID from
// parentOperationID and target.
func StepIntentEventID(parentOperationID uuid.UUID, target Target) uuid.UUID {
	return uuid.NewSHA1(StepOperationID(parentOperationID, target), []byte("intent"))
}

func targetKey(target Target) string {
	return strings.Join([]string{
		target.Kind, target.NodeType, target.Reference, target.URI, target.RelationType,
		target.ID.String(), target.SourceID.String(), target.TargetID.String(),
	}, "\x00")
}

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
