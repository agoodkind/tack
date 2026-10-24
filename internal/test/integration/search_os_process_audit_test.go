package integration

import (
	"context"
	"fmt"
	"time"

	"goodkind.io/tack/internal/audit"
)

// requireActualProcessAudit waits for one completed read event in the FoundationDB outbox for a read tool.
// For a tool that changes state, it waits for at least one completed change event in the FoundationDB outbox.
func requireActualProcessAudit(parent context.Context, harness *MCPHarness, requestID, tool string) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	if verb, covered := audit.ToolVerb(tool); covered && audit.IsRead(verb) {
		return requireActualProcessRead(ctx, harness, requestID, tool)
	}
	return requireActualProcessChange(ctx, harness, requestID, tool)
}

// requireActualProcessChange waits until the FoundationDB outbox contains at
// least one change event for requestID and tool. It requires every change and
// result event of the request to record tool and the completed outcome.
func requireActualProcessChange(ctx context.Context, harness *MCPHarness, requestID, tool string) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := harnessOutboxEvents(ctx, harness, requestID, audit.OperationDatabaseChange)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.Context.Tool != tool || event.Outcome != audit.OutcomeCompleted || event.Context.Phase != audit.PhaseChange {
				return fmt.Errorf("request %s has event %s with tool %s, phase %s, and outcome %s; expected tool %s, phase %s, and outcome %s",
					requestID, event.Verb, event.Context.Tool, event.Context.Phase, event.Outcome, tool, audit.PhaseChange, audit.OutcomeCompleted)
			}
		}
		if len(events) > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for the change events of request %s and tool %s: %w", requestID, tool, ctx.Err())
		case <-ticker.C:
		}
	}
}

// requireActualProcessRead waits until the FoundationDB outbox contains exactly
// one completed read event for requestID and tool.
func requireActualProcessRead(ctx context.Context, harness *MCPHarness, requestID, tool string) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := harnessReadEvents(ctx, harness, requestID)
		if err != nil {
			return err
		}
		if len(events) > 1 {
			return fmt.Errorf("expected one read event for request %s and tool %s, got %d", requestID, tool, len(events))
		}
		if len(events) == 1 {
			if events[0].Context.Tool != tool || events[0].Outcome != audit.OutcomeCompleted {
				return fmt.Errorf("read request %s has tool %s and outcome %s; expected tool %s and outcome %s",
					requestID, events[0].Context.Tool, events[0].Outcome, tool, audit.OutcomeCompleted)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for the read event for request %s and tool %s: %w", requestID, tool, ctx.Err())
		case <-ticker.C:
		}
	}
}
