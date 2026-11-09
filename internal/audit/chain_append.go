package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// errChainConflict signals that a concurrent writer advanced the same
// (org, shard) chain head between this writer's head read and its head write.
// The caller must roll back the transaction and retry the whole batch.
var errChainConflict = errors.New("audit chain head advanced concurrently")

// chainAppendInput contains the fields appendChainRow writes for one event.
type chainAppendInput struct {
	Event       Event
	EventID     uuid.UUID
	Shard       int16
	PIIRef      uuid.UUID
	ContextJSON []byte
	DeltaJSON   []byte
}

// chainAppendResult reports the sequence and hashes the append produced.
type chainAppendResult struct {
	Seq      int64
	PrevHash []byte
	RowHash  []byte
}

// claimEventIdentity records the event ID for projection and reports whether
// this call claimed it. The claim shares the append transaction, and rolling
// back that transaction releases the claim.
//
// The partitioned ledger's unique index includes event_time and rejects a
// duplicate only at the same instant. Reconstruction preserves event IDs but
// assigns new times. Before the identity claim, reconstruction inserted its
// history twice (TACK-451). Each event ID must produce only one ledger row.
func claimEventIdentity(ctx context.Context, tx pgx.Tx, eventID uuid.UUID) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO audit.projected_events (event_id)
		VALUES ($1)
		ON CONFLICT (event_id) DO NOTHING
	`, eventID)
	if err != nil {
		slog.ErrorContext(ctx, "audit.chain.identity_claim_failed",
			slog.String("event_id", eventID.String()), slog.String("err", err.Error()))
		return false, fmt.Errorf("claim event identity %s: %w", eventID, err)
	}
	return tag.RowsAffected() == 1, nil
}

// appendChainRow inserts an event into audit.events and advances its
// (org, shard) head in audit.chain_heads. The head update compares last_seq
// with the value this call read. Concurrent writers cannot both assign the
// same seq. A conflicting update affects zero rows under read committed or
// raises a serialization error under repeatable read. The caller retries both.
//
// The function returns errAlreadyProjected for an existing ledger event ID
// and errChainConflict when another writer advances the head first.
func appendChainRow(ctx context.Context, tx pgx.Tx, in chainAppendInput) (chainAppendResult, error) {
	// The function validates each event before claiming its identity. The
	// consumer stores rejected events in the dead-letter table. Only format 0
	// events without an outcome default to ok.
	if err := ValidateEvent(in.Event); err != nil {
		slog.ErrorContext(ctx, "audit.chain.event_invalid",
			slog.String("event_id", in.EventID.String()), slog.String("err", err.Error()))
		return chainAppendResult{}, fmt.Errorf("%w: %w", errMalformedPayload, err)
	}
	if in.Event.Outcome == "" && in.Event.Context.AuditFormatVersion == AuditFormatLegacy {
		in.Event.Outcome = OutcomeOK
	}
	claimed, err := claimEventIdentity(ctx, tx, in.EventID)
	if err != nil {
		return chainAppendResult{}, err
	}
	if !claimed {
		return chainAppendResult{}, errAlreadyProjected
	}
	head, err := readChainHead(ctx, tx, in.Event.Context.OrgID, in.Shard)
	if err != nil {
		return chainAppendResult{}, err
	}
	lastHash := head.LastHash
	seq := head.LastSeq + 1

	rowHash, err := hashRowForEvent(rowHashInput{
		Event: in.Event, EventID: in.EventID, Shard: in.Shard, Seq: seq,
		PIIRef: in.PIIRef, ContextJSON: in.ContextJSON, DeltaJSON: in.DeltaJSON,
		LastHash: lastHash, Version: auditHashVersionCurrent,
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit.chain.hash_failed", slog.String("err", err.Error()))
		return chainAppendResult{}, fmt.Errorf("chain hash: %w", err)
	}

	// An action without an error stores SQL NULL. The nullable column permits
	// IS NULL checks for absent errors; a JSON null does not satisfy those checks.
	// hashRowForEvent hashes the typed event before column encoding. The column's
	// representation of an absent error does not change that hash.
	var errorJSON []byte
	if in.Event.Error != nil {
		encoded, err := json.Marshal(in.Event.Error)
		if err != nil {
			slog.ErrorContext(ctx, "audit.chain.error_marshal_failed", slog.String("err", err.Error()))
			return chainAppendResult{}, fmt.Errorf("event error marshal: %w", err)
		}
		errorJSON = encoded
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO audit.events (
			org_id, shard, event_time, event_id, seq,
			actor_id, actor_kind, action, outcome, entity_kind, entity_id,
			context, delta, pii_ref, prev_hash, row_hash, error, extra,
			hash_version, idempotency_key
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
			$12, $13, $14, $15, $16, $17, $18, $19, $20
		)
		ON CONFLICT (event_id, event_time) DO NOTHING
	`,
		// The insert truncates event_time to microseconds before binding it.
		// The stored value matches the version 3 hash input without relying on the
		// driver's rounding.
		in.Event.Context.OrgID, in.Shard, in.Event.OccurredAt.UTC().Truncate(time.Microsecond), in.EventID, seq,
		in.Event.Actor.ID, actorKindCode(in.Event.Actor.Type), in.Event.Verb, in.Event.Outcome,
		in.Event.Entity.Type, in.Event.Entity.ID,
		in.ContextJSON, in.DeltaJSON, piiRefArg(in.PIIRef), lastHash, rowHash,
		errorJSON, nullableJSON(in.Event.Extra), auditHashVersionCurrent, in.Event.IdempotencyKey,
	)
	if err != nil {
		slog.ErrorContext(ctx, "audit.chain.event_insert_failed", slog.String("err", err.Error()))
		return chainAppendResult{}, fmt.Errorf("event insert: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return chainAppendResult{}, errAlreadyProjected
	}

	if err := writeChainHead(ctx, tx, in.Event.Context.OrgID, in.Shard, head, seq, rowHash); err != nil {
		return chainAppendResult{}, err
	}

	return chainAppendResult{Seq: seq, PrevHash: lastHash, RowHash: rowHash}, nil
}

func nullableJSON(value json.RawMessage) []byte {
	if len(value) == 0 {
		return nil
	}
	return value
}

// isRetryableChainErr reports whether a fresh transaction should retry an
// append. It accepts errChainConflict and the PostgreSQL serialization and
// deadlock error codes that concurrent writers can return.
func isRetryableChainErr(err error) bool {
	if errors.Is(err, errChainConflict) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01"
	}
	return false
}
