package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// SearchModelRepairPorts are the engine, the repair record store, and the
// serving index reader of the model repair.
type SearchModelRepairPorts struct {
	Engine  searchdomain.ModelRepairEngine
	Records searchdomain.ModelRepairStore
	Index   searchdomain.ServingIndexReader
}

// SearchModelRepair deploys the serving model again when ML Commons leaves it
// PARTIALLY_DEPLOYED or DEPLOY_FAILED with no deploy task running. Native
// automatic redeploy stays enabled. The repair acts only after the stuck
// state is StuckAfter old, at most MaxAttempts times per stuck episode, and
// only while public search is enabled.
type SearchModelRepair struct {
	ports    SearchModelRepairPorts
	clock    clock.Clock
	settings config.SearchModelRepairSettings
	gates    SearchModelRepairGates
}

// SearchModelRepairGates are the process switches of the repair. Enabled is
// OPENSEARCH_MODEL_REPAIR_ENABLED, and PublicSearch is
// OPENSEARCH_PUBLIC_ENABLED.
type SearchModelRepairGates struct {
	Enabled      bool
	PublicSearch bool
}

// NewSearchModelRepair constructs the model repair check.
func NewSearchModelRepair(ports SearchModelRepairPorts, source clock.Clock, settings config.SearchModelRepairSettings, gates SearchModelRepairGates) *SearchModelRepair {
	return &SearchModelRepair{ports: ports, clock: source, settings: settings, gates: gates}
}

// Settings returns the repair settings.
func (r *SearchModelRepair) Settings() config.SearchModelRepairSettings { return r.settings }

// Enabled reports whether the process enables the repair loop.
func (r *SearchModelRepair) Enabled() bool { return r.gates.Enabled }

// Check reads the serving model deployment once. While public search is
// disabled, Check reads nothing and returns. A DEPLOYED model ends the stuck
// episode and deletes the repair record. A model stuck for StuckAfter
// receives one deploy when the repair record grants an attempt and a second
// read still finds it stuck. A failed read returns an error.
func (r *SearchModelRepair) Check(ctx context.Context) error {
	if !r.gates.PublicSearch {
		telemetry.L(ctx).DebugContext(ctx, "search.model_repair.public_disabled")
		return nil
	}
	index, err := r.ports.Index.ServingSearchIndex(ctx)
	if errors.Is(err, searchdomain.ErrNoServingIndex) {
		return nil
	}
	if err != nil {
		return modelRepairFailure(ctx, "read serving search index", err)
	}
	deployment, err := r.ports.Engine.ModelDeployment(ctx, index)
	if err != nil {
		return modelRepairFailure(ctx, "read model deployment of index "+index, err)
	}
	if deployment.State == searchdomain.ModelStateDeployed {
		return r.reset(ctx, deployment.ModelID)
	}
	stuckFor := r.clock.Since(deployment.LastUpdated)
	if !deployment.Stuck() || stuckFor < r.settings.StuckAfter {
		telemetry.L(ctx).DebugContext(ctx, "search.model_repair.waiting", slog.String("model_id", deployment.ModelID),
			slog.String("state", deployment.State), slog.Int("active_tasks", deployment.ActiveTasks), slog.Duration("stuck_for", stuckFor))
		return nil
	}
	claim, err := r.ports.Records.ClaimModelRepair(ctx, searchdomain.ModelRepairRequest{
		ModelID: deployment.ModelID, Now: r.clock.Now().UTC(), Spacing: r.settings.AttemptSpacing, MaxAttempts: r.settings.MaxAttempts,
	})
	if err != nil {
		return modelRepairFailure(ctx, "claim repair of model "+deployment.ModelID, err)
	}
	if claim.Replaced != "" {
		telemetry.L(ctx).InfoContext(ctx, "search.model_repair.record_replaced", slog.String("old_model_id", claim.Replaced),
			slog.String("model_id", deployment.ModelID))
	}
	if !claim.Granted {
		telemetry.L(ctx).DebugContext(ctx, "search.model_repair.claim_refused", slog.String("model_id", deployment.ModelID),
			slog.Int("attempts", claim.Record.Attempts), slog.Duration("last_attempt_age", r.clock.Since(claim.Record.LastAttempt)))
		return nil
	}
	return r.attempt(ctx, index, claim, stuckFor)
}

