package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/telemetry"
)

type searchSettingsInput struct {
	clispec.InputMarker
}

// searchSettingsOutput reports the point-in-time limit and ML heap guard.
type searchSettingsOutput struct {
	clispec.ResultMarker
	Command                  string              `json:"command"`
	Persistent               string              `json:"persistent"`
	Transient                string              `json:"transient"`
	Defaults                 string              `json:"defaults"`
	Effective                string              `json:"effective"`
	MLJVMHeapMemoryThreshold searchSettingOutput `json:"ml_jvm_heap_memory_threshold"`
}

type searchSettingOutput struct {
	Persistent string `json:"persistent"`
	Transient  string `json:"transient"`
	Defaults   string `json:"defaults"`
	Effective  string `json:"effective"`
}

func searchSettingsOp(f *cli.Factory) clispec.Operation[searchSettingsInput] {
	return clispec.Operation[searchSettingsInput]{
		Name:     clispec.Name{Canonical: "settings", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsSearchSettings), Reads: true},
		Group:    searchGroup,
		Short:    "Read the OpenSearch point-in-time limit and ML heap threshold",
		New:      func() searchSettingsInput { return searchSettingsInput{InputMarker: clispec.InputMarker{}} },
		Run: func(ctx context.Context, _ searchSettingsInput, sink clispec.ResultSink) error {
			return runSearchSettings(ctx, f, sink)
		},
	}
}

// runSearchSettings reads the settings with the maintenance identity.
func runSearchSettings(ctx context.Context, f *cli.Factory, sink clispec.ResultSink) (runErr error) {
	adapter, err := newSearchMaintenanceAdapter(ctx, f.Cfg)
	if err != nil {
		return searchSettingsFailure("create search adapter", err)
	}
	defer func() {
		if err := adapter.Close(ctx); err != nil && runErr == nil {
			runErr = searchSettingsFailure("close search adapter", err)
		}
	}()
	settings, err := adapter.Settings(ctx)
	if err != nil {
		return searchSettingsFailure("read the search settings", err)
	}
	limit := settings.PointInTimeLimit
	heap := settings.MLJVMHeapMemoryThreshold
	output := searchSettingsOutput{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.search.settings",
		Persistent: limit.Persistent, Transient: limit.Transient, Defaults: limit.Defaults, Effective: limit.Effective(),
		MLJVMHeapMemoryThreshold: searchSettingOutput{
			Persistent: heap.Persistent, Transient: heap.Transient, Defaults: heap.Defaults, Effective: heap.Effective(),
		},
	}
	telemetry.L(ctx).InfoContext(ctx, "search.settings.read", slog.String("effective", output.Effective))
	if err := clispec.WriteJSONValue(ctx, sink, output); err != nil {
		return searchSettingsFailure("write the search settings", err)
	}
	return nil
}

// searchSettingsFailure adds operation to err and does not log. The search
// adapter, the adapter constructor, and the result sink log each failure
// before they return it.
func searchSettingsFailure(operation string, err error) error {
	wrapped := fmt.Errorf("%s: %w", operation, err)
	return wrapped
}
