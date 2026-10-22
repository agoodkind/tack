package auditintent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

type operationKey struct{}

// operationValue stores an encoded audit operation and the MCP tool slot on ctx
// when [WithOperation] attaches the operation. Its slot is nil when ctx does not
// have a tool slot.
type operationValue struct {
	operation json.RawMessage
	slot      *slot
}

// WithOperation attaches operation, an encoded audit operation in [json.RawMessage]
// form, to ctx under its own context key and returns the resulting context.
// It does not attach a tool slot. [Attached], [Stage], [StageMutation], [Build],
// [Pending], and [Commit] keep their behavior for the returned context.
func WithOperation(ctx context.Context, operation json.RawMessage) context.Context {
	current, _ := ctx.Value(slotKey{}).(*slot)
	return context.WithValue(ctx, operationKey{}, &operationValue{operation: operation, slot: current})
}

// Operation returns the encoded audit operation from the nearest [WithOperation]
// call on ctx. It returns nil when ctx does not have an operation or a later
// [WithSlot] call attached a nearer tool slot. The audit package reads that nearer
// slot instead.
func Operation(ctx context.Context) json.RawMessage {
	attached, _ := ctx.Value(operationKey{}).(*operationValue)
	current, _ := ctx.Value(slotKey{}).(*slot)
	if attached == nil || attached.slot != current {
		return nil
	}
	return attached.operation
}

// OperationID creates and stores a UUIDv7 ID when the nearest slot on ctx does not have one.
// It returns the stored ID, or [uuid.Nil] and an error if creation fails.
// It returns [uuid.Nil] without an error when ctx does not have a slot.
func OperationID(ctx context.Context) (uuid.UUID, error) {
	current, _ := ctx.Value(slotKey{}).(*slot)
	if current == nil {
		return uuid.Nil, nil
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.operationID != uuid.Nil {
		return current.operationID, nil
	}
	operationID, err := uuid.NewV7()
	if err != nil {
		slog.ErrorContext(ctx, "audit.intent_operation_id_failed", slog.String("tool", current.tool), slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("create the operation ID of tool %s: %w", current.tool, err)
	}
	current.operationID = operationID
	return operationID, nil
}
