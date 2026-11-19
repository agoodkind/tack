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

type parentRelationshipBackfillOutput struct {
	clispec.ResultMarker

	Command string                           `json:"command"`
	DryRun  bool                             `json:"dry_run"`
	Result  ParentRelationshipBackfillResult `json:"result"`
}

func parentRelationshipBackfillOp(factory *cli.Factory) clispec.Operation[noInput] {
	return clispec.Operation[noInput]{
		Name: clispec.Name{Canonical: "parent-relationships", CLIOverride: ""},
		Lifetime: clispec.Lifetime{
			Ticket:   "TACK-584",
			RemoveBy: time.Date(2026, time.December, 31, 0, 0, 0, 0, time.UTC),
		},
		Audit: audit.Spec{
			Verb:                       string(audit.VerbOpsBackfillParentRelationships),
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
		Short:    "Create missing child_of relationships from stored parent properties",
		Long: "Run with application writers stopped during validation and execution. " +
			"A dry run validates nodes and reports proposed changes. With --execute, the command creates " +
			"missing child_of relationships from parent_id and preserves stored properties and metadata. " +
			"Existing relationships remain unchanged. Repeating execution skips completed nodes.",
		New: func() noInput { return noInput{InputMarker: clispec.InputMarker{}} },
		DryRun: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return runParentRelationshipBackfillCommand(ctx, factory, sink, true)
		},
		Run: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return runParentRelationshipBackfillCommand(ctx, factory, sink, false)
		},
	}
}

func runParentRelationshipBackfillCommand(
	ctx context.Context,
	factory *cli.Factory,
	sink clispec.ResultSink,
	dryRun bool,
) error {
	env, err := NewEnv(ctx, factory.Cfg)
	if err != nil {
		wrapped := fmt.Errorf("open parent relationship migration environment: %w", err)
		slog.ErrorContext(
			ctx,
			"parent_relationships.environment_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}
	defer env.Close()
	result, err := RunParentRelationshipBackfill(
		ctx,
		env.Stores.Nodes,
		env.Stores.PropertyDefs,
		dryRun,
	)
	if err != nil {
		return err
	}

	return writeParentRelationshipBackfillOutput(ctx, sink, dryRun, result)
}

func writeParentRelationshipBackfillOutput(
	ctx context.Context,
	sink clispec.ResultSink,
	dryRun bool,
	result ParentRelationshipBackfillResult,
) error {
	output := parentRelationshipBackfillOutput{
		ResultMarker: clispec.ResultMarker{},
		Command:      "ops.backfill.once-parent-relationships",
		DryRun:       dryRun,
		Result:       result,
	}
	if err := clispec.WriteJSONValue(ctx, sink, output); err != nil {
		wrapped := fmt.Errorf("write parent relationship migration result: %w", err)
		slog.ErrorContext(
			ctx,
			"parent_relationships.result_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	return nil
}
