package service

import (
	"context"
	"log/slog"
	"time"

	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// modelDeploy stores the task ID, the errors, and the duration of one repair deploy.
type modelDeploy struct {
	taskID            string
	startErr, waitErr error
	took              time.Duration
}

func (r *SearchModelRepair) deploy(
	ctx context.Context,
	claim searchdomain.ModelRepairClaim,
	state string,
	stuckFor time.Duration,
) error {
	modelID := claim.Record.ModelID
	sent := r.sendDeploy(ctx, claim, state, stuckFor)
	switch {
	case sent.startErr != nil:
		return r.deployFailed(ctx, claim, "", sent.startErr)
	case sent.waitErr != nil && ctx.Err() != nil:
		return r.deployInterrupted(ctx, claim, sent.taskID)
	case sent.waitErr != nil:
		return r.deployFailed(ctx, claim, sent.taskID, sent.waitErr)
	}

	telemetry.L(ctx).InfoContext(
		ctx,
		"search.model_repair.deploy_completed",
		slog.String("model_id", modelID),
		slog.String("task_id", sent.taskID),
		slog.Int("attempt", claim.Record.Attempts),
		slog.Duration("duration", sent.took),
	)

	return nil
}

// sendDeploy starts one deploy of the claimed model and waits for its task.
func (r *SearchModelRepair) sendDeploy(
	ctx context.Context,
	claim searchdomain.ModelRepairClaim,
	state string,
	stuckFor time.Duration,
) modelDeploy {
	modelID := claim.Record.ModelID
	taskID, err := r.ports.Engine.StartModelDeploy(ctx, modelID)
	if err != nil {
		return modelDeploy{taskID: "", startErr: err, waitErr: nil, took: 0}
	}

	telemetry.L(ctx).InfoContext(
		ctx,
		"search.model_repair.deploy_started",
		slog.String("model_id", modelID),
		slog.String("task_id", taskID),
		slog.Int("attempt", claim.Record.Attempts),
		slog.String("state", state),
		slog.Duration("stuck_for", stuckFor),
	)
	started := r.clock.Now()
	waitErr := r.ports.Engine.WaitModelDeploy(ctx, modelID, taskID)

	return modelDeploy{
		taskID:   taskID,
		startErr: nil,
		waitErr:  waitErr,
		took:     r.clock.Since(started),
	}
}
