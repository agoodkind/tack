package audit

import (
	"encoding/json"

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

// allowedPhases maps each operation class to its supported phases.
// [ValidateEvent] rejects format 2 events with an absent class or phase.
//
// The supported class and phase combinations are:
//
//   - Read operations use the read phase.
//   - Database changes use the change or result phase.
//   - External operations use the intent or result phase.
var allowedPhases = map[OperationClass][]Phase{
	OperationRead:           {PhaseRead},
	OperationDatabaseChange: {PhaseChange, PhaseResult},
	OperationExternal:       {PhaseIntent, PhaseResult},
}

const (
	// AuditFormatLegacy is the original event format and the zero value of
	// [EventContext.AuditFormatVersion]. Historical format 0 rows retain this
	// format; current writers can also produce [AuditFormatComplete] events.
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
	// OwnerID identifies the person who owns the protected values of this
	// target when that person differs from ID. JSON encoding omits a zero value.
	OwnerID uuid.UUID `json:"owner_id,omitzero" exhaustruct:"optional"`
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

// ReadSelection records the arguments that selected a read's result.
// Reference is the requested node reference. Criteria maps each other
// argument name to its compact JSON value.
type ReadSelection struct {
	Reference string                     `json:"reference,omitempty"`
	Criteria  map[string]json.RawMessage `json:"criteria,omitempty"`
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
	// Protected supplies the subject IDs and JSON values for protected
	// references in Before and After.
	Protected []ProtectedValue `json:"protected,omitempty" exhaustruct:"optional"`
}

func isTerminalOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomeCompleted,
		OutcomeRefused,
		OutcomeFailed,
		OutcomePartiallyCompleted,
		OutcomeIncomplete:
		return true
	case OutcomeOK, OutcomeError, OutcomePending, OutcomeUnrecorded:
		return false
	default:
		return false
	}
}