// attempt reads the deployment again and sends one deploy when the model is
// still stuck. Otherwise it releases the claim without counting it.
func (r *SearchModelRepair) attempt(ctx context.Context, index string, claim searchdomain.ModelRepairClaim, stuckFor time.Duration) error {
	modelID := claim.Record.ModelID
	again, err := r.ports.Engine.ModelDeployment(ctx, index)
	if err != nil {
		failure := modelRepairFailure(ctx, "read model deployment of index "+index+" again", err)
		if releaseErr := r.release(ctx, claim); releaseErr != nil {
			return releaseErr
		}
		return failure
	}
	if !again.Stuck() || again.ModelID != modelID {
		if releaseErr := r.release(ctx, claim); releaseErr != nil {
			return releaseErr
		}
		telemetry.L(ctx).InfoContext(ctx, "search.model_repair.attempt_aborted", slog.String("model_id", modelID),
			slog.String("state", again.State), slog.Int("active_tasks", again.ActiveTasks))
		return nil
	}
	taskID, err := r.ports.Engine.StartModelDeploy(ctx, modelID)
	if err != nil {
		return r.deployFailed(ctx, claim, "", err)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.model_repair.deploy_started", slog.String("model_id", modelID), slog.String("task_id", taskID),
		slog.Int("attempt", claim.Record.Attempts), slog.String("state", again.State), slog.Duration("stuck_for", stuckFor))
	started := r.clock.Now()
	if err := r.ports.Engine.WaitModelDeploy(ctx, modelID, taskID); err != nil {
		if ctx.Err() != nil {
			return r.deployInterrupted(ctx, claim, taskID)
		}
		return r.deployFailed(ctx, claim, taskID, err)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.model_repair.deploy_completed", slog.String("model_id", modelID), slog.String("task_id", taskID),
		slog.Int("attempt", claim.Record.Attempts), slog.Duration("duration", r.clock.Since(started)))
	return nil
}

// deployFailed logs one failed attempt. The attempt stays counted. The last
// allowed attempt also logs that the episode is exhausted.
func (r *SearchModelRepair) deployFailed(ctx context.Context, claim searchdomain.ModelRepairClaim, taskID string, err error) error {
	record := claim.Record
	wrapped := fmt.Errorf("repair deploy %d of model %s, task %q: %w", record.Attempts, record.ModelID, taskID, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.model_repair.deploy_failed", slog.String("model_id", record.ModelID),
		slog.String("task_id", taskID), slog.Int("attempt", record.Attempts), slog.String("err", wrapped.Error()))
	if record.Attempts >= r.settings.MaxAttempts {
		telemetry.L(ctx).ErrorContext(ctx, "search.model_repair.attempts_exhausted", slog.String("model_id", record.ModelID),
			slog.Int("attempts", record.Attempts), slog.Time("episode_started", record.EpisodeStarted))
	}
	return wrapped
}

func (r *SearchModelRepair) release(ctx context.Context, claim searchdomain.ModelRepairClaim) error {
	if err := r.ports.Records.ReleaseModelRepair(ctx, claim); err != nil {
		return modelRepairFailure(ctx, "release repair of model "+claim.Record.ModelID, err)
	}
	return nil
}

// reset deletes the repair record after a DEPLOYED read and logs how the
// episode ended.
func (r *SearchModelRepair) reset(ctx context.Context, modelID string) error {
	record, found, err := r.ports.Records.ResetModelRepair(ctx)
	if err != nil {
		return modelRepairFailure(ctx, "reset repair of model "+modelID, err)
	}
	if !found {
		return nil
	}
	if record.ModelID != modelID {
		telemetry.L(ctx).InfoContext(ctx, "search.model_repair.record_replaced", slog.String("old_model_id", record.ModelID),
			slog.String("model_id", modelID), slog.Int("attempts", record.Attempts))
		return nil
	}
	telemetry.L(ctx).InfoContext(ctx, "search.model_repair.episode_reset", slog.String("model_id", modelID),
		slog.Int("attempts", record.Attempts), slog.Duration("episode_duration", r.clock.Since(record.EpisodeStarted)))
	return nil
}

// modelRepairFailure logs one failed repair step at Error and returns it
// wrapped.
func modelRepairFailure(ctx context.Context, operation string, err error) error {
	wrapped := fmt.Errorf("search model repair: %s: %w", operation, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.model_repair.check_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
