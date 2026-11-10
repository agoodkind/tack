package search

import (
	"context"
	"time"
)

// ML Commons writes these model states. Search compares the engine values
// without writing them.
const (
	ModelStateDeployed          = "DEPLOYED"
	ModelStatePartiallyDeployed = "PARTIALLY_DEPLOYED"
	ModelStateDeployFailed      = "DEPLOY_FAILED"
)

// ModelDeployment records the deployment state of the serving index's
// model. ActiveTasks counts the model's DEPLOY_MODEL tasks in CREATED or
// RUNNING only while their latest update remains within the deploy-task
// wait bound.
type ModelDeployment struct {
	ModelID     string
	State       string
	LastUpdated time.Time
	ActiveTasks int
}

// Stuck reports whether a failed model deployment lacks an active task.
//
// The method checks the deployment state and ActiveTasks together.
//
//   - PARTIALLY_DEPLOYED or DEPLOY_FAILED with zero ActiveTasks returns true.
//   - Every other state or a positive active-task count returns false.
func (d ModelDeployment) Stuck() bool {
	failed := d.State == ModelStatePartiallyDeployed || d.State == ModelStateDeployFailed
	return failed && d.ActiveTasks == 0
}

// ModelRepairRecord is the repair state of one stuck episode. Attempts
// counts the deploys that the repair sent in the episode, and LastAttempt is
// the claim time of the newest one.
type ModelRepairRecord struct {
	ModelID        string
	EpisodeStarted time.Time
	Attempts       int
	LastAttempt    time.Time
}

// ModelRepairRequest asks for one repair attempt for ModelID at Now. The
// store refuses the attempt after MaxAttempts attempts in the episode or
// within Spacing of the previous attempt.
type ModelRepairRequest struct {
	ModelID     string
	Now         time.Time
	Spacing     time.Duration
	MaxAttempts int
}

// ModelRepairClaim records a repair claim's outcome. Record stores the
// state after the claim; Previous stores the state before it. PreviousFound
// is false when the model did not have a record. Replaced contains the ID
// of another model after the claim deletes that model's record, or an empty
// string.
type ModelRepairClaim struct {
	Granted       bool
	Record        ModelRepairRecord
	Previous      ModelRepairRecord
	PreviousFound bool
	Replaced      string
}

// ModelRepairEngine reads the serving model deployment and starts model
// deployment without specifying node IDs.
type ModelRepairEngine interface {
	ModelDeployment(ctx context.Context, index string, now time.Time) (ModelDeployment, error)
	StartModelDeploy(ctx context.Context, modelID string) (string, error)
	WaitModelDeploy(ctx context.Context, modelID, taskID string) error
}

// ModelRepairStore persists one repair record. ClaimModelRepair counts one
// attempt. ReleaseModelRepair restores the replaced record only when
// another claim has not followed it. UncountModelRepair removes an
// interrupted claim's attempt count and preserves its claim time. It returns
// false without changing the record when the stored record differs from the
// claim's record. ResetModelRepair deletes and returns the record.
type ModelRepairStore interface {
	ClaimModelRepair(ctx context.Context, request ModelRepairRequest) (ModelRepairClaim, error)
	ReleaseModelRepair(ctx context.Context, claim ModelRepairClaim) error
	UncountModelRepair(ctx context.Context, claim ModelRepairClaim) (bool, error)
	ResetModelRepair(ctx context.Context) (ModelRepairRecord, bool, error)
}
