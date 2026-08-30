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

// LegacyRow reports the row WriteLegacyRow appended.
type LegacyRow struct {
	// EventID identifies the appended row.
	EventID uuid.UUID
	// Shard names the per-(org, shard) chain the row extends.
	Shard int16
	// Seq is the row's position on that chain.
	Seq int64
	// EventTime is the instant stored on the row.
	EventTime time.Time
}

// LegacyRowInput names the org and actor the appended row belongs to.
type LegacyRowInput struct {
	// OrgID is the org whose chain the row extends and whose readers see it.
	OrgID uuid.UUID
	// ActorID is the user the row attributes the action to.
	ActorID uuid.UUID
	// EntityID is the node the recorded action touched.
	EntityID uuid.UUID
	// EventID identifies the row. The zero value asks for a fresh id; a caller
	// that needs the row on a particular (org, shard) chain picks one whose
	// shard matches, because the shard derives from the actor and event ids.
	EventID uuid.UUID
	// Action is the verb the row records. Empty writes mcp.tool_invoked, the
	// shape production holds in quantity.
	Action string
	// Tool names the MCP tool the row attributes the action to. Setting it also
	// writes the entity kind the pre-fix build used for a tool invocation,
	// mcp_tool, which is why those rows cannot name the node they touched.
	Tool string
}

// legacyEntityKind is the entity kind the pre-fix build stamped on a tool
// invocation. A row carrying it names no node, which is exactly the production
// shape TACK-455 has to reconstruct against.
const legacyEntityKind = "mcp_tool"

// WriteLegacyRow appends one ledger row in the shape the writer produced
// before the ledger stored outcomes: hash version 1, whose payload does not
// cover an outcome, and SQL NULL in the outcome, error, and extra columns.
//
// Every environment created after migration 006 has no such row, because its
// writer has always stored an outcome. Only production carries them, and the
// contract that both read tiers report a missing outcome as unrecorded cannot
// be proven against a ledger that holds none. This writes one so a testbed can
// carry the shape and the contract can be proven there (TACK-435).
//
// The row joins the real chain: it reads the head, hashes on top of it, and
// advances it, so chain verification covers it like any other row. The caller
// gates this on the data-generator target marker, and its command records
// through the audit choke-point, so fabricating the row is itself on the
// record.
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

// appendLegacyChainRow writes the row inside tx and returns its sequence. It
// shares the head protocol with appendChainRow, so a change to how the chain
// advances reaches both writers at once.
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
	// The outcome, error, and extra columns bind SQL NULL, which is what the
	// pre-006 writer left behind: those columns did not exist when it ran. A
	// nil byte slice and a nil string pointer bind as NULL.
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

// legacyEvent builds the event the legacy row records. It carries the shape a
// tool invocation had before the outcome column existed, which is what
// production holds in quantity.
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
