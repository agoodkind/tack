package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/runtime"
)

const defaultDatagenSeed = 245

var (
	qaGroup = &clispec.Group{
		Use: "qa", Short: "QA environment operations", Long: "", Parent: opsGroup,
	}
	datagenGroup = &clispec.Group{
		Use: "datagen", Short: "Generate request-faithful QA data",
		Long: "", Parent: qaGroup,
	}
)

type datagenSeedInput struct {
	clispec.InputMarker
	Scale          string
	Seed           int
	Commit         bool
	RedactAuditPII bool
}

type datagenSeedResult struct {
	clispec.ResultMarker
	Command    string `json:"command"`
	Scale      string `json:"scale"`
	Seed       int64  `json:"seed"`
	DryRun     bool   `json:"dry_run"`
	ToolCalls  int64  `json:"tool_calls"`
	Created    int    `json:"created"`
	Reused     int    `json:"reused"`
	Workspaces int    `json:"workspaces"`
	Projects   int    `json:"projects"`
	Issues     int    `json:"issues"`
}

func datagenSeedOp(f *cli.Factory) clispec.Operation[datagenSeedInput] {
	return clispec.Operation[datagenSeedInput]{
		Name:    clispec.Name{Canonical: "seed", CLIOverride: ""},
		Group:   datagenGroup,
		Aliases: nil,
		Hidden:  false,
		Short:   "Generate deterministic QA data through authenticated MCP calls",
		Long: "Defaults to a dry run. Pass --commit only where FoundationDB is " +
			"reachable and the app audit DSNs are present, such as the tack-app " +
			"container environment, not the default tack-ops container. Audit PII " +
			"redaction is opt-in and requires the synchronous audit writer.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[datagenSeedInput]{
			clispec.StringParam(
				"scale", "data volume: small, medium, or large", "small", false,
				func(input *datagenSeedInput, value string) { input.Scale = value },
			),
			clispec.IntParam(
				"seed", "deterministic content seed", defaultDatagenSeed,
				func(input *datagenSeedInput, value int) { input.Seed = value },
			),
			clispec.BoolParam(
				"commit", "send writes after target validation", false,
				func(input *datagenSeedInput, value bool) { input.Commit = value },
			),
			clispec.BoolParam(
				"redact-audit-pii",
				"redact one actor only with the synchronous audit writer",
				false,
				func(input *datagenSeedInput, value bool) {
					input.RedactAuditPII = value
				},
			),
		},
		New: func() datagenSeedInput {
			return datagenSeedInput{
				InputMarker: clispec.InputMarker{},
				Scale:       "small", Seed: defaultDatagenSeed, Commit: false,
				RedactAuditPII: false,
			}
		},
		Run: func(
			ctx context.Context,
			input datagenSeedInput,
			sink clispec.ResultSink,
		) error {
			return runDatagenSeed(ctx, f, input, sink)
		},
	}
}

func runDatagenSeed(
	ctx context.Context,
	factory *cli.Factory,
	input datagenSeedInput,
	sink clispec.ResultSink,
) error {
	scale, err := datagen.ParseScale(input.Scale)
	if err != nil {
		return err
	}
	content, err := datagen.NewContent(ctx, int64(input.Seed))
	if err != nil {
		return err
	}
	if !input.Commit {
		identities := datagen.PlanIdentities(factory.Cfg, int64(input.Seed), scale)
		driver := datagen.NewDriver(nil, true, int64(input.Seed))
		return runDatagenGenerator(
			ctx, factory, sink, driver, content, identities, scale, input, true,
		)
	}
	if err := datagen.ValidateTarget(factory.Cfg); err != nil {
		return err
	}
	graph, err := runtime.BuildGraph(ctx, factory.Cfg)
	if err != nil {
		slog.ErrorContext(ctx, "qa.datagen.runtime_failed", slog.String("err", err.Error()))
		return fmt.Errorf(
			"qa datagen: build runtime; run where FDB is reachable and the app audit environment is present: %w",
			err,
		)
	}
	defer graph.Close()
	identities, err := datagen.BootstrapIdentities(
		ctx,
		factory.Cfg,
		int64(input.Seed),
		scale,
	)
	if err != nil {
		return err
	}
	driver := datagen.NewDriver(graph, false, int64(input.Seed))
	return runDatagenGenerator(
		ctx, factory, sink, driver, content, identities, scale, input, false,
	)
}

func runDatagenGenerator(
	ctx context.Context,
	factory *cli.Factory,
	sink clispec.ResultSink,
	driver *datagen.Driver,
	content *datagen.Content,
	identities datagen.Identities,
	scale datagen.Scale,
	input datagenSeedInput,
	dryRun bool,
) error {
	generator := datagen.NewGenerator(
		driver,
		content,
		identities,
		scale,
		int64(input.Seed),
		datagen.GeneratorOptions{
			DryRun:         dryRun,
			ProductionAuth: factory.Cfg.Env != "development",
			RedactAuditPII: input.RedactAuditPII,
			SynchronousAudit: len(audit.SplitBrokers(factory.Cfg.AuditKafkaBrokers)) == 0 &&
				factory.Cfg.AuditWriterDSN != "",
		},
	)
	summary, err := generator.Run(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "qa.datagen.seed_failed", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen seed: %w", err)
	}
	return clispec.WriteJSONValue(ctx, sink, datagenSeedResult{
		ResultMarker: clispec.ResultMarker{},
		Command:      "ops.qa.datagen.seed",
		Scale:        summary.Scale,
		Seed:         summary.Seed,
		DryRun:       summary.DryRun,
		ToolCalls:    summary.ToolCalls,
		Created:      summary.Created,
		Reused:       summary.Reused,
		Workspaces:   summary.Workspaces,
		Projects:     summary.Projects,
		Issues:       summary.Issues,
	})
}
