package audit

import (
	"strings"

	"github.com/google/uuid"
)

// StepOperationID derives a stable step operation ID.
//
// The ID depends on parentOperationID and the target's complete identity.
// Repeated calls with those same values return the same ID.
//
// A caller can derive the ID as follows:
//
//	first := StepOperationID(parentOperationID, target)
//	retry := StepOperationID(parentOperationID, target)
//	fmt.Println(first == retry)
//	// Output: true
func StepOperationID(parentOperationID uuid.UUID, target Target) uuid.UUID {
	return uuid.NewSHA1(parentOperationID, []byte("step\x00"+targetKey(target)))
}

// StepIntentEventID derives a stable step intent event ID.
//
// The ID depends on [StepOperationID] for parentOperationID and target. The
// intent marker distinguishes the event ID from the step operation ID.
//
// A caller can derive the ID as follows:
//
//	first := StepIntentEventID(parentOperationID, target)
//	retry := StepIntentEventID(parentOperationID, target)
//	fmt.Println(first == retry)
//	// Output: true
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
