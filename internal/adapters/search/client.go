package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/telemetry"
)

// Adapter owns the single official OpenSearch client used by Tack.
type Adapter struct {
	client *opensearch.Client
	api    *opensearchapi.Client
}

// New constructs the official client after validating native settings.
func New(ctx context.Context, cfg Config) (*Adapter, error) {
	if err := cfg.Validate(); err != nil {
		wrapped := fmt.Errorf("create OpenSearch client: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.client.create_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	client, err := opensearch.NewClient(opensearch.Config{
		Addresses:      []string{cfg.Endpoint},
		Username:       cfg.Username,
		Password:       cfg.Password,
		CACert:         []byte(cfg.CA),
		RequestTimeout: cfg.RequestTimeout,
		MaxRetries:     cfg.MaxRetries,
		RetryOnStatus:  []int{502, 503, 504},
	})
	if err != nil {
		wrapped := fmt.Errorf("create OpenSearch client: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.client.create_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	return &Adapter{client: client, api: opensearchapi.NewFromClient(client)}, nil
}

// Close stops the official client's transport resources.
func (a *Adapter) Close(ctx context.Context) error {
	if err := a.client.Close(); err != nil {
		wrapped := fmt.Errorf("close OpenSearch client: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.client.close_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}

// CreateIndex creates one strict native semantic index.
func (a *Adapter) CreateIndex(ctx context.Context, index string, spec IndexSpec) error {
	body, err := json.Marshal(indexBody{Settings: indexSettings{NumberOfShards: spec.Primaries, NumberOfRoutingShards: spec.RoutingShards, NumberOfReplicas: spec.Replicas}, Mappings: spec.Mapping()})
	if err != nil {
		wrapped := fmt.Errorf("encode OpenSearch mapping: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.index.encode_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	response, err := a.api.Indices.Create(ctx, opensearchapi.IndicesCreateReq{Index: index, Body: bytes.NewReader(body)})
	if err != nil {
		wrapped := fmt.Errorf("create OpenSearch index %q: %w", index, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.index.create_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	if response.Inspect().Response != nil && response.Inspect().Response.IsError() {
		wrapped := fmt.Errorf("create OpenSearch index %q: %s", index, response.Inspect().Response.String())
		telemetry.L(ctx).ErrorContext(ctx, "search.index.create_rejected", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	return nil
}

// SetReplicas changes the replica count without changing mappings.
func (a *Adapter) SetReplicas(ctx context.Context, index string, replicas int) error {
	if replicas < 0 {
		wrapped := fmt.Errorf("OpenSearch replicas must be nonnegative")
		telemetry.L(ctx).ErrorContext(ctx, "search.replicas.invalid", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	body, err := json.Marshal(indexSettingsBody{Index: indexSettings{NumberOfShards: 0, NumberOfRoutingShards: 0, NumberOfReplicas: replicas}})
	if err != nil {
		wrapped := fmt.Errorf("encode OpenSearch settings: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.replicas.encode_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	response, err := a.api.Indices.Settings.Put(ctx, opensearchapi.SettingsPutReq{Indices: []string{index}, Body: bytes.NewReader(body)})
	if err != nil {
		wrapped := fmt.Errorf("set OpenSearch replicas for %q: %w", index, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.replicas.update_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	if response.Inspect().Response != nil && response.Inspect().Response.IsError() {
		wrapped := fmt.Errorf("set OpenSearch replicas for %q: %s", index, response.Inspect().Response.String())
		telemetry.L(ctx).ErrorContext(ctx, "search.replicas.update_rejected", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	return nil
}

// IndexInfo reads mapping metadata used by the verification command.
func (a *Adapter) IndexInfo(ctx context.Context, index string) (IndexInfo, error) {
	response, err := a.api.Indices.Mapping.Get(ctx, &opensearchapi.MappingGetReq{Indices: []string{index}})
	if err != nil {
		wrapped := fmt.Errorf("read OpenSearch mapping %q: %w", index, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.mapping.read_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return IndexInfo{}, wrapped
	}
	entry, ok := response.GetIndices()[index]
	if !ok {
		wrapped := fmt.Errorf("OpenSearch mapping %q is absent", index)
		telemetry.L(ctx).ErrorContext(ctx, "search.mapping.absent", slog.String("err", wrapped.Error()), slog.String("index", index))
		return IndexInfo{}, wrapped
	}
	var mapping struct {
		Meta mappingMeta `json:"_meta"`
	}
	if err := json.Unmarshal(entry.Mappings, &mapping); err != nil {
		wrapped := fmt.Errorf("decode OpenSearch mapping %q: %w", index, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.mapping.decode_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return IndexInfo{}, wrapped
	}
	return IndexInfo{MappingVersion: mapping.Meta.MappingVersion, ModelID: mapping.Meta.ModelID}, nil
}

// Ping verifies the configured endpoint through the official client.
func (a *Adapter) Ping(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	if err != nil {
		wrapped := fmt.Errorf("create OpenSearch ping request: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.ping.request_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	response, err := a.client.Stream(request)
	if err != nil {
		wrapped := fmt.Errorf("ping OpenSearch: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.ping.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if response.StatusCode >= http.StatusBadRequest {
		_ = response.Body.Close()
		wrapped := fmt.Errorf("ping OpenSearch: status %s", response.Status)
		telemetry.L(ctx).ErrorContext(ctx, "search.ping.rejected", slog.String("err", wrapped.Error()), slog.Int("status_code", response.StatusCode))
		return wrapped
	}
	_ = response.Body.Close()
	return nil
}
