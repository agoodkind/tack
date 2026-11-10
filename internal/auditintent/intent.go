// Package auditintent attaches MCP tool slots and encoded audit operations to
// request contexts.
//
// [WithSlot] attaches an MCP tool slot with the tool name and actor.
// [WithOperation] attaches a slot that stores an encoded audit operation.
//
// FoundationDB writers read the nearest slot through the audit package. Each
// writer writes one change event per changed record in the transaction that
// changes the record. Writers call [Commit] after that transaction commits.
//
// The MCP wrapper calls [Committed] on its own context to check the MCP tool
// slot it attached. The wrapper skips its change event when that slot reports
// a commit and the tool call succeeds.
//
// Encoded operations use [json.RawMessage].
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

// WithSlot attaches a fresh slot to ctx for the supplied tool and calling
// actor.
//
// The returned context uses this slot instead of any earlier slot. The slot
// starts with the following audit state:
//
//   - It does not store an encoded operation.
//   - It does not have an operation ID.
//   - It does not report a commit.
func WithSlot(ctx context.Context, tool string, actor uuid.UUID) context.Context {
	return context.WithValue(
		ctx,
		slotKey{},
		&slot{
			mu:          sync.Mutex{},
			tool:        tool,
			actor:       actor,
			operationID: uuid.Nil,
			operation:   nil,
			committed:   false,
		},
	)
}

// WithOperation attaches a slot to ctx for the encoded audit operation.
//
// Writers using the returned context read this operation instead of an earlier
// slot. The new slot starts with the following state:
//
//   - It does not specify a tool name or actor.
//   - It does not have an operation ID.
//   - It does not report a commit.
func WithOperation(ctx context.Context, operation json.RawMessage) context.Context {
	return context.WithValue(
		ctx,
		slotKey{},
		&slot{
			mu:          sync.Mutex{},
			tool:        "",
			actor:       uuid.Nil,
			operationID: uuid.Nil,
			operation:   operation,
			committed:   false,
		},
	)
}

// Operation returns the encoded audit operation from the nearest slot on ctx.
//
// The slot type determines the result:
//
//   - A slot from WithOperation returns the supplied operation.
//   - A slot from WithSlot returns nil.
//   - A context without a slot returns nil.
func Operation(ctx context.Context) json.RawMessage {
	current, _ := ctx.Value(slotKey{}).(*slot)
	if current == nil {
		return nil
	}

	return current.operation
}

// Attached reports whether ctx has an audit slot.
//
// Context lookup includes parent contexts. A child without an additional slot
// inherits its parent's slot:
//
//	parent := WithOperation(context.Background(), nil)
//	child, cancel := context.WithCancel(parent)
//	defer cancel()
//	fmt.Println(Attached(child)) // true
func Attached(ctx context.Context) bool {
	current, _ := ctx.Value(slotKey{}).(*slot)
	return current != nil
}

// Tool returns the tool name from the nearest slot on ctx.
//
// The slot type determines the result:
//
//   - A slot from WithSlot returns the supplied tool name.
//   - A slot from WithOperation returns an empty string.
//   - A context without a slot returns an empty string.
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
//
// The slot type determines the result:
//
//   - A slot from WithSlot returns the supplied actor UUID.
//   - A slot from WithOperation returns [uuid.Nil].
//   - A context without a slot returns [uuid.Nil].
func Actor(ctx context.Context) uuid.UUID {
	current, _ := ctx.Value(slotKey{}).(*slot)
	if current == nil {
		return uuid.Nil
	}

	current.mu.Lock()
	defer current.mu.Unlock()

	return current.actor
}

// OperationID returns the operation ID from the nearest slot on ctx.
//
// The function locks the slot while it checks or creates the ID:
//
//   - It returns an existing ID without creating another ID.
//   - It creates and stores a UUIDv7 when the slot does not have an ID.
//   - It returns [uuid.Nil] and an error if ID creation fails.
//   - It returns [uuid.Nil] without an error when ctx does not have a slot.
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
		slog.ErrorContext(
			ctx,
			"audit.intent_operation_id_failed",
			slog.String("tool", current.tool),
			slog.String("err", err.Error()),
		)

		return uuid.Nil, fmt.Errorf("create the operation ID of tool %s: %w", current.tool, err)
	}

	current.operationID = operationID

	return operationID, nil
}

// Commit marks the nearest slot on ctx as committed after a transaction writes
// change events.
//
// The mark applies only to the slot that ctx selects:
//
//   - A later Committed call on that slot returns true.
//   - A context without a slot does not change any state.
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
//
// A constructor on a child context selects a new slot without changing the
// parent's slot. Commit uses the slot from its argument:
//
//	parent := WithSlot(context.Background(), "tack_create_workspace", uuid.New())
//	child := WithOperation(parent, nil)
//	Commit(child)
//	fmt.Println(Committed(parent), Committed(child)) // false true
func Committed(ctx context.Context) bool {
	current, _ := ctx.Value(slotKey{}).(*slot)
	if current == nil {
		return false
	}

	current.mu.Lock()
	defer current.mu.Unlock()

	return current.committed
}
