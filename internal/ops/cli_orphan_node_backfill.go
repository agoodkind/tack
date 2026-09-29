package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/service"
)

type orphanNodeBackfillOutput struct {
	clispec.ResultMarker
	Command string               `json:"command"`
	DryRun  bool                 `json:"dry_run"`
	Result  OrphanBackfillResult `json:"result"`
}

func orphanNodeBackfillOp(f *cli.Factory) clispec.Operation[noInput] {
	return clispec.Operation[noInput]{
		Name:     clispec.Name{Canonical: "orphan-nodes", CLIOverride: ""},
		Lifetime: clispec.Lifetime{Ticket: "TACK-546", RemoveBy: time.Date(2026, time.November, 28, 0, 0, 0, 0, time.UTC)},
		Audit:    audit.Spec{Verb: string(audit.VerbOpsBackfillOrphanNodes), Mutates: true},
		Group:    opsGroup,
		Short:    "Delete the nodes without a hierarchy parent and their descendants",
		Long: "The command scans the nodes of every organization for nodes of a type that lists CanLiveUnder types " +
			"and has no edge to an existing parent. A dry run reports the count and the node IDs. " +
			"With --execute, the command deletes each of those nodes and its hierarchy descendants. " +
			"A second run reports zero orphans.",
		New: func() noInput { return noInput{InputMarker: clispec.InputMarker{}} },
		DryRun: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return runOrphanNodeBackfillCommand(ctx, f, sink, true)
		},
		Run: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return runOrphanNodeBackfillCommand(ctx, f, sink, false)
		},
	}
}

func runOrphanNodeBackfillCommand(ctx context.Context, f *cli.Factory, sink clispec.ResultSink, dryRun bool) error {
	env, err := NewEnv(ctx, f.Cfg)
	if err != nil {
		wrapped := fmt.Errorf("open orphan node backfill environment: %w", err)
		slog.ErrorContext(ctx, "orphan_nodes.environment_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer env.Close()
	nodes := service.NewNodeService(
		env.Stores.Nodes, env.Stores.Views, env.Stores.NodeTypes, env.Stores.PropertyDefs,
		env.Stores.Relationships, env.Stores.NodeDeleter,
	)
	result, err := RunOrphanNodeBackfill(ctx, env.Stores.NodeDeleter, nodes, dryRun)
	if err != nil {
		return err
	}
	output := orphanNodeBackfillOutput{
		ResultMarker: clispec.ResultMarker{},
		Command:      orphanBackfillTool,
		DryRun:       dryRun,
		Result:       result,
	}
	if err := clispec.WriteJSONValue(ctx, sink, output); err != nil {
		wrapped := fmt.Errorf("write orphan node backfill result: %w", err)
		slog.ErrorContext(ctx, "orphan_nodes.report_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}
