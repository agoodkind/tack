package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// modelTargetPrefix is the prefix of each ML Commons model audit target reference.
const modelTargetPrefix = "opensearch/model/"

// modelRepairEvidence is the model state that a repair deploy records before
// the deploy, and the deploy task and its duration that it records after the
// deploy.
type modelRepairEvidence struct {
	ModelID         string `json:"model_id"`
	Index           string `json:"index"`
	State           string `json:"state"`
	Attempt         int    `json:"attempt"`
	StuckForSeconds int64  `json:"stuck_for_seconds"`
	TaskID          string `json:"task_id,omitempty"`
	DeploySeconds   int64  `json:"deploy_seconds,omitempty"`
}

// modelDeploy stores the task ID, the errors, and the duration of one repair deploy.
type modelDeploy struct {
	taskID            string
	startErr, waitErr error
	took              time.Duration
}

// deploy sends one deploy of the claimed model and waits for its task. It
// records the deploy as one service step through audit.RunServiceStep. The
// step intent records the stuck model state, and the step result records the
// deploy task and its duration or the failure. When the outbox does not save
// the intent, deploy does not send the deploy and releases the claim.
func (r *SearchModelRepair) deploy(ctx context.Context, index string, claim searchdomain.ModelRepairClaim, state string, stuckFor time.Duration) error {
	modelID := claim.Record.ModelID
	planned := modelRepairEvidence{
		ModelID: modelID, Index: index, State: state, Attempt: claim.Record.Attempts,
		StuckForSeconds: int64(stuckFor.Seconds()), TaskID: "", DeploySeconds: 0,
	}
	encoded, err := json.Marshal(planned)
	if err != nil {
		return modelRepairFailure(ctx, "encode the repair evidence of model "+modelID, errors.Join(err, r.release(ctx, claim)))
	}
	var sent modelDeploy
	target := audit.Target{
		Kind: audit.TargetKindExternal, NodeType: "", Reference: modelTargetPrefix + modelID, Name: "",
		URI: "", RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	recordErr := audit.RunServiceStep(ctx, r.ports.Audit.Outbox, r.ports.Audit.Actor, audit.VerbSearchModelRepair, target, encoded,
		func(ctx context.Context) error {
			sent = r.sendDeploy(ctx, claim, state, stuckFor)
			if sent.startErr != nil {
				return sent.startErr
			}
			return sent.waitErr
		},
		func(ctx context.Context) (json.RawMessage, error) {
			observed := planned
			observed.TaskID, observed.DeploySeconds = sent.taskID, int64(sent.took.Seconds())
			encoded, err := json.Marshal(observed)
			if err != nil {
				return nil, modelRepairFailure(ctx, "encode the repair evidence of model "+modelID, err)
			}
			return encoded, nil
		})
	switch {
	case sent.startErr != nil:
		return r.deployFailed(ctx, claim, "", sent.startErr)
	case sent.waitErr != nil && ctx.Err() != nil:
		return r.deployInterrupted(ctx, claim, sent.taskID)
	case sent.waitErr != nil:
		return r.deployFailed(ctx, claim, sent.taskID, sent.waitErr)
	case recordErr != nil && sent.taskID == "":
		return modelRepairFailure(ctx, "record the repair deploy of model "+modelID, errors.Join(recordErr, r.release(ctx, claim)))
	case recordErr != nil:
		return modelRepairFailure(ctx, "record the repair deploy of model "+modelID, recordErr)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.model_repair.deploy_completed", slog.String("model_id", modelID), slog.String("task_id", sent.taskID),
		slog.Int("attempt", claim.Record.Attempts), slog.Duration("duration", sent.took))
	return nil
}

// sendDeploy starts one deploy of the claimed model and waits for its task.
func (r *SearchModelRepair) sendDeploy(ctx context.Context, claim searchdomain.ModelRepairClaim, state string, stuckFor time.Duration) modelDeploy {
	modelID := claim.Record.ModelID
	taskID, err := r.ports.Engine.StartModelDeploy(ctx, modelID)
	if err != nil {
		return modelDeploy{taskID: "", startErr: err, waitErr: nil, took: 0}
	}
	telemetry.L(ctx).InfoContext(ctx, "search.model_repair.deploy_started", slog.String("model_id", modelID), slog.String("task_id", taskID),
		slog.Int("attempt", claim.Record.Attempts), slog.String("state", state), slog.Duration("stuck_for", stuckFor))
	started := r.clock.Now()
	waitErr := r.ports.Engine.WaitModelDeploy(ctx, modelID, taskID)
	return modelDeploy{taskID: taskID, startErr: nil, waitErr: waitErr, took: r.clock.Since(started)}
}
