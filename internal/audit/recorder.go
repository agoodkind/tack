// Package audit defines the audit ledger contract. Every state change and
// meaningful read must use Recorder. The package implements YugabyteDB,
// FoundationDB intent, and in-memory recording.
//
// Audit records provide legal evidence, and structured slog entries support
// operations. Both forms of recording are independent requirements.
// MemoryRecorder captures test events without emitting a slog entry.
package audit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Recorder accepts one audit event for durable production storage or test
// capture. Implementations must provide hash chaining, sequence allocation,
// and durability appropriate to the operation.
//
// State changes must commit their evidence atomically with the change. Reads
// may batch evidence through a write-ahead log, but they must admit the read
// durably and finalize its evidence before returning data.
type Recorder interface {
	Record(ctx context.Context, ev Event) error
}

// Event is the canonical audit record. Its JSON tags define the transport
// representation. The writer maps these values to audit.events columns and
// JSONB values.
type Event struct {
	Verb string `json:"verb"`
	// EventID identifies the event from the producer's recording call. The
	// Kafka partition key uses the shard derived from this ID and the actor ID.
	// The consumer reuses EventID for the chain row and idempotency index. The ID
	// must remain stable after recording; Kafka redelivery must reconstruct the
	// same row.
	EventID        uuid.UUID       `json:"event_id"`
	Actor          Actor           `json:"actor"`
	Entity         Entity          `json:"entity"`
	Context        EventContext    `json:"context"`
	Delta          *Delta          `json:"delta,omitempty"`
	Outcome        Outcome         `json:"outcome"`
	Error          *EventError     `json:"error,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	OccurredAt     time.Time       `json:"occurred_at"`
	Extra          json.RawMessage `json:"extra,omitempty"`
	// Protected supplies JSON values for delta references. Writers store these
	// values in audit.pii and omit them from ledger rows and row hashes.
	Protected []ProtectedValue `json:"protected,omitempty" exhaustruct:"optional"`
}

// Actor identifies who triggered the event. Email, Name, IP, and UserAgent
// record personal information at event time. GDPR redaction erases the
// corresponding audit.pii payloads (TACK-178).
type Actor struct {
	Type          ActorType `json:"type"`
	ID            uuid.UUID `json:"id"`
	Email         string    `json:"email,omitempty"`
	Name          string    `json:"name,omitempty"`
	SessionID     string    `json:"session_id,omitempty"`
	IP            string    `json:"ip,omitempty"`
	UserAgent     string    `json:"ua,omitempty"`
	RequestID     string    `json:"request_id,omitempty"`
	APITokenLabel string    `json:"api_token_label,omitempty"`
}

type ActorType string

const (
	ActorUser    ActorType = "user"
	ActorService ActorType = "service"
	ActorSystem  ActorType = "system"
	ActorToken   ActorType = "api_token"
	// ActorOperator identifies a human running a command against the deployment.
	// Product users acting through the API use a different actor type.
	ActorOperator ActorType = "operator"
	// ActorUnauthenticated identifies the actor of a request that failed before
	// authentication. The event uses a nil actor ID and the system org.
	ActorUnauthenticated ActorType = "unauthenticated"
)

// Entity identifies the event target. When Type is "node", NodeType records
// the Tack node kind, such as an issue or project.
type Entity struct {
	Type       string    `json:"type"`
	NodeType   string    `json:"node_type,omitempty"`
	ID         uuid.UUID `json:"id"`
	Identifier string    `json:"identifier,omitempty"`
	Name       string    `json:"name,omitempty"`
}

// EventContext records scope and provenance. OrgID is mandatory for sharding,
// row-level security, and chain isolation.
type EventContext struct {
	OrgID       uuid.UUID `json:"org_id"`
	WorkspaceID uuid.UUID `json:"workspace_id,omitempty"`
	ScopeID     uuid.UUID `json:"scope_id,omitempty"`
	ParentID    uuid.UUID `json:"parent_id,omitempty"`
	RequestID   string    `json:"request_id,omitempty"`
	TraceID     string    `json:"trace_id,omitempty"`
	Source      Source    `json:"source"`
	Tool        string    `json:"tool,omitempty"`
	RPC         string    `json:"rpc,omitempty"`
	Reason      string    `json:"reason,omitempty"`

	// APITokenID identifies the token on the authentication event that accepted
	// the request. The audit-consumer derives the token's last use from this
	// event; the app does not update the token table during the request
	// (TACK-502).
	//
	// The omitzero tag omits a zero UUID. encoding/json does not omit arrays with
	// omitempty. TACK-514 tracked zero IDs in other event contexts that changed
	// the bytes covered by existing row hashes.
	APITokenID uuid.UUID `json:"api_token_id,omitzero" exhaustruct:"optional"`

	// CommitVersion records the FoundationDB outbox versionstamp as 24 lowercase
	// hexadecimal characters. String order matches outbox commit order. The relay
	// sets this field only when it is empty. JSON encoding omits an empty value
	// and preserves the hashed context bytes of events without this field.
	CommitVersion string `json:"commit_version,omitzero" exhaustruct:"optional"`

	// Format 2 events set these fields. JSON encoding omits each zero value and
	// preserves the context bytes covered by format 0 row hashes.
	AuditFormatVersion uint16         `json:"audit_format_version,omitzero" exhaustruct:"optional"`
	OperationClass     OperationClass `json:"operation_class,omitzero" exhaustruct:"optional"`
	Phase              Phase          `json:"phase,omitzero" exhaustruct:"optional"`
	OperationID        uuid.UUID      `json:"operation_id,omitzero" exhaustruct:"optional"`
	ParentOperationID  uuid.UUID      `json:"parent_operation_id,omitzero" exhaustruct:"optional"`
	StartedAt          time.Time      `json:"started_at,omitzero" exhaustruct:"optional"`
	CompletedAt        time.Time      `json:"completed_at,omitzero" exhaustruct:"optional"`
	Target             *Target        `json:"target,omitempty" exhaustruct:"optional"`
	Read               *ReadEvidence  `json:"read,omitempty" exhaustruct:"optional"`
	CommittedSteps     []string       `json:"committed_steps,omitempty" exhaustruct:"optional"`
	// Selection records normalized read arguments. HTTPMethod and RequestPath
	// record the checked HTTP method and path. JSON encoding omits empty values
	// and preserves the context bytes covered by earlier row hashes.
	Selection   *ReadSelection `json:"selection,omitempty" exhaustruct:"optional"`
	HTTPMethod  string         `json:"http_method,omitempty" exhaustruct:"optional"`
	RequestPath string         `json:"request_path,omitempty" exhaustruct:"optional"`
}

type Source string

const (
	SourceMCP       Source = "mcp"
	SourceRPC       Source = "rpc"
	SourceSystem    Source = "system"
	SourceMigration Source = "migration"
	SourceSeed      Source = "seed"
	// SourceOperator identifies a product row that an operator command writes
	// on a user's behalf (TACK-424).
	SourceOperator Source = "operator"
)

// Delta records before and after values for state changes. Reads leave Delta
// nil.
type Delta struct {
	Before  json.RawMessage `json:"before,omitempty"`
	After   json.RawMessage `json:"after,omitempty"`
	Changed []string        `json:"changed,omitempty"`
}

type Outcome string

const (
	OutcomeOK    Outcome = "ok"
	OutcomeError Outcome = "error"
	// OutcomePending identifies an intent row that awaits its outcome row.
	OutcomePending Outcome = "pending"
	// OutcomeUnrecorded represents a historical SQL NULL from before the ledger
	// stored outcomes. Readers return this value. Writers must never store it.
	// It differs from OutcomeOK because the historical record does not establish
	// success.
	OutcomeUnrecorded Outcome = "unrecorded"

	// OutcomeCompleted is a format 2 terminal outcome for a finished operation.
	// A completed read has prepared its result.
	OutcomeCompleted Outcome = "completed"
	// OutcomeRefused is a format 2 terminal outcome when Tack rejected a request
	// before the operation ran. Authentication or authorization failure can
	// cause this rejection.
	OutcomeRefused Outcome = "refused"
	// OutcomeFailed is a format 2 terminal outcome when an operation stopped
	// and did not commit any effects.
	OutcomeFailed Outcome = "failed"
	// OutcomePartiallyCompleted is a format 2 terminal outcome when an operation
	// stopped after some of its steps committed.
	OutcomePartiallyCompleted Outcome = "partially_completed"
	// OutcomeIncomplete is a format 2 terminal outcome when the available
	// evidence cannot establish whether the operation finished.
	OutcomeIncomplete Outcome = "incomplete"
)

type EventError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
