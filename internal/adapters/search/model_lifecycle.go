package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"goodkind.io/tack/internal/telemetry"
)

type loggedModelError struct{ err error }

func (e loggedModelError) Error() string { return e.err.Error() }
func (e loggedModelError) Unwrap() error { return e.err }

func isLoggedModelError(err error) bool {
	var logged loggedModelError
	return errors.As(err, &logged)
}

func checkMLResponse(ctx context.Context, response *opensearch.Response, err error) error {
	if err != nil {
		return err
	}
	if response == nil {
		return fmt.Errorf("OpenSearch returned no ML response")
	}
	if response.IsError() {
		wrapped := fmt.Errorf("OpenSearch ML request failed: %w", opensearch.ParseError(response))
		telemetry.L(ctx).ErrorContext(ctx, "search.model.request_rejected", slog.String("err", wrapped.Error()))
		return loggedModelError{err: wrapped}
	}
	return nil
}

func (a *Adapter) findPinnedModel(ctx context.Context) (string, error) {
	body, err := MarshalModelSearchBody()
	if err != nil {
		wrapped := fmt.Errorf("encode pinned model search: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.model.search_encode_failed", slog.String("err", wrapped.Error()))
		return "", loggedModelError{err: wrapped}
	}
	var found modelSearchResult
	response, err := opensearch.Do(ctx, a.client, http.MethodPost, modelSearchRequest{body: body}, &found)
	if err := checkMLResponse(ctx, response, err); err != nil {
		wrapped := fmt.Errorf("search registered OpenSearch models: %w", err)
		if !isLoggedModelError(err) {
			telemetry.L(ctx).ErrorContext(ctx, "search.model.search_failed", slog.String("err", wrapped.Error()))
		}
		return "", loggedModelError{err: wrapped}
	}
	modelID := ""
	for _, hit := range found.Hits.Hits {
		pinned := hit.Source.Name == PinnedModel.Name && hit.Source.ContentHash == PinnedModel.BundleDigest &&
			hit.Source.ContentSize == PinnedModel.BundleBytes
		if !pinned {
			continue
		}
		if modelID != "" {
			return "", fmt.Errorf("multiple registered OpenSearch models match %s with bundle SHA-256 %s", PinnedModel.Name, PinnedModel.BundleDigest)
		}
		modelID = hit.ID
	}
	return modelID, nil
}

func (a *Adapter) registerPinnedModel(ctx context.Context) (string, error) {
	body, err := json.Marshal(struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		ModelFormat string `json:"model_format"`
		Function    string `json:"function_name"`
	}{Name: PinnedModel.Name, Version: PinnedModel.Version, ModelFormat: pinnedModelFormat, Function: pinnedModelAlgorithm})
	if err != nil {
		wrapped := fmt.Errorf("encode pinned model registration: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.model.register_encode_failed", slog.String("err", wrapped.Error()))
		return "", loggedModelError{err: wrapped}
	}
	var started modelTaskStart
	response, err := opensearch.Do(ctx, a.client, http.MethodPost, modelRegisterRequest{body: body}, &started)
	if err := checkMLResponse(ctx, response, err); err != nil {
		wrapped := fmt.Errorf("register pinned OpenSearch model: %w", err)
		if !isLoggedModelError(err) {
			telemetry.L(ctx).ErrorContext(ctx, "search.model.register_failed", slog.String("err", wrapped.Error()))
		}
		return "", loggedModelError{err: wrapped}
	}
	if started.TaskID == "" {
		return "", fmt.Errorf("register pinned OpenSearch model: task ID is empty")
	}
	return a.waitModelTask(ctx, started.TaskID)
}

// deployOnce starts one deploy task for modelID and waits for it.
func (a *Adapter) deployOnce(ctx context.Context, modelID string) error {
	var started modelTaskStart
	response, err := opensearch.Do(ctx, a.client, http.MethodPost, modelDeployRequest{modelID: modelID}, &started)
	if err := checkMLResponse(ctx, response, err); err != nil {
		wrapped := fmt.Errorf("deploy OpenSearch model %s: %w", modelID, err)
		if !isLoggedModelError(err) {
			telemetry.L(ctx).ErrorContext(ctx, "search.model.deploy_failed", slog.String("err", wrapped.Error()), slog.String("model_id", modelID))
		}
		return loggedModelError{err: wrapped}
	}
	if started.TaskID == "" {
		return fmt.Errorf("deploy OpenSearch model %s: task ID is empty", modelID)
	}
	deployedID, err := a.waitModelTask(ctx, started.TaskID)
	if err != nil {
		return err
	}
	if deployedID != modelID {
		return fmt.Errorf("deploy OpenSearch model %s: task returned model %s", modelID, deployedID)
	}
	return nil
}

// waitModelTask polls taskID once a second until the task ends or five
// minutes pass. JVM heap use can exceed the parent circuit breaker limit
// while a model deploy runs, and the engine then rejects every request with
// status 429. A poll that receives status 429 does not end the wait, because
// the task continues to run on the engine.
func (a *Adapter) waitModelTask(ctx context.Context, taskID string) (string, error) {
	deadline, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for {
		var task modelTask
		response, err := opensearch.Do(deadline, a.client, http.MethodGet, modelTaskRequest{taskID: taskID}, &task)
		if err == nil && response != nil && response.StatusCode == http.StatusTooManyRequests {
			telemetry.L(ctx).InfoContext(ctx, "search.model.task_poll_rejected", slog.String("task_id", taskID),
				slog.String("reason", opensearch.ParseError(response).Error()))
			task.State = string(modelTaskRunning)
		} else if err := checkMLResponse(ctx, response, err); err != nil {
			wrapped := fmt.Errorf("read OpenSearch ML task %s: %w", taskID, err)
			if !isLoggedModelError(err) {
				telemetry.L(ctx).ErrorContext(ctx, "search.model.task_failed", slog.String("err", wrapped.Error()), slog.String("task_id", taskID))
			}
			return "", loggedModelError{err: wrapped}
		}
		switch modelTaskState(task.State) {
		case modelTaskCompleted:
			if task.ModelID == "" {
				return "", fmt.Errorf("OpenSearch ML task %s completed without a model ID", taskID)
			}
			return task.ModelID, nil
		case modelTaskCreated, modelTaskRunning:
			wait := time.NewTimer(time.Second)
			select {
			case <-deadline.Done():
				wait.Stop()
				wrapped := fmt.Errorf("wait for OpenSearch ML task %s: %w", taskID, deadline.Err())
				telemetry.L(ctx).ErrorContext(ctx, "search.model.task_timeout", slog.String("err", wrapped.Error()), slog.String("task_id", taskID))
				return "", loggedModelError{err: wrapped}
			case <-wait.C:
			}
		default:
			return "", modelTaskFailedError{taskID: taskID, state: task.State, detail: task.Error}
		}
	}
}
