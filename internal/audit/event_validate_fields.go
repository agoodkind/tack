package audit

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// sha256HexLength is the length of a SHA-256 digest in lowercase
// hexadecimal.
const sha256HexLength = 64

func validateActor(event Event) error {
	if event.Actor.Type == ActorUnauthenticated {
		if event.Actor.ID != uuid.Nil || event.Context.OrgID != systemOrgID {
			return errors.New("unauthenticated actor requires a nil actor ID and the system org")
		}
		return nil
	}
	if actorKindCode(event.Actor.Type) == 0 || event.Actor.ID == uuid.Nil {
		return errors.New("format 2 event requires a known actor kind and a non-nil actor ID")
	}
	return nil
}

func validateTarget(target Target) error {
	switch target.Kind {
	case TargetKindNode:
		if target.ID == uuid.Nil || target.NodeType == "" {
			return errors.New("node target requires ID and NodeType")
		}
	case TargetKindRelationship:
		if target.SourceID == uuid.Nil || target.TargetID == uuid.Nil || target.RelationType == "" {
			return errors.New("relationship target requires SourceID, TargetID, and RelationType")
		}
	case TargetKindResource:
		if target.URI == "" {
			return errors.New("resource target requires URI")
		}
	case TargetKindExternal:
		if target.Reference == "" {
			return errors.New("external target requires Reference")
		}
	default:
		return fmt.Errorf("target kind %q is unsupported", target.Kind)
	}
	return nil
}

func validateOutcomeFields(event Event) error {
	eventContext := event.Context
	terminal := isTerminalOutcome(event.Outcome)
	if terminal == eventContext.CompletedAt.IsZero() {
		return errors.New("terminal events require a completion time and pending events cannot set one")
	}
	if terminal && eventContext.CompletedAt.Before(eventContext.StartedAt) {
		return errors.New("completion time cannot precede the start time")
	}
	unsuccessful := terminal && event.Outcome != OutcomeCompleted
	if unsuccessful != (event.Error != nil) {
		return fmt.Errorf("outcome %q requires an error exactly when it is unsuccessful", event.Outcome)
	}
	if unsuccessful && event.Error.Code == "" {
		return errors.New("unsuccessful event error requires a code")
	}
	return validateCommittedSteps(event.Outcome, eventContext.CommittedSteps)
}

func validateCommittedSteps(outcome Outcome, steps []string) error {
	switch outcome {
	case OutcomePartiallyCompleted:
		if len(steps) == 0 {
			return errors.New("format 2 outcome partially_completed requires at least one committed step in CommittedSteps")
		}
		return nil
	case OutcomeCompleted, OutcomeRefused, OutcomeFailed:
		if len(steps) != 0 {
			return fmt.Errorf("format 2 outcome %q does not allow CommittedSteps", outcome)
		}
		return nil
	case OutcomeIncomplete, OutcomePending, OutcomeOK, OutcomeError, OutcomeUnrecorded:
		return nil
	default:
		return fmt.Errorf("audit does not define outcome %q", outcome)
	}
}

func validateReadEvidence(read ReadEvidence) error {
	if !isSHA256Hex(read.PayloadSHA256) || read.PayloadFormat == "" {
		return errors.New("read evidence requires a valid payload SHA-256 digest and a payload format")
	}
	needsManifest := read.Count > 0 || read.ManifestParts > 0 || read.ManifestSHA256 != ""
	if needsManifest && !isSHA256Hex(read.ManifestSHA256) {
		return errors.New("read evidence with returned identities requires a valid manifest SHA-256 digest")
	}
	return nil
}

func isSHA256Hex(value string) bool {
	if len(value) != sha256HexLength {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
