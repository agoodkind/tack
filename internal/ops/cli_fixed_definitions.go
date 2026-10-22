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
	"goodkind.io/tack/internal/telemetry"
)

// fixedDefinitionsCommand is the command name that the correction audit
// events record.
const fixedDefinitionsCommand = "ops.backfill.once-fixed-definitions"

type fixedDefinitionsInput struct {
	clispec.InputMarker
}

type fixedDefinitionsOutput struct {
	clispec.ResultMarker
	Command   string                        `json:"command"`
	DryRun    bool                          `json:"dry_run"`
	Result    service.FixedDefinitionResult `json:"result"`
	NodeTypes service.FixedNodeTypeResult   `json:"node_types"`
}

func fixedDefinitionsOp(f *cli.Factory) clispec.Operation[fixedDefinitionsInput] {
	return clispec.Operation[fixedDefinitionsInput]{
		Name:     clispec.Name{Canonical: "fixed-definitions", CLIOverride: ""},
		Lifetime: clispec.Lifetime{Ticket: "TACK-580", RemoveBy: time.Date(2026, time.December, 31, 0, 0, 0, 0, time.UTC)},
		Audit:    audit.Spec{Verb: string(audit.VerbOpsBackfillFixedDefinitions), Mutates: true},
		Group:    opsGroup,
		Short:    "Apply seeded search exclusions, applicability, and parent types to stored built-in metadata",
		Params:   nil,
		New: func() fixedDefinitionsInput {
			return fixedDefinitionsInput{InputMarker: clispec.InputMarker{}}
		},
		DryRun: func(ctx context.Context, in fixedDefinitionsInput, sink clispec.ResultSink) error {
			return runFixedDefinitionsCommand(ctx, f, sink, true)
		},
		Run: func(ctx context.Context, in fixedDefinitionsInput, sink clispec.ResultSink) error {
			return runFixedDefinitionsCommand(ctx, f, sink, false)
		},
	}
}

// RunFixedDefinitionCorrection corrects stored built-in property
// definitions in every organization through store. When dryRun is false,
// it stages a property_def.update audit event for each correction with the
// operator principal from ctx. A dry run does not write.
func RunFixedDefinitionCorrection(ctx context.Context, store service.FixedDefinitionStore, dryRun bool) (service.FixedDefinitionResult, error) {
	result, err := service.CorrectFixedDefinitions(ctx, store, stageFixedDefinitionAudit, dryRun)
	if err != nil {
		wrapped := fmt.Errorf("run fixed definition correction: %w", err)
		slog.ErrorContext(ctx, "metadata.fixed_definition.run_failed", slog.String("err", wrapped.Error()))
		return result, wrapped
	}
	return result, nil
}

func runFixedDefinitionsCommand(ctx context.Context, f *cli.Factory, sink clispec.ResultSink, dryRun bool) error {
	env, err := NewEnv(ctx, f.Cfg)
	if err != nil {
		wrapped := fmt.Errorf("open fixed definition correction environment: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "metadata.fixed_definition.environment_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer env.Close()
	result, err := RunFixedDefinitionCorrection(ctx, env.Stores.PropertyDefs, dryRun)
	if err != nil {
		return err
	}
	nodeTypes, err := RunFixedNodeTypeCorrection(ctx, env.Stores.NodeTypes, dryRun)
	if err != nil {
		return err
	}
	output := fixedDefinitionsOutput{
		ResultMarker: clispec.ResultMarker{}, Command: fixedDefinitionsCommand, DryRun: dryRun, Result: result, NodeTypes: nodeTypes,
	}
	if err := clispec.WriteJSONValue(ctx, sink, output); err != nil {
		wrapped := fmt.Errorf("write fixed definition correction result: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "metadata.fixed_definition.report_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}
