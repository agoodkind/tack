package search

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/telemetry"
)

// PhysicalSettings reports the shard topology of one physical index.
type PhysicalSettings struct {
	Primaries     int
	RoutingShards int
	Replicas      int
}

// IndexSettings reads the primary and replica counts from the typed settings
// API and the routing shard count from the index metadata in the cluster
// state. A split target has no index.number_of_routing_shards setting. Its
// metadata still records the routing shard count.
func (a *Adapter) IndexSettings(ctx context.Context, index string) (PhysicalSettings, error) {
	response, err := a.api.Indices.Settings.Get(ctx, &opensearchapi.SettingsGetReq{Indices: []string{index}})
	if err != nil {
		wrapped := fmt.Errorf("read OpenSearch index %s settings: %w", index, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.index.settings_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return PhysicalSettings{}, wrapped
	}
	entry, exists := response.GetIndices()[index]
	if !exists {
		wrapped := fmt.Errorf("OpenSearch index %s settings are absent", index)
		telemetry.L(ctx).ErrorContext(ctx, "search.index.settings_absent", slog.String("err", wrapped.Error()), slog.String("index", index))
		return PhysicalSettings{}, wrapped
	}
	var decoded struct {
		Index struct {
			Primaries json.Number `json:"number_of_shards"`
			Replicas  json.Number `json:"number_of_replicas"`
		} `json:"index"`
	}
	if err := json.Unmarshal(entry.Settings, &decoded); err != nil {
		wrapped := fmt.Errorf("decode OpenSearch index %s settings: %w", index, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.index.settings_invalid", slog.String("err", wrapped.Error()), slog.String("index", index))
		return PhysicalSettings{}, wrapped
	}
	primary, err := decoded.Index.Primaries.Int64()
	if err != nil {
		wrapped := fmt.Errorf("parse OpenSearch index %s primary shards: %w", index, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.index.settings_invalid", slog.String("err", wrapped.Error()), slog.String("index", index))
		return PhysicalSettings{}, wrapped
	}
	replicas, err := decoded.Index.Replicas.Int64()
	if err != nil {
		wrapped := fmt.Errorf("parse OpenSearch index %s replicas: %w", index, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.index.settings_invalid", slog.String("err", wrapped.Error()), slog.String("index", index))
		return PhysicalSettings{}, wrapped
	}
	routing, err := a.routingShards(ctx, index)
	if err != nil {
		return PhysicalSettings{}, err
	}
	return PhysicalSettings{Primaries: int(primary), RoutingShards: routing, Replicas: int(replicas)}, nil
}

// routingShards reads routing_num_shards from the cluster state metadata of
// index. OpenSearch records that value for every index. The filter path
// limits the response to that one value.
func (a *Adapter) routingShards(ctx context.Context, index string) (int, error) {
	response, err := a.api.Cluster.State(ctx, &opensearchapi.ClusterStateReq{
		Metrics: []string{"metadata"}, Indices: []string{index},
		Params: opensearchapi.ClusterStateParams{FilterPath: []string{"metadata.indices.*.routing_num_shards"}},
	})
	if err != nil {
		wrapped := fmt.Errorf("read OpenSearch index %s metadata: %w", index, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.index.metadata_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return 0, wrapped
	}
	entry, exists := response.Metadata.Indices[index]
	if !exists || entry.RoutingNumShards < 1 {
		wrapped := fmt.Errorf("OpenSearch index %s metadata has no routing shard count", index)
		telemetry.L(ctx).ErrorContext(ctx, "search.index.metadata_invalid", slog.String("err", wrapped.Error()), slog.String("index", index))
		return 0, wrapped
	}
	return entry.RoutingNumShards, nil
}

// AliasTarget requires the public alias to select exactly one physical index.
func (a *Adapter) AliasTarget(ctx context.Context, alias string) (string, error) {
	response, err := a.api.Indices.Alias.Get(ctx, opensearchapi.AliasGetReq{Alias: []string{alias}})
	if err != nil {
		wrapped := fmt.Errorf("resolve OpenSearch alias %s: %w", alias, engineCause(response.Inspect().Response, err))
		telemetry.L(ctx).ErrorContext(ctx, "search.alias.resolve_failed", slog.String("err", wrapped.Error()), slog.String("alias", alias))
		return "", wrapped
	}
	indices := response.GetIndices()
	if len(indices) != 1 {
		wrapped := fmt.Errorf("OpenSearch alias %s selects %d physical indices", alias, len(indices))
		telemetry.L(ctx).ErrorContext(ctx, "search.alias.invalid", slog.String("err", wrapped.Error()), slog.String("alias", alias))
		return "", wrapped
	}
	for index := range indices {
		return index, nil
	}
	return "", fmt.Errorf("OpenSearch alias %s has no target", alias)
}

// SetAlias binds the public name to the provisioned physical index.
func (a *Adapter) SetAlias(ctx context.Context, alias, index string) error {
	existsResponse, err := opensearch.Do[json.RawMessage](ctx, a.client, http.MethodHead,
		opensearchapi.AliasExistsReq{Alias: []string{alias}}, nil)
	if err != nil {
		wrapped := fmt.Errorf("check OpenSearch alias %s: %w", alias, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.alias.check_failed", slog.String("err", wrapped.Error()), slog.String("alias", alias))
		return wrapped
	}
	if existsResponse.StatusCode != http.StatusNotFound {
		if existsResponse.IsError() {
			wrapped := fmt.Errorf("check OpenSearch alias %s: %w", alias, opensearch.ParseError(existsResponse))
			telemetry.L(ctx).ErrorContext(ctx, "search.alias.check_failed", slog.String("err", wrapped.Error()), slog.String("alias", alias))
			return wrapped
		}
		current, err := a.AliasTarget(ctx, alias)
		if err != nil {
			return err
		}
		if current != index {
			wrapped := fmt.Errorf("OpenSearch alias %s already selects physical index %s", alias, current)
			telemetry.L(ctx).ErrorContext(ctx, "search.alias.conflict", slog.String("err", wrapped.Error()), slog.String("alias", alias))
			return wrapped
		}
		return nil
	}
	response, err := a.api.Indices.Alias.Put(ctx, opensearchapi.AliasPutReq{Alias: alias, Indices: []string{index}})
	if err != nil {
		wrapped := fmt.Errorf("set OpenSearch alias %s to %s: %w", alias, index, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.alias.set_failed", slog.String("err", wrapped.Error()), slog.String("alias", alias), slog.String("index", index))
		return wrapped
	}
	if response.Inspect().Response != nil && response.Inspect().Response.IsError() {
		wrapped := fmt.Errorf("set OpenSearch alias %s to %s: %s", alias, index, response.Inspect().Response.String())
		telemetry.L(ctx).ErrorContext(ctx, "search.alias.set_rejected", slog.String("err", wrapped.Error()), slog.String("alias", alias), slog.String("index", index))
		return wrapped
	}
	if !response.Acknowledged {
		wrapped := fmt.Errorf("set OpenSearch alias %s to %s: update was not acknowledged", alias, index)
		telemetry.L(ctx).ErrorContext(ctx, "search.alias.set_unacknowledged", slog.String("err", wrapped.Error()), slog.String("alias", alias), slog.String("index", index))
		return wrapped
	}
	return nil
}
