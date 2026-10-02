package search

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// deployTaskType is the ML Commons task type of a model deploy.
const deployTaskType = "DEPLOY_MODEL"

var _ searchdomain.ModelRepairEngine = (*Adapter)(nil)

// activeDeployTasksRequest counts the deploy tasks of one model in CREATED
// or RUNNING.
type activeDeployTasksRequest struct{ body []byte }

func (request activeDeployTasksRequest) GetRequest(method string) (*http.Request, error) {
	return jsonBodyRequest(method, "/_plugins/_ml/tasks/_search", request.body, "build active deploy task search request")
}

type taskCountResult struct {
	Hits struct {
		Total struct {
			Value int `json:"value"`
		} `json:"total"`
	} `json:"hits"`
}

// ModelDeployment reads the model that index maps, the model record, and the
// number of its deploy tasks in CREATED or RUNNING.
func (a *Adapter) ModelDeployment(ctx context.Context, index string) (searchdomain.ModelDeployment, error) {
	var none searchdomain.ModelDeployment
	info, err := a.IndexInfo(ctx, index)
	if err != nil {
		return none, queryStepError{operation: "read model of index " + index, err: err}
	}
	if info.ModelID == "" {
		wrapped := fmt.Errorf("index %s maps no model ID", index)
		telemetry.L(ctx).ErrorContext(ctx, "search.model.mapping_model_absent", slog.String("err", wrapped.Error()), slog.String("index", index))
		return none, loggedModelError{err: wrapped}
	}
	model, err := a.getModel(ctx, info.ModelID)
	if err != nil {
		return none, err
	}
	active, err := a.activeDeployTasks(ctx, info.ModelID)
	if err != nil {
		return none, err
	}
	return searchdomain.ModelDeployment{
		ModelID: info.ModelID, State: model.State, LastUpdated: time.UnixMilli(model.LastUpdated).UTC(), ActiveTasks: active,
	}, nil
}

// StartModelDeploy starts one deploy of modelID with no node IDs and returns
// its task ID.
func (a *Adapter) StartModelDeploy(ctx context.Context, modelID string) (string, error) {
	return a.startDeploy(ctx, modelID)
}

// WaitModelDeploy waits for the deploy task taskID of modelID to end. A task
// that ends in any state other than COMPLETED returns an error.
func (a *Adapter) WaitModelDeploy(ctx context.Context, modelID, taskID string) error {
	return a.waitDeploy(ctx, modelID, taskID)
}

func (a *Adapter) activeDeployTasks(ctx context.Context, modelID string) (int, error) {
	body, err := json.Marshal(activeDeployTasksQuery(modelID))
	if err != nil {
		wrapped := fmt.Errorf("encode active deploy task search of model %s: %w", modelID, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.model.task_search_encode_failed", slog.String("err", wrapped.Error()), slog.String("model_id", modelID))
		return 0, loggedModelError{err: wrapped}
	}
	var result taskCountResult
	response, err := opensearch.Do(ctx, a.client, http.MethodPost, activeDeployTasksRequest{body: body}, &result)
	if err := checkMLResponse(ctx, response, err); err != nil {
		wrapped := fmt.Errorf("search active deploy tasks of model %s: %w", modelID, err)
		if !isLoggedModelError(err) {
			telemetry.L(ctx).ErrorContext(ctx, "search.model.task_search_failed", slog.String("err", wrapped.Error()), slog.String("model_id", modelID))
		}
		return 0, loggedModelError{err: wrapped}
	}
	return result.Hits.Total.Value, nil
}

type taskFilterClause struct {
	Term  map[string]string   `json:"term,omitempty"`
	Terms map[string][]string `json:"terms,omitempty"`
}

type taskBoolQuery struct {
	Filter []taskFilterClause `json:"filter"`
}

type taskSearchQuery struct {
	Bool taskBoolQuery `json:"bool"`
}

type taskCountBody struct {
	Size           int             `json:"size"`
	TrackTotalHits bool            `json:"track_total_hits"`
	Query          taskSearchQuery `json:"query"`
}

// activeDeployTasksQuery counts the DEPLOY_MODEL tasks of modelID in CREATED
// or RUNNING. ML Commons maps model_id, task_type, and state as keyword
// fields.
func activeDeployTasksQuery(modelID string) taskCountBody {
	filters := []taskFilterClause{
		{Term: map[string]string{"model_id": modelID}, Terms: nil},
		{Term: map[string]string{"task_type": deployTaskType}, Terms: nil},
		{Term: nil, Terms: map[string][]string{"state": {string(modelTaskCreated), string(modelTaskRunning)}}},
	}
	return taskCountBody{Size: 0, TrackTotalHits: true, Query: taskSearchQuery{Bool: taskBoolQuery{Filter: filters}}}
}
