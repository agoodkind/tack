package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v5"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// deployedRecordInterval spaces the reads of the model record.
	deployedRecordInterval = time.Second
	deployingModelState    = "DEPLOYING"
	deployFailedModelState = "DEPLOY_FAILED"
)

// waitDeployedRecord reads the record of modelID while its state is DEPLOYING
// or DEPLOY_FAILED, until modelTaskWait ends. ML Commons marks a deploy task
// COMPLETED before it writes the final state to the model record, and its
// sync-up job can write DEPLOY_FAILED to the record while a slow deploy runs. A
// read directly after the task can return either earlier state. Every other
// state is final for this task and ends the wait. A poll that status 429 rejects
// or that exceeds its request timeout does not end the wait, as in
// waitModelTask. It returns an error when modelTaskWait ends. After a final
// state, the caller verifies the record and rejects any state other than
// DEPLOYED.
func (a *Adapter) waitDeployedRecord(ctx context.Context, modelID string) error {
	window := time.NewTimer(modelTaskWait)
	defer window.Stop()
	state := ""
	for reads := 1; ; reads++ {
		current, answered, err := a.pollRecordState(ctx, modelID)
		if err != nil {
			return err
		}
		if answered {
			state = current
		}
		if answered && state != deployingModelState && state != deployFailedModelState {
			telemetry.L(ctx).DebugContext(ctx, "search.model.final_record_read", slog.String("model_id", modelID), slog.String("state", state), slog.Int("reads", reads))
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
			wrapped := fmt.Errorf("OpenSearch model %s record stayed in a waiting state for %s; last state %q", modelID, modelTaskWait, state)
			telemetry.L(ctx).ErrorContext(ctx, "search.model.deployed_record_timeout", slog.String("err", wrapped.Error()), slog.String("model_id", modelID))
			return loggedModelError{err: wrapped}
		case <-wait.C:
		}
	}
}

// pollRecordState reads the model state once. It returns false without an error
// when status 429 rejects the read or the read exceeds its request timeout.
func (a *Adapter) pollRecordState(ctx context.Context, modelID string) (string, bool, error) {
	var model registeredModel
	response, err := opensearch.Execute(ctx, a.client, http.MethodGet, modelGetRequest{modelID: modelID}, &model)
	switch {
	case err == nil && response != nil && response.StatusCode == http.StatusTooManyRequests:
		telemetry.L(ctx).InfoContext(ctx, "search.model.record_poll_rejected", slog.String("model_id", modelID),
			slog.String("reason", opensearch.ParseError(response).Error()))
		return "", false, nil
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
		telemetry.L(ctx).InfoContext(ctx, "search.model.record_poll_timed_out", slog.String("model_id", modelID), slog.String("reason", err.Error()))
		return "", false, nil
	}
	if err := checkMLResponse(ctx, response, err); err != nil {
		wrapped := fmt.Errorf("read OpenSearch model %s: %w", modelID, engineCause(ctx, response, err))
		if !isLoggedModelError(err) {
			telemetry.L(ctx).ErrorContext(ctx, "search.model.read_failed", slog.String("err", wrapped.Error()), slog.String("model_id", modelID))
		}
		return "", false, loggedModelError{err: wrapped}
	}
	return model.State, true, nil
}
