package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/adapters/postgres"
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

func orphanNodeBackfillOp(factory *cli.Factory) clispec.Operation[noInput] {
	operation := clispec.Operation[noInput]{
		Name: clispec.Name{Canonical: "orphan-nodes", CLIOverride: ""},
		Lifetime: clispec.Lifetime{
			Ticket:   "TACK-546",
			RemoveBy: time.Date(2026, time.November, 28, 0, 0, 0, 0, time.UTC),
		},
		Audit: audit.Spec{
			Verb:                       string(audit.VerbOpsBackfillOrphanNodes),
			Mutates:                    true,
			Atomic:                     false,
			Reads:                      false,
			CreatesAuditInfrastructure: false,
			Class:                      "",
			Targets:                    nil,
			SafeInput:                  nil,
		},
		Group:    opsGroup,
		Aliases:  nil,
		Hidden:   false,
		Examples: nil,
		Args:     nil,
		Params:   nil,
		Short:    "Delete the nodes without a hierarchy parent",
		Long: "The command scans the nodes of every organization for nodes of a type that lists CanLiveUnder types " +
			"and does not have a child_of edge to an existing parent. " +
			"The command refuses nodes with multiple child_of targets " +
			"when at least one target exists in the organization; repair the parent explicitly first. " +
			"A dry run reports the counts and node IDs. " +
			"With --execute, the command deletes each of those nodes with its hierarchy descendants. " +
			"A second run reports zero orphans.",
		New:    func() noInput { return noInput{InputMarker: clispec.InputMarker{}} },
		DryRun: nil,
		Run:    nil,
	}
	operation.DryRun = orphanNodeBackfillCallback(factory, true)
	operation.Run = orphanNodeBackfillCallback(factory, false)

	return operation
}

func orphanNodeBackfillCallback(
	factory *cli.Factory,
	dryRun bool,
) func(context.Context, noInput, clispec.ResultSink) error {
	return func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
		return runOrphanNodeBackfillCommand(ctx, factory, sink, dryRun)
	}
}

func runOrphanNodeBackfillCommand(
	ctx context.Context,
	f *cli.Factory,
	sink clispec.ResultSink,
	dryRun bool,
) error {
	env, err := NewEnv(ctx, f.Cfg)
	if err != nil {
		wrapped := fmt.Errorf("open orphan node backfill environment: %w", err)
		slog.ErrorContext(
			ctx,
			"orphan_nodes.environment_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}
	defer env.Close()
	nodes, err := service.NewNodeService(
		env.Stores.Nodes,
		env.Stores.Views,
		env.Stores.NodeTypes,
		env.Stores.PropertyDefs,
		env.Stores.Relationships,
		env.Stores.NodeDeleter,
		postgres.NewOrgMemberRepo(env.Pool),
		env.Stores.Relationships,
	)
	if err != nil {
		wrapped := fmt.Errorf("build the orphan node backfill node service: %w", err)
		slog.ErrorContext(
			ctx,
			"orphan_nodes.node_service_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	result, err := RunOrphanNodeBackfill(
		ctx,
		env.Stores.NodeDeleter,
		env.Stores.Relationships,
		nodes,
		dryRun,
	)
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
