package search

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/telemetry"
)

// EnsureIndex creates a missing physical index. For an existing index, it
// updates only the replica count and then verifies the mapping and shard
// topology against spec.
func (a *Adapter) EnsureIndex(ctx context.Context, index string, spec IndexSpec) error {
	if err := spec.Validate(ctx); err != nil {
		wrapped := fmt.Errorf("ensure OpenSearch index %s: %w", index, err)
		if !isLoggedModelError(err) {
			telemetry.L(ctx).ErrorContext(ctx, "search.index.invalid", slog.String("err", wrapped.Error()), slog.String("index", index))
		}
		return wrapped
	}
	response, err := opensearch.Do[json.RawMessage](ctx, a.client, http.MethodHead, opensearchapi.IndicesExistsReq{Indices: []string{index}}, nil)
	if err != nil {
		wrapped := fmt.Errorf("check OpenSearch index %s: %w", index, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.index.exists_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	if response.StatusCode == http.StatusNotFound {
		return a.CreateIndex(ctx, index, spec)
	}
	if response.IsError() {
		wrapped := fmt.Errorf("check OpenSearch index %s: %w", index, opensearch.ParseError(response))
		telemetry.L(ctx).ErrorContext(ctx, "search.index.exists_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	settings, err := a.IndexSettings(ctx, index)
	if err != nil {
		return err
	}
	if settings.Replicas != spec.Replicas {
		if err := a.SetReplicas(ctx, index, spec.Replicas); err != nil {
			return err
		}
		telemetry.L(ctx).InfoContext(ctx, "search.replicas.updated", slog.String("index", index),
			slog.Int("previous_replicas", settings.Replicas), slog.Int("replicas", spec.Replicas))
	}
	return a.VerifyIndex(ctx, index, spec)
}

// WaitGreen requires every configured index shard to become assigned.
func (a *Adapter) WaitGreen(ctx context.Context, index string) error {
	response, err := a.api.Cluster.Health(ctx, &opensearchapi.ClusterHealthReq{
		Indices: []string{index},
		Params:  opensearchapi.ClusterHealthParams{WaitForStatus: "green", Timeout: time.Minute},
	})
	if err != nil {
		wrapped := fmt.Errorf("wait for OpenSearch index %s green health: %w", index, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.index.health_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	if response.TimedOut || response.Status != "green" {
		wrapped := fmt.Errorf("OpenSearch index %s health is %s, timed out %t", index, response.Status, response.TimedOut)
		telemetry.L(ctx).ErrorContext(ctx, "search.index.health_invalid", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	return nil
}
