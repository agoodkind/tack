package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

type metadataNameBackfillOutput struct {
	clispec.ResultMarker
	Command string                     `json:"command"`
	DryRun  bool                       `json:"dry_run"`
	Result  MetadataNameBackfillResult `json:"result"`
}

func metadataNameBackfillOp(f *cli.Factory) clispec.Operation[noInput] {
	return clispec.Operation[noInput]{
		Name:     clispec.Name{Canonical: "metadata-name-index", CLIOverride: ""},
		Lifetime: clispec.Lifetime{Ticket: "TACK-542", RemoveBy: time.Date(2026, time.November, 30, 0, 0, 0, 0, time.UTC)},
		Audit:    audit.Spec{Verb: string(audit.VerbOpsBackfillMetadataNameIndex), Mutates: true},
		Group:    opsGroup,
		Short:    "Write the missing type-key and property-name index entries",
		Long: "The command scans the node types and property definitions of every organization. " +
			"A dry run reports the records without an index entry. With --execute, the command writes those entries. " +
			"A second run reports zero missing entries.",
		New: func() noInput { return noInput{InputMarker: clispec.InputMarker{}} },
		DryRun: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return runMetadataNameBackfillCommand(ctx, f, sink, true)
		},
		Run: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return runMetadataNameBackfillCommand(ctx, f, sink, false)
		},
	}
}

func runMetadataNameBackfillCommand(ctx context.Context, f *cli.Factory, sink clispec.ResultSink, dryRun bool) error {
	env, err := NewEnv(ctx, f.Cfg)
	if err != nil {
		wrapped := fmt.Errorf("open metadata name index backfill environment: %w", err)
		slog.ErrorContext(ctx, "metadata_name_index.environment_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer env.Close()
	result, err := RunMetadataNameBackfill(ctx, env.Stores.NodeTypes, env.Stores.PropertyDefs, dryRun)
	if err != nil {
		return err
	}
	output := metadataNameBackfillOutput{
		ResultMarker: clispec.ResultMarker{},
		Command:      "ops.backfill.once-metadata-name-index",
		DryRun:       dryRun,
		Result:       result,
	}
	if err := clispec.WriteJSONValue(ctx, sink, output); err != nil {
		wrapped := fmt.Errorf("write metadata name index backfill result: %w", err)
		slog.ErrorContext(ctx, "metadata_name_index.report_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}
