package search

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	opensearch "github.com/opensearch-project/opensearch-go/v5"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/telemetry"
)

// PhysicalSettings reports the shard topology of one physical index.
type PhysicalSettings struct {
	Primaries     int
	RoutingShards int
	Replicas      int
}

// IndexSettings reads the shard topology of index.
//
// A split target does not have an index.number_of_routing_shards setting. Its
// cluster state metadata still records the routing shard count.
//
//  1. Read primary and replica counts from the typed settings API.
//  2. Read the routing shard count from cluster state metadata.
func (a *Adapter) IndexSettings(ctx context.Context, index string) (PhysicalSettings, error) {
	response, err := a.api.Indices.GetSettings(
		ctx,
		&opensearchapi.IndicesGetSettingsReq{Indices: []string{index}},
	)
	if err != nil {
		wrapped := fmt.Errorf("read OpenSearch index %s settings: %w", index, err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.index.settings_failed",
			slog.String("err", wrapped.Error()),
			slog.String("index", index),
		)

		return PhysicalSettings{}, wrapped
	}

	var indices map[string]struct {
		Settings json.RawMessage `json:"settings"`
	}
	if err := json.NewDecoder(response.RawBody()).Decode(&indices); err != nil {
		return PhysicalSettings{}, engineFailure(
			ctx,
			"search.index.settings_invalid",
			"decode settings "+index,
			index,
			err,
		)
	}

	entry, exists := indices[index]
	if !exists {
		wrapped := fmt.Errorf("OpenSearch index %s settings are absent", index)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.index.settings_absent",
			slog.String("err", wrapped.Error()),
			slog.String("index", index),
		)

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
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.index.settings_invalid",
			slog.String("err", wrapped.Error()),
			slog.String("index", index),
		)

		return PhysicalSettings{}, wrapped
	}

	primary, err := decoded.Index.Primaries.Int64()
	if err != nil {
		wrapped := fmt.Errorf("parse OpenSearch index %s primary shards: %w", index, err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.index.settings_invalid",
			slog.String("err", wrapped.Error()),
			slog.String("index", index),
		)

		return PhysicalSettings{}, wrapped
	}

	replicas, err := decoded.Index.Replicas.Int64()
	if err != nil {
		wrapped := fmt.Errorf("parse OpenSearch index %s replicas: %w", index, err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.index.settings_invalid",
			slog.String("err", wrapped.Error()),
			slog.String("index", index),
		)

		return PhysicalSettings{}, wrapped
	}

	routing, err := a.routingShards(ctx, index)
	if err != nil {
		return PhysicalSettings{}, err
	}

	return PhysicalSettings{
		Primaries:     int(primary),
		RoutingShards: routing,
		Replicas:      int(replicas),
	}, nil
}

