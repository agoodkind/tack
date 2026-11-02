package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

const (
	// deployBreakerWindow bounds the deploy retries after the ML Commons
	// memory circuit breaker rejects a deploy task.
	deployBreakerWindow = 3 * time.Minute
	// deployBreakerInterval spaces those retries.
	deployBreakerInterval = 5 * time.Second
	// breakerOpenText is the ML Commons message of a rejection by an open
	// memory circuit breaker.
	breakerOpenText = "Circuit Breaker is open"
	// deployedRecordInterval spaces the reads of the model record.
	deployedRecordInterval = time.Second
)

// modelTaskFailedError reports an ML Commons task that ended in a failed
// state.
type modelTaskFailedError struct {
	taskID string
	state  string
	detail string
}

func (e modelTaskFailedError) Error() string {
	return fmt.Sprintf("OpenSearch ML task %s ended in state %q: %s", e.taskID, e.state, e.detail)
}

// breakerOpen reports whether err is a task failure from an open ML Commons
// memory circuit breaker.
func breakerOpen(err error) bool {
	var failure modelTaskFailedError
	return errors.As(err, &failure) && strings.Contains(failure.detail, breakerOpenText)
}

// deployModel deploys modelID unless ML Commons already reports it deployed.
// It retries a deploy that the memory circuit breaker rejects every
// deployBreakerInterval until deployBreakerWindow ends. It returns every
// other failure at once. The breaker can reject the first deploy task for a
// short time after registration loads the model bundle into the JVM heap.
func (a *Adapter) deployModel(ctx context.Context, modelID string) error {
	model, err := a.getModel(ctx, modelID)
	if err != nil {
		return err
	}
	if model.State == deployedModelState {
		return nil
	}
	window, cancel := context.WithTimeout(ctx, deployBreakerWindow)
	defer cancel()
	for {
		err := a.deployOnce(ctx, modelID)
		if err == nil {
			return a.waitDeployedRecord(ctx, modelID)
		}
		if !breakerOpen(err) {
			return err
		}
		telemetry.L(ctx).InfoContext(ctx, "search.model.deploy_breaker_open", slog.String("model_id", modelID), slog.String("reason", err.Error()))
		wait := time.NewTimer(deployBreakerInterval)
		select {
		case <-window.Done():
			wait.Stop()
			wrapped := fmt.Errorf("deploy OpenSearch model %s while the memory circuit breaker stayed open for %s: %w", modelID, deployBreakerWindow, err)
			telemetry.L(ctx).ErrorContext(ctx, "search.model.deploy_breaker_timeout", slog.String("err", wrapped.Error()), slog.String("model_id", modelID))
			return loggedModelError{err: wrapped}
		case <-wait.C:
		}
	}
}

// waitDeployedRecord reads the record of modelID until its state is DEPLOYED
// or modelTaskWait ends. ML Commons marks a deploy task COMPLETED before
// it writes DEPLOYED to the model record, and its sync-up job can write
// DEPLOY_FAILED to the record while a slow deploy runs. A read directly after
// the task can return that earlier state. After this wait, the caller verifies
// the record and rejects any state other than DEPLOYED.
func (a *Adapter) waitDeployedRecord(ctx context.Context, modelID string) error {
	window := time.NewTimer(modelTaskWait)
	defer window.Stop()
	for reads := 1; ; reads++ {
		model, err := a.getModel(ctx, modelID)
		if err != nil {
			return err
		}
		if model.State == deployedModelState {
			telemetry.L(ctx).DebugContext(ctx, "search.model.deployed_record_read", slog.String("model_id", modelID), slog.Int("reads", reads))
			return nil
		}
		wait := time.NewTimer(deployedRecordInterval)
		select {
		case <-ctx.Done():
			wait.Stop()
			wrapped := fmt.Errorf("wait for the record of OpenSearch model %s: %w", modelID, ctx.Err())
			telemetry.L(ctx).ErrorContext(ctx, "search.model.deployed_record_canceled", slog.String("err", wrapped.Error()), slog.String("model_id", modelID))
			return loggedModelError{err: wrapped}
		case <-window.C:
			wait.Stop()
			telemetry.L(ctx).InfoContext(ctx, "search.model.deployed_record_wait_ended", slog.String("model_id", modelID), slog.String("state", model.State))
			return nil
		case <-wait.C:
		}
	}
}
