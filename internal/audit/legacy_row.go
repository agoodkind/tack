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
	// Shard identifies the per-(org, shard) chain.
	Shard int16
	// Seq is the row's position on that chain.
	Seq int64
	// EventTime is the instant stored on the row.
	EventTime time.Time
}

// LegacyRowInput selects the org, actor, and historical event fields.
type LegacyRowInput struct {
	// OrgID selects the chain's org and its authorized readers.
	OrgID uuid.UUID
	// ActorID identifies the user responsible for the recorded action.
	ActorID uuid.UUID
	// EntityID identifies the action's node target.
	EntityID uuid.UUID
	// EventID uses a fresh UUID when zero. Callers requiring a particular
	// shard must select an ID that hashes to that shard with ActorID.
	EventID uuid.UUID
	// Action selects the verb, defaulting to mcp.tool_invoked when empty.
	Action string
	// Tool identifies the invoked MCP tool. A nonempty value uses the historical
	// mcp_tool entity kind, which did not identify the affected product node.
	Tool string
}

// legacyEntityKind reproduces tool-invocation rows without a node identity
// for reference-key reconstruction testing (TACK-455).
const legacyEntityKind = "mcp_tool"

// WriteLegacyRow appends historical hash version 1 with SQL NULL outcome,
// error, and extra columns. Version 1 does not hash an outcome. This reproduces
// pre-migration-006 rows for testing OutcomeUnrecorded on newer environments
// (TACK-435). It extends the real chain with the shared head protocol.
// The caller must enforce the data-generator target marker and record the
// generating command through the shared operator audit gate.
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

// appendLegacyChainRow writes the row inside tx and returns its sequence using
// the same head-read and compare-and-swap protocol as appendChainRow.
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
		Event:       event,
		EventID:     row.EventID,
		Shard:       row.Shard,
		Seq:         seq,
		PIIRef:      uuid.Nil,
		ContextJSON: contextJSON,
		DeltaJSON:   []byte("null"),
		LastHash:    head.LastHash,
		Version:     auditHashVersion1,
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit.legacy_row.hash_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("write legacy ledger row: hash: %w", err)
	}

	// Bind SQL NULL for columns absent from the pre-006 schema, using nil
	// byte slices and a nil string pointer rather than JSON null values.
	var absentOutcome *string
	var absentJSON []byte
	_, err = tx.Exec(
		ctx,
		`
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
		in.OrgID,
		row.Shard,
		row.EventTime,
		row.EventID,
		seq,
		in.ActorID,
		actorKindCode(ActorUser),
		event.Verb,
		absentOutcome,
		event.Entity.Type,
		in.EntityID,
		contextJSON,
		[]byte("null"),
		piiRefArg(uuid.Nil),
		head.LastHash,
		rowHash,
		absentJSON,
		absentJSON,
		auditHashVersion1,
		event.IdempotencyKey,
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

// legacyEvent constructs a historical tool-invocation event without outcome fields.
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
			Type:          ActorUser,
			ID:            in.ActorID,
			Email:         "",
			Name:          "",
			SessionID:     "",
			IP:            "",
			UserAgent:     "",
			RequestID:     "",
			APITokenLabel: "",
		},
		Entity: Entity{
			Type:       entityKind,
			NodeType:   "",
			ID:         in.EntityID,
			Identifier: "",
			Name:       "",
		},
		Context: EventContext{
			OrgID:       in.OrgID,
			WorkspaceID: uuid.Nil,
			ScopeID:     uuid.Nil,
			ParentID:    uuid.Nil,
			RequestID:   "",
			TraceID:     "",
			Source:      SourceMCP,
			Tool:        in.Tool,
			RPC:         "",
			Reason:      "",
		},
		Delta:          nil,
		Outcome:        "",
		Error:          nil,
		IdempotencyKey: "",
		OccurredAt:     row.EventTime,
		Extra:          nil,
	}
}
