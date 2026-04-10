// Package temporal wires the Temporal SDK client, worker, workflows, and activities.
package temporal

import (
	"context"
	"fmt"

	"go.temporal.io/sdk/client"
	"github.com/google/uuid"
)

const TaskQueue = "tack"

// NewClient creates a Temporal client connected to the given hostPort (e.g. "localhost:7233").
func NewClient(hostPort string) (client.Client, error) {
	c, err := client.Dial(client.Options{HostPort: hostPort})
	if err != nil {
		return nil, fmt.Errorf("temporal client: %w", err)
	}
	return c, nil
}

// NodeCleanupScheduler implements node.NodeCleanupScheduler via Temporal.
// It enqueues a NodeCleanupWorkflow for every soft-deleted node.
type NodeCleanupScheduler struct {
	Client client.Client
}

func (s *NodeCleanupScheduler) Schedule(ctx context.Context, orgID, nodeID uuid.UUID) error {
	opts := client.StartWorkflowOptions{
		ID:        fmt.Sprintf("node-cleanup-%s", nodeID),
		TaskQueue: TaskQueue,
	}
	_, err := s.Client.ExecuteWorkflow(ctx, opts, NodeCleanupWorkflow, NodeCleanupInput{
		OrgID:  orgID,
		NodeID: nodeID,
	})
	return err
}

// NoopNodeCleanupScheduler satisfies node.NodeCleanupScheduler without doing anything.
// Used in tests and when Temporal is not configured.
type NoopNodeCleanupScheduler struct{}

func (s *NoopNodeCleanupScheduler) Schedule(_ context.Context, _, _ uuid.UUID) error { return nil }
