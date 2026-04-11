package node

import (
	"context"

	"github.com/google/uuid"
)

// AutomationExecutor runs automation rules for a node event.
// The service layer calls Run after every node mutation.
// The no-op implementation is used until the real executor is built out.
type AutomationExecutor interface {
	Run(ctx context.Context, orgID, workspaceID uuid.UUID, nv *NodeValue, trigger AutomationTrigger) error
}

// NoopAutomationExecutor does nothing. Wired everywhere from day one so the
// service layer never needs to change when real automation is implemented.
type NoopAutomationExecutor struct{}

func (n *NoopAutomationExecutor) Run(_ context.Context, _, _ uuid.UUID, _ *NodeValue, _ AutomationTrigger) error {
	return nil
}
