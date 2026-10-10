package search

import (
	"context"
	"time"
)

// Model states that ML Commons writes to the model record. Search compares
// them as engine values and never writes them.
const (
	ModelStateDeployed          = "DEPLOYED"
	ModelStatePartiallyDeployed = "PARTIALLY_DEPLOYED"
	ModelStateDeployFailed      = "DEPLOY_FAILED"
)

// ModelDeployment is the deployment state of the model that the serving
// index maps. ActiveTasks counts the DEPLOY_MODEL tasks of the model in
// CREATED or RUNNING.
// A task counts as active only within the deploy-task wait bound after its latest update.
type ModelDeployment struct {
	ModelID     string
	State       string
	LastUpdated time.Time
	ActiveTasks int
}

// Stuck reports whether ML Commons left the model PARTIALLY_DEPLOYED or
// DEPLOY_FAILED with no deploy task in CREATED or RUNNING.
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

// ModelRepairClaim is the result of one claim. Record is the stored record
// after the claim. Previous is the record before the claim, and
// PreviousFound is false when no record for the model existed. Replaced is
// the model ID of a record for another model that the claim deleted, or an
// empty string.
type ModelRepairClaim struct {
	Granted       bool
	Record        ModelRepairRecord
	Previous      ModelRepairRecord
	PreviousFound bool
	Replaced      string
}

// ModelRepairEngine reads the serving model deployment and deploys the model
// with no node IDs.
type ModelRepairEngine interface {
	ModelDeployment(ctx context.Context, index string) (ModelDeployment, error)
	StartModelDeploy(ctx context.Context, modelID string) (string, error)
	WaitModelDeploy(ctx context.Context, modelID, taskID string) error
}

// ModelRepairStore persists the one repair record. ClaimModelRepair counts
// one attempt. ReleaseModelRepair restores the record that a claim replaced
// when no other claim followed it. UncountModelRepair removes the attempt of
// an interrupted claim and keeps its claim time. It reports false and changes
// nothing when the stored record differs from the claim's record.
// ResetModelRepair deletes the record and returns it.
type ModelRepairStore interface {
	ClaimModelRepair(ctx context.Context, request ModelRepairRequest) (ModelRepairClaim, error)
	ReleaseModelRepair(ctx context.Context, claim ModelRepairClaim) error
	UncountModelRepair(ctx context.Context, claim ModelRepairClaim) (bool, error)
	ResetModelRepair(ctx context.Context) (ModelRepairRecord, bool, error)
}
