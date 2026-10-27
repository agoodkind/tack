package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

type strictValidationOutput struct {
	clispec.ResultMarker
	Command string                 `json:"command"`
	DryRun  bool                   `json:"dry_run"`
	Result  StrictValidationReport `json:"result"`
}

// strictValidationOp declares the `ops verify strict-validation` command.
func strictValidationOp(f *cli.Factory) clispec.Operation[noInput] {
	return clispec.Operation[noInput]{
		Name:     clispec.Name{Canonical: "strict-validation", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsStrictValidation), Mutates: true},
		Group:    verifyGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Validate every node record and activate strict validation of ordinary writes",
		Long: "A dry run captures the write fence evidence, validates every node record against its organization schema, " +
			"and reports the invalid records without failing. With --execute and zero invalid records, the command activates strict " +
			"validation in one transaction that compares the evidence again. With --execute and invalid records, the command " +
			"exits with an error and does not activate strict validation. Before a run with --execute, " +
			"./server ops deploy verify --execute must pass on every guest that runs the app service. Activation " +
			"covers every organization in the FoundationDB cluster, and commands cannot remove the activation record.",
		Examples: nil,
		Args:     nil,
		Params:   nil,
		New:      func() noInput { return noInput{InputMarker: clispec.InputMarker{}} },
		DryRun: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return runStrictValidationCommand(ctx, f, sink, false)
		},
		Run: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return runStrictValidationCommand(ctx, f, sink, true)
		},
	}
}

func runStrictValidationCommand(ctx context.Context, f *cli.Factory, sink clispec.ResultSink, execute bool) error {
	env, err := NewEnv(ctx, f.Cfg)
	if err != nil {
		slog.ErrorContext(ctx, "strict.environment_failed", slog.String("err", err.Error()))
		return fmt.Errorf("open the strict validation environment: %w", err)
	}
	defer env.Close()
	schemas := storeSchemaSource{nodeTypes: env.Stores.NodeTypes, propertyDefs: env.Stores.PropertyDefs}
	report, runErr := RunStrictValidation(ctx, env.Stores.Nodes, schemas, execute)
	output := strictValidationOutput{ResultMarker: clispec.ResultMarker{}, Command: "ops.verify.strict-validation", DryRun: !execute, Result: report}
	if err := clispec.WriteJSONValue(ctx, sink, output); err != nil {
		slog.ErrorContext(ctx, "strict.report_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write the strict validation report: %w", err)
	}
	return runErr
}
