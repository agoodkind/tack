package audit

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// OperationClass is the kind of work an operation performs.
type OperationClass string

const (
	// OperationRead classifies operations that return data without changing
	// state.
	OperationRead OperationClass = "read"
	// OperationDatabaseChange classifies operations that change records in a
	// Tack database.
	OperationDatabaseChange OperationClass = "database_change"
	// OperationExternal classifies operations that change state in a system
	// outside the Tack databases.
	OperationExternal OperationClass = "external"
)

// Phase is the step of an operation that one event records.
type Phase string

const (
	// PhaseIntent records an external operation before its effect, with the
	// pending outcome.
	PhaseIntent Phase = "intent"
	// PhaseResult records the terminal result of an operation.
	PhaseResult Phase = "result"
	// PhaseChange records one committed record change with its old and new
	// values.
	PhaseChange Phase = "change"
	// PhaseRead records one finished read.
	PhaseRead Phase = "read"
)

// allowedPhases maps operation classes to phases: read to read, database
// change to change or result, and external to intent or result.
// ValidateEvent rejects format 2 events if the map does not contain the
// class or does not list the phase for that class.
var allowedPhases = map[OperationClass][]Phase{
	OperationRead:           {PhaseRead},
	OperationDatabaseChange: {PhaseChange, PhaseResult},
	OperationExternal:       {PhaseIntent, PhaseResult},
}

const (
	// AuditFormatLegacy is the format of every historical row and every event
	// current writers produce. It is the zero value of
	// EventContext.AuditFormatVersion.
	AuditFormatLegacy uint16 = 0
	// AuditFormatComplete is the event format that requires evidence for the
	// event's operation class.
	AuditFormatComplete uint16 = 2
)

const (
	// TargetKindNode identifies one node and requires ID and NodeType.
	TargetKindNode = "node"
	// TargetKindRelationship identifies one relationship and requires SourceID,
	// TargetID, and RelationType.
	TargetKindRelationship = "relationship"
	// TargetKindResource identifies a resource address, such as an HTTP route,
	// and requires URI.
	TargetKindResource = "resource"
	// TargetKindExternal identifies a resource in an external system and
	// requires Reference.
	TargetKindExternal = "external"
)

// Target identifies the object an event acts on. Kind determines which
// fields the target requires.
type Target struct {
	Kind         string    `json:"kind,omitempty"`
	NodeType     string    `json:"node_type,omitempty"`
	Reference    string    `json:"reference,omitempty"`
	Name         string    `json:"name,omitempty"`
	URI          string    `json:"uri,omitempty"`
	RelationType string    `json:"relation_type,omitempty"`
	ID           uuid.UUID `json:"id,omitzero"`
	SourceID     uuid.UUID `json:"source_id,omitzero"`
	TargetID     uuid.UUID `json:"target_id,omitzero"`
}

// ReadEvidence records a finished read's result count, payload format, and
// payload SHA-256 digest. It also records the SHA-256 digest and part count
// of the ordered list of returned object identities. The ledger stores the
// digests instead of a response copy.
type ReadEvidence struct {
	Count          uint64 `json:"count,omitzero"`
	PayloadSHA256  string `json:"payload_sha256,omitempty"`
	PayloadFormat  string `json:"payload_format,omitempty"`
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
	ManifestParts  uint32 `json:"manifest_parts,omitzero"`
}

// OutcomeDetails records an operation's terminal outcome, the failure code
// and message for an unsuccessful outcome, and the steps that committed
// before the operation stopped.
type OutcomeDetails struct {
	Outcome        Outcome  `json:"outcome"`
	FailureCode    string   `json:"failure_code,omitempty"`
	FailureMessage string   `json:"failure_message,omitempty"`
	CommittedSteps []string `json:"committed_steps,omitempty"`
}

// Change records one changed record with its target and actual old and new
// values. Before is empty when the operation creates a record, and After is
// empty when it deletes a record.
type Change struct {
	EventID uuid.UUID       `json:"event_id"`
	Target  Target          `json:"target"`
	Before  json.RawMessage `json:"before,omitempty"`
	After   json.RawMessage `json:"after,omitempty"`
	Changed []string        `json:"changed,omitempty"`
}

// sha256HexLength is the length of a SHA-256 digest in lowercase
// hexadecimal.
const sha256HexLength = 64

func isTerminalOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomeCompleted, OutcomeRefused, OutcomeFailed, OutcomePartiallyCompleted, OutcomeIncomplete:
		return true
	case OutcomeOK, OutcomeError, OutcomePending, OutcomeUnrecorded:
		return false
	default:
		return false
	}
}

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
