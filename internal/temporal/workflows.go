package temporal

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"github.com/google/uuid"
)

type NodeCleanupInput struct {
	OrgID  uuid.UUID `json:"org_id"`
	NodeID uuid.UUID `json:"node_id"`
}

// NodeCleanupWorkflow deletes all FDB-resident data for a soft-deleted node.
// It is enqueued by NodeCleanupScheduler after a SQL soft-delete and retried
// automatically by Temporal on failure.
func NodeCleanupWorkflow(ctx workflow.Context, input NodeCleanupInput) error {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    5 * time.Minute,
			MaximumAttempts:    10,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)
	return workflow.ExecuteActivity(ctx, (*Activities).DeleteNodeActivity, input).Get(ctx, nil)
}
