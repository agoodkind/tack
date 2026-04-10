package temporal

import (
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// NewWorker creates a Temporal worker registered with all Tack workflows and activities.
func NewWorker(c client.Client, acts *Activities) worker.Worker {
	w := worker.New(c, TaskQueue, worker.Options{})
	w.RegisterWorkflow(NodeCleanupWorkflow)
	w.RegisterActivity(acts.DeleteNodeActivity)
	return w
}
