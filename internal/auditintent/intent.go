// Package auditintent carries one staged audit event from the service layer
// into the FoundationDB transaction that commits the change it describes.
//
// The MCP tool wrapper attaches a slot to the request context. The service
// stages the event for the state change it is about to make. The store writes
// the staged event into the operator outbox inside the same transaction as
// the change, so the change and its ledger record commit together or not at
// all, and marks the slot committed. The wrapper reads that mark and skips the
// row it would otherwise record after the fact (TACK-173).
//
// This package holds no event shape: the payload is opaque JSON, so the
// storage adapter can import it without importing the audit package.
package auditintent

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/google/uuid"
)

type slotKey struct{}

// slot is the mutable state one request shares between the wrapper, the
// service, and the store. A pointer on the context lets each of them see what
// the others did, the same way the audit scope builder works.
type slot struct {
	mu        sync.Mutex
	tool      string
	actor     uuid.UUID
	payload   json.RawMessage
	committed bool
}

// WithSlot attaches an empty slot to ctx for the named tool and the actor
// making the call. The tool name is what the staged event records as its
// source, so the reconstruction that keys on it keeps working; the actor is
// the wrapper's identity for the caller, which the service never sees.
func WithSlot(ctx context.Context, tool string, actor uuid.UUID) context.Context {
	return context.WithValue(ctx, slotKey{}, &slot{
		mu: sync.Mutex{}, tool: tool, actor: actor, payload: nil, committed: false,
	})
}

// Attached reports whether ctx carries a slot, which is true only inside the
// MCP tool wrapper.
func Attached(ctx context.Context) bool {
	current, _ := ctx.Value(slotKey{}).(*slot)
	return current != nil
}

// Tool returns the tool name the slot was attached for, or "" without a slot.
func Tool(ctx context.Context) string {
	current, _ := ctx.Value(slotKey{}).(*slot)
	if current == nil {
		return ""
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.tool
}

// Actor returns the caller the slot was attached for, or the zero id without
// a slot.
func Actor(ctx context.Context) uuid.UUID {
	current, _ := ctx.Value(slotKey{}).(*slot)
	if current == nil {
		return uuid.Nil
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.actor
}

// Stage records the event the next state-changing transaction must commit
// with. It reports false when ctx carries no slot, which is every caller
// outside the MCP wrapper; those paths record the way they always have.
func Stage(ctx context.Context, payload json.RawMessage) bool {
	current, _ := ctx.Value(slotKey{}).(*slot)
	if current == nil || len(payload) == 0 {
		return false
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	current.payload = payload
	current.committed = false
	return true
}

// Pending returns the staged event a transaction must write, if any. It stays
// pending across transaction retries until Commit is called.
func Pending(ctx context.Context) (json.RawMessage, bool) {
	current, _ := ctx.Value(slotKey{}).(*slot)
	if current == nil {
		return nil, false
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	if len(current.payload) == 0 {
		return nil, false
	}
	return current.payload, true
}

// Commit marks the staged event committed and clears it, so a second
// transaction in the same request (a default child written after its parent,
// for example) does not write it again.
func Commit(ctx context.Context) {
	current, _ := ctx.Value(slotKey{}).(*slot)
	if current == nil {
		return
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	if len(current.payload) == 0 {
		return
	}
	current.payload = nil
	current.committed = true
}

// Committed reports whether a staged event was written with its change.
func Committed(ctx context.Context) bool {
	current, _ := ctx.Value(slotKey{}).(*slot)
	if current == nil {
		return false
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.committed
}
