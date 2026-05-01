// Package audit defines the audit ledger contract: a single Recorder that
// every state change and every meaningful read funnels through. Storage
// backends (Yugabyte, FDB-intent, in-memory) live in sibling files.
//
// The recorder layer is dual-track with telemetry: every Record call still
// emits a structured slog line. Audit is for legal evidence; slog is for
// operations. They are independent and both required.
package audit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Recorder accepts one audit event and either persists it (production) or
// captures it (tests). Implementations are responsible for hash chaining,
// sequence allocation, and durability guarantees appropriate to the verb
// class (state-change verbs commit synchronously with the operation;
// read verbs may batch through a WAL).
type Recorder interface {
	Record(ctx context.Context, ev Event) error
}

// Event is the canonical record shape. Field names match the on-disk JSONB
// columns in audit.events so callers can serialize without translation.
type Event struct {
	Verb           string          `json:"verb"`
	Actor          Actor           `json:"actor"`
	Entity         Entity          `json:"entity"`
	Context        EventContext    `json:"context"`
	Delta          *Delta          `json:"delta,omitempty"`
	Outcome        Outcome         `json:"outcome"`
	Error          *EventError     `json:"error,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	OccurredAt     time.Time       `json:"occurred_at"`
	Extra          json.RawMessage `json:"extra,omitempty"`
}

// Actor is who triggered the event. PII fields (Email, Name, IP, UA) are
// snapshots at event time; GDPR redaction lives in audit.pii (see TACK-178).
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
)

// Entity is the target of the event. NodeType is the tack-specific kind
// (issue, project, ...) when Type is "node".
type Entity struct {
	Type       string    `json:"type"`
	NodeType   string    `json:"node_type,omitempty"`
	ID         uuid.UUID `json:"id"`
	Identifier string    `json:"identifier,omitempty"`
	Name       string    `json:"name,omitempty"`
}

// EventContext carries scope and provenance. OrgID is mandatory; the recorder
// uses it for sharding, RLS, and chain isolation.
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
}

type Source string

const (
	SourceMCP       Source = "mcp"
	SourceRPC       Source = "rpc"
	SourceSystem    Source = "system"
	SourceMigration Source = "migration"
	SourceSeed      Source = "seed"
)

// Delta captures before/after for state-change verbs. Read verbs leave it nil.
type Delta struct {
	Before  json.RawMessage `json:"before,omitempty"`
	After   json.RawMessage `json:"after,omitempty"`
	Changed []string        `json:"changed,omitempty"`
}

type Outcome string

const (
	OutcomeOK    Outcome = "ok"
	OutcomeError Outcome = "error"
)

type EventError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
