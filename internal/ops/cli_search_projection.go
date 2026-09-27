package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

type searchProjectionBackfillInput struct {
	clispec.InputMarker
	Manifest string
}

type searchProjectionBackfillOutput struct {
	clispec.ResultMarker
	Command string                        `json:"command"`
	DryRun  bool                          `json:"dry_run"`
	Result  node.ProjectionBackfillResult `json:"result"`
}

func searchProjectionBackfillOp(f *cli.Factory) clispec.Operation[searchProjectionBackfillInput] {
	return clispec.Operation[searchProjectionBackfillInput]{
		Name:     clispec.Name{Canonical: "search-projections", CLIOverride: ""},
		Lifetime: clispec.Lifetime{Ticket: "TACK-542", RemoveBy: time.Date(2026, time.December, 31, 0, 0, 0, 0, time.UTC)},
		Audit:    audit.Spec{Verb: string(audit.VerbOpsBackfillSearchProjections), Mutates: true},
		Group:    opsGroup,
		Short:    "Apply reviewed search projection declarations",
		Params: []clispec.Param[searchProjectionBackfillInput]{
			clispec.StringParam("manifest", "This parameter accepts the path of the reviewed projection manifest.", "", true, func(in *searchProjectionBackfillInput, value string) { in.Manifest = value }),
		},
		New: func() searchProjectionBackfillInput {
			return searchProjectionBackfillInput{InputMarker: clispec.InputMarker{}, Manifest: ""}
		},
		DryRun: func(ctx context.Context, in searchProjectionBackfillInput, sink clispec.ResultSink) error {
			return runSearchProjectionBackfillCommand(ctx, f, in, sink, true)
		},
		Run: func(ctx context.Context, in searchProjectionBackfillInput, sink clispec.ResultSink) error {
			return runSearchProjectionBackfillCommand(ctx, f, in, sink, false)
		},
	}
}

func runSearchProjectionBackfillCommand(ctx context.Context, f *cli.Factory, in searchProjectionBackfillInput, sink clispec.ResultSink, dryRun bool) error {
	manifest, err := os.Open(in.Manifest)
	if err != nil {
		wrapped := fmt.Errorf("open search projection manifest %q: %w", in.Manifest, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.projection.open_failed", slog.String("err", wrapped.Error()), slog.String("manifest", in.Manifest))
		return wrapped
	}
	defer func() { _ = manifest.Close() }()
	env, err := NewEnv(ctx, f.Cfg)
	if err != nil {
		wrapped := fmt.Errorf("open search projection backfill environment: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.projection.environment_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer env.Close()
	result, err := RunSearchProjectionBackfill(ctx, env.Stores.PropertyDefs, manifest, dryRun)
	if err != nil {
		return err
	}
	output := searchProjectionBackfillOutput{
		ResultMarker: clispec.ResultMarker{},
		Command:      "ops.backfill.once-search-projections",
		DryRun:       dryRun,
		Result:       result,
	}
	if err := clispec.WriteJSONValue(ctx, sink, output); err != nil {
		wrapped := fmt.Errorf("write search projection backfill result: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.projection.report_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}
