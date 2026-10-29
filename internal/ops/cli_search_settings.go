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

// searchSettingsOutput is the value of search.max_open_pit_context in each
// cluster settings section and the value that OpenSearch applies. An empty
// section value means that the section does not set the setting.
type searchSettingsOutput struct {
	clispec.ResultMarker
	Command    string `json:"command"`
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
		Short:    "Read the persistent, transient, default, and effective OpenSearch point-in-time limit",
		New:      func() searchSettingsInput { return searchSettingsInput{InputMarker: clispec.InputMarker{}} },
		Run: func(ctx context.Context, _ searchSettingsInput, sink clispec.ResultSink) error {
			return runSearchSettings(ctx, f, sink)
		},
	}
}

// runSearchSettings reads search.max_open_pit_context with the maintenance
// credentials and writes only that setting's values.
func runSearchSettings(ctx context.Context, f *cli.Factory, sink clispec.ResultSink) (runErr error) {
	adapter, err := newSearchMaintenanceAdapter(ctx, f.Cfg)
	if err != nil {
		return searchSettingsFailure(ctx, "create search adapter", err)
	}
	defer func() {
		if err := adapter.Close(ctx); err != nil && runErr == nil {
			runErr = searchSettingsFailure(ctx, "close search adapter", err)
		}
	}()
	limit, err := adapter.PointInTimeLimit(ctx)
	if err != nil {
		return searchSettingsFailure(ctx, "read the point-in-time limit", err)
	}
	output := searchSettingsOutput{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.search.settings",
		Persistent: limit.Persistent, Transient: limit.Transient, Defaults: limit.Defaults, Effective: limit.Effective(),
	}
	telemetry.L(ctx).InfoContext(ctx, "search.settings.read", slog.String("effective", output.Effective))
	if err := clispec.WriteJSONValue(ctx, sink, output); err != nil {
		return searchSettingsFailure(ctx, "write the point-in-time limit", err)
	}
	return nil
}

func searchSettingsFailure(ctx context.Context, operation string, err error) error {
	wrapped := fmt.Errorf("%s: %w", operation, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.settings.failed", slog.String("err", wrapped.Error()))
	return wrapped
}
