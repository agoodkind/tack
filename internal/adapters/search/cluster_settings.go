package search

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/telemetry"
)

const (
	pointInTimeLimitSetting = "search.max_open_pit_context"
	mlHeapThresholdSetting  = "plugins.ml_commons.jvm_heap_memory_threshold"
)

// ClusterSettingValues reports each source of a cluster setting.
// An empty value means that the source does not set the setting.
type ClusterSettingValues struct {
	Persistent string
	Transient  string
	Defaults   string
}

// Effective selects transient, persistent, then defaults. OpenSearch omits
// the default when a cluster override exists.
func (values ClusterSettingValues) Effective() string {
	if values.Transient != "" {
		return values.Transient
	}
	if values.Persistent != "" {
		return values.Persistent
	}
	return values.Defaults
}

// ClusterSettings reports the point-in-time limit and ML heap guard.
type ClusterSettings struct {
	PointInTimeLimit         ClusterSettingValues
	MLJVMHeapMemoryThreshold ClusterSettingValues
}

type clusterSettingsSection struct {
	Search struct {
		MaxOpenPITContext string `json:"max_open_pit_context"`
	} `json:"search"`
	Plugins struct {
		MLCommons struct {
			JVMHeapMemoryThreshold string `json:"jvm_heap_memory_threshold"`
		} `json:"ml_commons"`
	} `json:"plugins"`
}

type clusterSettingsSections struct {
	Persistent clusterSettingsSection `json:"persistent"`
	Transient  clusterSettingsSection `json:"transient"`
	Defaults   clusterSettingsSection `json:"defaults"`
}

func (sections clusterSettingsSections) values(setting string, selectValue func(clusterSettingsSection) string) (ClusterSettingValues, error) {
	values := ClusterSettingValues{
		Persistent: selectValue(sections.Persistent),
		Transient:  selectValue(sections.Transient),
		Defaults:   selectValue(sections.Defaults),
	}
	if values.Effective() == "" {
		return ClusterSettingValues{}, fmt.Errorf("OpenSearch cluster settings do not report a value for %s", setting)
	}
	return values, nil
}

// Settings reads only the point-in-time limit and ML heap threshold with
// defaults. Explicit nested filter paths match OpenSearch's nested response.
func (a *Adapter) Settings(ctx context.Context) (ClusterSettings, error) {
	includeDefaults := true
	response, err := a.api.Cluster.GetSettings(ctx, &opensearchapi.ClusterGetSettingsReq{
		Header: nil,
		Params: &opensearchapi.ClusterGetSettingsParams{
			TimeoutParams: opensearchapi.TimeoutParams{},
			DebugParams: opensearchapi.DebugParams{FilterPath: []string{
				"persistent." + pointInTimeLimitSetting,
				"transient." + pointInTimeLimitSetting,
				"defaults." + pointInTimeLimitSetting,
				"persistent." + mlHeapThresholdSetting,
				"transient." + mlHeapThresholdSetting,
				"defaults." + mlHeapThresholdSetting,
			}},
			FlatSettings: nil, IncludeDefaults: &includeDefaults,
		},
	})
	if err != nil {
		return ClusterSettings{}, clusterSettingsFailure(ctx, "read", err)
	}
	body := response.RawBody()
	if body == nil {
		return ClusterSettings{}, clusterSettingsFailure(ctx, "empty", fmt.Errorf("OpenSearch returned an empty cluster settings response"))
	}
	var sections clusterSettingsSections
	if err := json.NewDecoder(body).Decode(&sections); err != nil {
		return ClusterSettings{}, clusterSettingsFailure(ctx, "decode", err)
	}
	limit, err := sections.values(pointInTimeLimitSetting, func(section clusterSettingsSection) string {
		return section.Search.MaxOpenPITContext
	})
	if err != nil {
		return ClusterSettings{}, clusterSettingsFailure(ctx, "default_absent", err)
	}
	heap, err := sections.values(mlHeapThresholdSetting, func(section clusterSettingsSection) string {
		return section.Plugins.MLCommons.JVMHeapMemoryThreshold
	})
	if err != nil {
		return ClusterSettings{}, clusterSettingsFailure(ctx, "default_absent", err)
	}
	return ClusterSettings{PointInTimeLimit: limit, MLJVMHeapMemoryThreshold: heap}, nil
}

func clusterSettingsFailure(ctx context.Context, operation string, err error) error {
	wrapped := fmt.Errorf("%s OpenSearch cluster settings: %w", operation, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.cluster_settings."+operation, slog.String("err", wrapped.Error()))
	return wrapped
}
