// Package auditintent attaches the identity of one MCP tool call or act-as
// write to its request context.
//
// The MCP tool wrapper attaches a slot with the tool name and the actor. Each
// FoundationDB writer reads the slot through the audit package, writes one
// change event for each changed record in its own transaction, and marks the
// slot committed. The wrapper reads that mark and does not record the change
// again after the call (TACK-173).
//
// This package does not define an event shape. The storage adapter and the
// audit package both import it.
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

// Actor returns the caller of the slot, or the zero ID without a slot.
func Actor(ctx context.Context) uuid.UUID {
	current, _ := ctx.Value(slotKey{}).(*slot)
	if current == nil {
		return uuid.Nil
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.actor
}

// OperationID returns the UUIDv7 operation ID of the slot. The first call
// creates the ID, and every later call returns the same ID. It returns the
// zero ID without a slot.
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

// Committed reports whether a transaction in the request wrote change events.
func Committed(ctx context.Context) bool {
	current, _ := ctx.Value(slotKey{}).(*slot)
	if current == nil {
		return false
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.committed
}
