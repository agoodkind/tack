// Package auditintent attaches MCP tool slots and encoded audit operations
// to request contexts.
//
// The MCP tool wrapper attaches a slot with the tool name and actor.
// Each FoundationDB writer reads the slot through the audit package and writes
// one change event per changed record in the same transaction as the record change.
// The writer marks the slot committed. The wrapper skips its change event
// when the slot has that mark.
//
// Encoded operations use json.RawMessage.
package auditintent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/google/uuid"
)

type slotKey struct{}

// slot stores the state that the wrapper and the writers of one request share.
// A slot from WithOperation stores an encoded audit operation instead of a
// tool name and an actor.
type slot struct {
	mu          sync.Mutex
	tool        string
	actor       uuid.UUID
	operationID uuid.UUID
	operation   json.RawMessage
	committed   bool
}

// WithSlot attaches an empty slot to ctx for the named tool and the actor
// making the call.
func WithSlot(ctx context.Context, tool string, actor uuid.UUID) context.Context {
	return context.WithValue(ctx, slotKey{}, &slot{
		mu: sync.Mutex{}, tool: tool, actor: actor, operationID: uuid.Nil, operation: nil, committed: false,
	})
}

// WithOperation attaches a slot to ctx that stores the encoded audit
// operation. Writers under the returned context read the operation from this
// slot instead of from an earlier slot.
func WithOperation(ctx context.Context, operation json.RawMessage) context.Context {
	return context.WithValue(ctx, slotKey{}, &slot{
		mu: sync.Mutex{}, tool: "", actor: uuid.Nil, operationID: uuid.Nil, operation: operation, committed: false,
	})
}

// Operation returns the encoded audit operation of the slot on ctx. It
// returns nil when ctx does not have a slot or the slot stores a tool call.
func Operation(ctx context.Context) json.RawMessage {
	current, _ := ctx.Value(slotKey{}).(*slot)
	if current == nil {
		return nil
	}
	return current.operation
}

// Attached reports whether ctx has a slot.
func Attached(ctx context.Context) bool {
	current, _ := ctx.Value(slotKey{}).(*slot)
	return current != nil
}

// Tool returns the tool name of the slot, or "" without a slot.
func Tool(ctx context.Context) string {
	current, _ := ctx.Value(slotKey{}).(*slot)
	if current == nil {
		return ""
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.tool
}

// Actor returns the actor UUID from the nearest slot on ctx.
// It returns uuid.Nil when ctx does not have a slot or WithOperation created that slot.
func Actor(ctx context.Context) uuid.UUID {
	current, _ := ctx.Value(slotKey{}).(*slot)
	if current == nil {
		return uuid.Nil
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.actor
}

// OperationID creates and stores a UUIDv7 ID when the nearest slot on ctx does not have one.
// It returns the stored ID, or uuid.Nil and an error if creation fails.
// It returns uuid.Nil without an error when ctx does not have a slot.
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

// Commit marks the slot committed after a transaction wrote change events.
func Commit(ctx context.Context) {
	current, _ := ctx.Value(slotKey{}).(*slot)
	if current == nil {
		return
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	current.committed = true
}

// Committed reports whether Commit marked the nearest slot on ctx as committed.
// It returns false when ctx does not have a slot.
func Committed(ctx context.Context) bool {
	current, _ := ctx.Value(slotKey{}).(*slot)
	if current == nil {
		return false
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.committed
}