// routingShards reads routing_num_shards from the cluster state metadata of
// index. OpenSearch records that value for every index. The filter path
// limits the response to that one value.
func (a *Adapter) routingShards(ctx context.Context, index string) (int, error) {
	response, err := a.api.Cluster.State(
		ctx,
		&opensearchapi.ClusterStateReq{
			Metric:  []string{"metadata"},
			Indices: []string{index},
			Params: &opensearchapi.ClusterStateParams{
				FilterPath: []string{"metadata.indices.*.routing_num_shards"},
			},
		},
	)
	if err != nil {
		wrapped := fmt.Errorf("read OpenSearch index %s metadata: %w", index, err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.index.metadata_failed",
			slog.String("err", wrapped.Error()),
			slog.String("index", index),
		)

		return 0, wrapped
	}

	var state struct {
		Metadata struct {
			Indices map[string]struct {
				RoutingNumShards int `json:"routing_num_shards"`
			} `json:"indices"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(response.Body, &state); err != nil {
		return 0, engineFailure(
			ctx,
			"search.index.metadata_invalid",
			"decode metadata "+index,
			index,
			err,
		)
	}

	entry, exists := state.Metadata.Indices[index]
	if !exists || entry.RoutingNumShards < 1 {
		wrapped := fmt.Errorf("OpenSearch index %s metadata has no routing shard count", index)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.index.metadata_invalid",
			slog.String("err", wrapped.Error()),
			slog.String("index", index),
		)

		return 0, wrapped
	}

	return entry.RoutingNumShards, nil
}

// AliasTarget requires the public alias to select exactly one physical index.
//
// AliasTarget returns the physical index name from the OpenSearch alias
// response.
//
//  1. Read the indices that alias selects.
//  2. Return the single physical index.
func (a *Adapter) AliasTarget(ctx context.Context, alias string) (string, error) {
	response, err := a.api.Indices.GetAlias(
		ctx,
		&opensearchapi.IndicesGetAliasReq{Name: []string{alias}},
	)
	if err != nil {
		wrapped := fmt.Errorf(
			"resolve OpenSearch alias %s: %w",
			alias,
			engineCause(ctx, response.Inspect().Response, err),
		)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.alias.resolve_failed",
			slog.String("err", wrapped.Error()),
			slog.String("alias", alias),
		)

		return "", wrapped
	}

	indices := response.Entries
	if len(indices) != 1 {
		wrapped := fmt.Errorf(
			"OpenSearch alias %s selects %d physical indices",
			alias,
			len(indices),
		)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.alias.invalid",
			slog.String("err", wrapped.Error()),
			slog.String("alias", alias),
		)

		return "", wrapped
	}

	for index := range indices {
		return index, nil
	}

	return "", fmt.Errorf("OpenSearch alias %s has no target", alias)
}

// SetAlias binds the public name to the provisioned physical index.
//
// SetAlias accepts an existing binding to index and rejects a binding to
// another index.
//
//  1. Check whether alias exists.
//  2. Verify an existing binding or create a missing binding.
//  3. Require OpenSearch to acknowledge the new binding.
func (a *Adapter) SetAlias(ctx context.Context, alias, index string) error {
	existsResponse, err := opensearch.Execute[json.RawMessage](
		ctx,
		a.client,
		http.MethodHead,
		&opensearchapi.IndicesExistsAliasReq{Name: []string{alias}},
		nil,
	)
	if err != nil {
		wrapped := fmt.Errorf("check OpenSearch alias %s: %w", alias, err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.alias.check_failed",
			slog.String("err", wrapped.Error()),
			slog.String("alias", alias),
		)

		return wrapped
	}

	if existsResponse.StatusCode != http.StatusNotFound {
		if existsResponse.IsError() {
			wrapped := fmt.Errorf(
				"check OpenSearch alias %s: %w",
				alias,
				opensearch.ParseError(existsResponse),
			)
			telemetry.L(ctx).ErrorContext(
				ctx,
				"search.alias.check_failed",
				slog.String("err", wrapped.Error()),
				slog.String("alias", alias),
			)

			return wrapped
		}

		current, err := a.AliasTarget(ctx, alias)
		if err != nil {
			return err
		}

		if current != index {
			wrapped := fmt.Errorf(
				"OpenSearch alias %s already selects physical index %s",
				alias,
				current,
			)
			telemetry.L(ctx).ErrorContext(
				ctx,
				"search.alias.conflict",
				slog.String("err", wrapped.Error()),
				slog.String("alias", alias),
			)

			return wrapped
		}

		return nil
	}

	return a.createIndexAlias(ctx, alias, index)
}

func (a *Adapter) createIndexAlias(ctx context.Context, alias string, index string) error {
	response, err := a.putIndexAlias(ctx, alias, index)
	if err != nil {
		return err
	}

	if response.Inspect().Response != nil && response.Inspect().Response.IsError() {
		wrapped := &aliasRejectedError{
			alias:    alias,
			index:    index,
			response: response.Inspect().Response.String(),
		}
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.alias.set_rejected",
			slog.String("err", wrapped.Error()),
			slog.String("alias", alias),
			slog.String("index", index),
		)

		return wrapped
	}

	if !response.Acknowledged {
		wrapped := &aliasUnacknowledgedError{alias: alias, index: index}
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.alias.set_unacknowledged",
			slog.String("err", wrapped.Error()),
			slog.String("alias", alias),
			slog.String("index", index),
		)

		return wrapped
	}

	return nil
}
