package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/clock"
)

// LegacyRow reports the row that [WriteLegacyRow] appended.
type LegacyRow struct {
	// EventID identifies the appended row.
	EventID uuid.UUID
	// Shard identifies the row's per-(org, shard) hash chain.
	Shard int16
	// Seq is the row's position on that chain.
	Seq int64
	// EventTime records the instant that the row stores.
	EventTime time.Time
}

// LegacyRowInput specifies the organization and actor for the appended row.
type LegacyRowInput struct {
	// OrgID identifies the organization that owns the chain and can read the row.
	OrgID uuid.UUID
	// ActorID identifies the user to whom the row attributes the action.
	ActorID uuid.UUID
	// EntityID identifies the node that the action affected.
	EntityID uuid.UUID
	// EventID identifies the row. The zero value requests a fresh ID. A caller
	// that needs a particular (org, shard) chain chooses an event ID that produces
	// the required shard from the actor and event IDs.
	EventID uuid.UUID
	// Action specifies the recorded verb. An empty value writes mcp.tool_invoked,
	// the verb in many historical production rows.
	Action string
	// Tool specifies the MCP tool that performed the action. A nonempty value
	// also selects mcp_tool, the entity kind that the earlier build used for tool
	// invocations. That kind does not identify the affected node.
	Tool string
}

// legacyEntityKind records the earlier tool-invocation entity kind. That kind
// does not identify a node. TACK-455 must reconstruct reference keys from
// historical production rows with this kind.
const legacyEntityKind = "mcp_tool"

// WriteLegacyRow appends a ledger row in the format that predates recorded
// outcomes.
//
// The row uses hash version 1, which does not hash an outcome, and SQL NULL in
// the outcome, error, and extra columns. Migration 006 added those columns.
// Production already contained rows from the earlier writer; environments
// created afterward did not naturally contain them. TACK-435 uses this writer
// to test that both read tiers report a missing outcome as unrecorded.
//
// The write extends and verifies with the real hash chain:
//
//  1. WriteLegacyRow reads the current chain head.
//  2. It hashes the new row using that head and advances the head.
//  3. Chain verification checks the appended row like other rows.
//
// The data-generator command checks its target marker before calling this
// function and records its own operation through the audit gate.
func WriteLegacyRow(ctx context.Context, pool *pgxpool.Pool, in LegacyRowInput) (LegacyRow, error) {
	if in.OrgID == uuid.Nil {
		return LegacyRow{}, fmt.Errorf("write legacy ledger row: org id required")
	}
	eventID := in.EventID
	if eventID == uuid.Nil {
		fresh, err := uuid.NewV7()
		if err != nil {
			slog.ErrorContext(ctx, "audit.legacy_row.id_failed", slog.String("err", err.Error()))
			return LegacyRow{}, fmt.Errorf("write legacy ledger row: new event id: %w", err)
		}
		eventID = fresh
	}
	row := LegacyRow{
		EventID:   eventID,
		Shard:     shardOf(in.ActorID, eventID),
		Seq:       0,
		EventTime: clock.Now().UTC().Truncate(time.Microsecond),
	}
	transaction, err := pool.Begin(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit.legacy_row.begin_failed", slog.String("err", err.Error()))
		return LegacyRow{}, fmt.Errorf("write legacy ledger row: begin: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	row.Seq, err = appendLegacyChainRow(ctx, transaction, in, row)
	if err != nil {
		return LegacyRow{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		slog.ErrorContext(ctx, "audit.legacy_row.commit_failed", slog.String("err", err.Error()))
		return LegacyRow{}, fmt.Errorf("write legacy ledger row: commit: %w", err)
	}
	return row, nil
}

// appendLegacyChainRow writes the row in tx and returns its sequence. It and
// appendChainRow use the shared chain-head read and write functions. Changes
// to those functions affect both writers.
func appendLegacyChainRow(
	ctx context.Context,
	tx pgx.Tx,
	in LegacyRowInput,
	row LegacyRow,
) (int64, error) {
	claimed, err := claimEventIdentity(ctx, tx, row.EventID)
	if err != nil {
		return 0, err
	}
	if !claimed {
		return 0, fmt.Errorf("write legacy ledger row: identity %s already projected", row.EventID)
	}
	head, err := readChainHead(ctx, tx, in.OrgID, row.Shard)
	if err != nil {
		return 0, err
	}
	seq := head.LastSeq + 1
	event := legacyEvent(in, row)
	contextJSON, err := json.Marshal(event.Context)
	if err != nil {
		slog.ErrorContext(ctx, "audit.legacy_row.context_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("write legacy ledger row: encode context: %w", err)
	}
	rowHash, err := hashRowForEvent(rowHashInput{
		Event: event, EventID: row.EventID, Shard: row.Shard, Seq: seq,
		PIIRef: uuid.Nil, ContextJSON: contextJSON, DeltaJSON: []byte("null"),
		LastHash: head.LastHash, Version: auditHashVersion1,
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit.legacy_row.hash_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("write legacy ledger row: hash: %w", err)
	}
	// The earlier writer ran before migration 006 added outcome, error, and
	// extra. These columns contain SQL NULL for its rows. A nil byte slice and a
	// nil string pointer bind as NULL.
	var absentOutcome *string
	var absentJSON []byte
	_, err = tx.Exec(ctx, `
		INSERT INTO audit.events (
			org_id, shard, event_time, event_id, seq,
			actor_id, actor_kind, action, outcome, entity_kind, entity_id,
			context, delta, pii_ref, prev_hash, row_hash, error, extra,
			hash_version, idempotency_key
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
			$12, $13, $14, $15, $16, $17, $18, $19, $20
		)
	`,
		in.OrgID, row.Shard, row.EventTime, row.EventID, seq,
		in.ActorID, actorKindCode(ActorUser), event.Verb, absentOutcome,
		event.Entity.Type, in.EntityID,
		contextJSON, []byte("null"), piiRefArg(uuid.Nil), head.LastHash, rowHash,
		absentJSON, absentJSON, auditHashVersion1, event.IdempotencyKey,
	)
	if err != nil {
		slog.ErrorContext(ctx, "audit.legacy_row.insert_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("write legacy ledger row: insert: %w", err)
	}
	if err := writeChainHead(ctx, tx, in.OrgID, row.Shard, head, seq, rowHash); err != nil {
		return 0, err
	}
	return seq, nil
}

// legacyEvent builds the event for a legacy row. It reproduces the
// tool-invocation format from before the outcome column, which historical
// production rows used in quantity.
func legacyEvent(in LegacyRowInput, row LegacyRow) Event {
	verb := string(VerbMCPToolInvoked)
	if in.Action != "" {
		verb = in.Action
	}
	entityKind := "node"
	if in.Tool != "" {
		entityKind = legacyEntityKind
	}
	return Event{
		Verb:    verb,
		EventID: row.EventID,
		Actor: Actor{
			Type: ActorUser, ID: in.ActorID, Email: "", Name: "", SessionID: "",
			IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: Entity{
			Type: entityKind, NodeType: "", ID: in.EntityID, Identifier: "", Name: "",
		},
		Context: EventContext{
			OrgID: in.OrgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: "", TraceID: "", Source: SourceMCP, Tool: in.Tool, RPC: "", Reason: "",
		},
		Delta: nil, Outcome: "", Error: nil,
		IdempotencyKey: "", OccurredAt: row.EventTime, Extra: nil,
	}
}
