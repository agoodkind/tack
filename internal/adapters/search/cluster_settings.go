package search

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/telemetry"
)

// pointInTimeLimitSetting limits the open point-in-time contexts on each
// OpenSearch node.
const pointInTimeLimitSetting = "search.max_open_pit_context"

// PointInTimeLimit is the value of search.max_open_pit_context in each
// section of the cluster settings response. An empty value means that the
// section does not set the setting.
type PointInTimeLimit struct {
	Persistent string
	Transient  string
	Defaults   string
}

// Effective returns the value that OpenSearch applies. A transient cluster
// setting overrides a persistent one, and a persistent cluster setting
// overrides the node setting. When neither cluster section sets the value,
// OpenSearch 3.8.0 reports the node setting from the container environment
// in the defaults section. When a cluster section sets the value, the
// defaults section omits it.
func (limit PointInTimeLimit) Effective() string {
	if limit.Transient != "" {
		return limit.Transient
	}
	if limit.Persistent != "" {
		return limit.Persistent
	}
	return limit.Defaults
}

// pointInTimeLimitSection is one section of the nested cluster settings
// response, filtered to search.max_open_pit_context.
type pointInTimeLimitSection struct {
	Search struct {
		MaxOpenPITContext string `json:"max_open_pit_context"`
	} `json:"search"`
}

// PointInTimeLimit reads search.max_open_pit_context from the persistent,
// transient, and defaults sections of the cluster settings. The filter path
// limits the response to that one setting. The request reads the nested
// form because the filter path of OpenSearch 3.8.0 does not match a flat
// setting name that contains dots.
func (a *Adapter) PointInTimeLimit(ctx context.Context) (PointInTimeLimit, error) {
	includeDefaults := true
	response, err := a.api.Cluster.GetSettings(ctx, &opensearchapi.ClusterGetSettingsReq{
		Header: nil,
		Params: &opensearchapi.ClusterGetSettingsParams{
			TimeoutParams: opensearchapi.TimeoutParams{},
			DebugParams: opensearchapi.DebugParams{FilterPath: []string{
				"persistent." + pointInTimeLimitSetting,
				"transient." + pointInTimeLimitSetting,
				"defaults." + pointInTimeLimitSetting,
			}},
			FlatSettings: nil, IncludeDefaults: &includeDefaults,
		},
	})
	if err != nil {
		wrapped := fmt.Errorf("read OpenSearch cluster setting %s: %w", pointInTimeLimitSetting, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.cluster_settings.read_failed", slog.String("err", wrapped.Error()))
		return PointInTimeLimit{}, wrapped
	}
	var sections struct {
		Persistent pointInTimeLimitSection `json:"persistent"`
		Transient  pointInTimeLimitSection `json:"transient"`
		Defaults   pointInTimeLimitSection `json:"defaults"`
	}
	body := response.RawBody()
	if body == nil {
		wrapped := fmt.Errorf("OpenSearch returned an empty cluster settings response for %s", pointInTimeLimitSetting)
		telemetry.L(ctx).ErrorContext(ctx, "search.cluster_settings.empty", slog.String("err", wrapped.Error()))
		return PointInTimeLimit{}, wrapped
	}
	if err := json.NewDecoder(body).Decode(&sections); err != nil {
		wrapped := fmt.Errorf("decode OpenSearch cluster setting %s: %w", pointInTimeLimitSetting, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.cluster_settings.invalid", slog.String("err", wrapped.Error()))
		return PointInTimeLimit{}, wrapped
	}
	limit := PointInTimeLimit{
		Persistent: sections.Persistent.Search.MaxOpenPITContext,
		Transient:  sections.Transient.Search.MaxOpenPITContext,
		Defaults:   sections.Defaults.Search.MaxOpenPITContext,
	}
	if limit.Effective() == "" {
		wrapped := fmt.Errorf("OpenSearch cluster settings do not report a value for %s", pointInTimeLimitSetting)
		telemetry.L(ctx).ErrorContext(ctx, "search.cluster_settings.default_absent", slog.String("err", wrapped.Error()))
		return PointInTimeLimit{}, wrapped
	}
	return limit, nil
}
