package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
)

// failureRecordTimeout limits detached failure recording, and the remaining
// constants supply error codes and a fixed message for failed changes.
const (
	failureRecordTimeout   = 30 * time.Second
	failureCodeNotFound    = "not_found"
	failureCodeConflict    = "conflict"
	failureCodeInvalid     = "invalid_argument"
	failureCodeDatabase    = "database_error"
	failureMessageRollback = "the change transaction rolled back without committing"
)

// WriteChanges builds each change event with audit.ChangeEvent and sets its
// verb to operation.Verb. It validates and encodes each event before writing
// it to the operator outbox in tx, and returns the first error.
func WriteChanges(ctx context.Context, tx pgx.Tx, operation audit.Event, changes []audit.Change) error {
	completedAt := clock.Now().UTC()
	for _, change := range changes {
		event, err := audit.ChangeEvent(operation, change, completedAt)
		if err != nil {
			slog.ErrorContext(ctx, "audit.change.invalid", slog.String("err", err.Error()))
			return fmt.Errorf("validate audit change %s: %w", change.EventID, err)
		}
		event.Verb = operation.Verb
		if err := writeEvent(ctx, tx, event); err != nil {
			return err
		}
	}
	return nil
}

func failureEvent(operation audit.Event, target audit.Target, cause error) audit.Event {
	completedAt := clock.Now().UTC()
	event := operation
	event.EventID = uuid.Must(uuid.NewV7())
	event.Entity = targetEntity(target)
	event.Context.AuditFormatVersion = audit.AuditFormatComplete
	event.Context.OperationClass = audit.OperationDatabaseChange
	event.Context.Phase = audit.PhaseResult
	event.Context.CompletedAt = completedAt
	event.Context.Target = &target
	event.Delta = nil
	event.Protected = nil
	event.Outcome = audit.OutcomeFailed
	event.Error = &audit.EventError{Code: failureCode(cause), Message: failureMessageRollback}
	event.OccurredAt = completedAt
	return event
}

func targetEntity(target audit.Target) audit.Entity {
	return audit.Entity{Type: target.Kind, NodeType: target.NodeType, ID: target.ID, Identifier: "", Name: target.Name}
}

func failureCode(cause error) string {
	switch {
	case errors.Is(cause, domain.ErrNotFound):
		return failureCodeNotFound
	case errors.Is(cause, domain.ErrAlreadyExists):
		return failureCodeConflict
	case errors.Is(cause, domain.ErrInvalidArgument):
		return failureCodeInvalid
	default:
		return failureCodeDatabase
	}
}

func writeEvent(ctx context.Context, tx pgx.Tx, event audit.Event) error {
	if err := audit.ValidateEvent(event); err != nil {
		slog.ErrorContext(ctx, "audit.change.event_invalid", slog.String("err", err.Error()))
		return fmt.Errorf("validate audit event %s: %w", event.EventID, err)
	}
	encoded, err := audit.EncodeEvent(event, nil)
	if err != nil {
		slog.ErrorContext(ctx, "audit.change.encode_failed", slog.String("err", err.Error()))
		return fmt.Errorf("encode audit event %s: %w", event.EventID, err)
	}
	if err := audit.WriteEncodedOutboxTx(ctx, tx, encoded); err != nil {
		slog.ErrorContext(ctx, "audit.change.write_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write audit event %s: %w", event.EventID, err)
	}
	return nil
}

// changeTx runs work on pool with runChangeTx. If runChangeTx returns an
// error, changeTx records a failed result event for target and joins that
// error with any error from recording the event.
func changeTx(
	ctx context.Context,
	pool *pgxpool.Pool,
	operation audit.Event,
	target audit.Target,
	work func(pgx.Tx) error,
) error {
	err := runChangeTx(ctx, pool, work)
	if err == nil {
		return nil
	}
	recordErr := recordFailure(ctx, pool, operation, target, err)
	joined := errors.Join(err, recordErr)
	slog.ErrorContext(ctx, "audit.change.transaction_failed",
		slog.String("target_id", target.ID.String()), slog.String("err", joined.Error()))
	return joined
}

// recordFailure writes a failed result event for target in a separate
// transaction. It uses [context.WithoutCancel] and failureRecordTimeout to
// limit the write independently of caller cancellation. The event contains a
// fixed message and a code derived from cause without the raw database error.
func recordFailure(ctx context.Context, pool *pgxpool.Pool, operation audit.Event, target audit.Target, cause error) error {
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), failureRecordTimeout)
	defer cancel()
	event := failureEvent(operation, target, cause)
	err := pgx.BeginFunc(recordCtx, pool, func(tx pgx.Tx) error {
		return writeEvent(recordCtx, tx, event)
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit.change.failure_record_failed",
			slog.String("event_id", event.EventID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("record failed change for target %s: %w", target.ID, err)
	}
	return nil
}

// newChange calls audit.NewChange with a new UUIDv7 event ID, target, and the
// old and new JSON values. It logs and wraps any error with the target ID.
func newChange(ctx context.Context, target audit.Target, before, after json.RawMessage) (audit.Change, error) {
	change, err := audit.NewChange(uuid.Must(uuid.NewV7()), target, before, after)
	if err != nil {
		slog.ErrorContext(ctx, "audit.change.build_failed", slog.String("target_id", target.ID.String()), slog.String("err", err.Error()))
		return audit.Change{}, fmt.Errorf("build audit change for target %s: %w", target.ID, err)
	}
	return change, nil
}

// changeValue encodes a record as JSON for an audit change and returns nil when
// value is nil.
func changeValue[Value userValue | tokenValue | memberValue](ctx context.Context, value *Value) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		slog.ErrorContext(ctx, "audit.change.value_encode_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("encode audit change value: %w", err)
	}
	return encoded, nil
}
