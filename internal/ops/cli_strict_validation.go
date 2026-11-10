package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// StrictValidationOutput is the JSON result of ops verify strict-validation.
type StrictValidationOutput struct {
	clispec.ResultMarker
	Command string `json:"command"`
	DryRun  bool   `json:"dry_run"`
	// MaxViolations is the --max-violations value of the run.
	MaxViolations int                    `json:"max_violations"`
	Result        StrictValidationReport `json:"result"`
}

type strictValidationInput struct {
	clispec.InputMarker
	MaxViolations int
}

func strictValidationOp(f *cli.Factory) clispec.Operation[strictValidationInput] {
	return clispec.Operation[strictValidationInput]{
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
			"covers every organization in the FoundationDB cluster, and commands cannot remove the activation record. " +
			"The report counts every invalid record and lists the first --max-violations of them by organization ID and node ID.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[strictValidationInput]{
			clispec.IntParam("max-violations",
				fmt.Sprintf("how many invalid records the report lists, from 1 through %d", maxReportedViolations),
				defaultReportedViolations,
				func(input *strictValidationInput, value int) { input.MaxViolations = value }),
		},
		New: func() strictValidationInput {
			return strictValidationInput{InputMarker: clispec.InputMarker{}, MaxViolations: defaultReportedViolations}
		},
		DryRun: func(ctx context.Context, input strictValidationInput, sink clispec.ResultSink) error {
			return runStrictValidationCommand(ctx, f, input, sink, false)
		},
		Run: func(ctx context.Context, input strictValidationInput, sink clispec.ResultSink) error {
			return runStrictValidationCommand(ctx, f, input, sink, true)
		},
	}
}

func runStrictValidationCommand(ctx context.Context, f *cli.Factory, input strictValidationInput, sink clispec.ResultSink, execute bool) error {
	if err := checkReportedViolations(ctx, input.MaxViolations); err != nil {
		return err
	}
	env, err := NewEnv(ctx, f.Cfg)
	if err != nil {
		slog.ErrorContext(ctx, "strict.environment_failed", slog.String("err", err.Error()))
		return fmt.Errorf("open the strict validation environment: %w", err)
	}
	defer env.Close()
	schemas := storeSchemaSource{nodeTypes: env.Stores.NodeTypes, propertyDefs: env.Stores.PropertyDefs}
	report, runErr := RunStrictValidation(ctx, env.Stores.Nodes, schemas, execute, input.MaxViolations)
	output := StrictValidationOutput{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.verify.strict-validation", DryRun: !execute,
		MaxViolations: input.MaxViolations, Result: report,
	}
	if err := clispec.WriteJSONValue(ctx, sink, output); err != nil {
		slog.ErrorContext(ctx, "strict.report_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write the strict validation report: %w", err)
	}
	return runErr
}
