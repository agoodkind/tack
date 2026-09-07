package auditintent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

// TestStagedEventIsPendingUntilCommittedAndThenConsumed pins the lifecycle
// the store relies on: a staged event stays pending across transaction
// retries, one commit consumes it, and the wrapper then sees it committed.
func TestStagedEventIsPendingUntilCommittedAndThenConsumed(t *testing.T) {
	actor := uuid.New()
	ctx := WithSlot(context.Background(), "tack_create_issue", actor)
	payload := json.RawMessage(`{"verb":"node.create"}`)

	if !Stage(ctx, payload) {
		t.Fatal("Stage must accept an event when a slot is attached")
	}
	if Committed(ctx) {
		t.Fatal("a staged event is not committed until the store says so")
	}
	for range 2 {
		pending, ok := Pending(ctx)
		if !ok || string(pending) != string(payload) {
			t.Fatalf("Pending = %q, %v; want the staged event on every retry", pending, ok)
		}
	}

	Commit(ctx)

	if _, ok := Pending(ctx); ok {
		t.Fatal("a committed event must not be written by a later transaction in the same request")
	}
	if !Committed(ctx) {
		t.Fatal("Committed must report the write")
	}
	if Tool(ctx) != "tack_create_issue" || Actor(ctx) != actor || !Attached(ctx) {
		t.Fatalf("Tool = %q, Actor = %s; want the tool and actor the slot was attached for", Tool(ctx), Actor(ctx))
	}
}

// TestNoSlotMeansNothingIsStagedOrCommitted pins the path every non-MCP
// caller takes: without a slot, staging is refused, nothing is pending, and a
// commit changes nothing, so operator commands and seeds keep their own
// recording.
func TestNoSlotMeansNothingIsStagedOrCommitted(t *testing.T) {
	ctx := context.Background()

	if Stage(ctx, json.RawMessage(`{}`)) {
		t.Fatal("Stage must refuse without a slot")
	}
	if _, ok := Pending(ctx); ok {
		t.Fatal("nothing can be pending without a slot")
	}
	Commit(ctx)
	if Committed(ctx) || Tool(ctx) != "" || Actor(ctx) != uuid.Nil || Attached(ctx) {
		t.Fatal("a context without a slot reports nothing committed, no tool, and no actor")
	}
}

// TestCommitWithoutAStagedEventLeavesTheSlotUncommitted pins that a store
// transaction which had nothing to write does not claim it recorded anything,
// so the wrapper still records that call.
func TestCommitWithoutAStagedEventLeavesTheSlotUncommitted(t *testing.T) {
	ctx := WithSlot(context.Background(), "tack_list_issues", uuid.New())

	Commit(ctx)

	if Committed(ctx) {
		t.Fatal("a transaction with no staged event must not mark the slot committed")
	}
}
