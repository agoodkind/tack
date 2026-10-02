package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/datagen"
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
	// AuditConsumerOffsets is the `ops audit consumer-offsets` report read
	// after a committed seed. It is nil after a dry run.
	AuditConsumerOffsets *auditConsumerOffsetsReport `json:"audit_consumer_offsets,omitempty"`
}

func datagenSeedOp(f *cli.Factory) clispec.Operation[datagenSeedInput] {
	return clispec.Operation[datagenSeedInput]{
		Name:     clispec.Name{Canonical: "seed", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsDatagenSeed), Mutates: true},
		Group:    datagenGroup,
		Short:    "Generate deterministic QA data through authenticated MCP calls",
		Long: "Defaults to a dry run. Pass --commit only where FoundationDB is " +
			"reachable and the app audit DSNs are present, such as the tack-app " +
			"container environment, not the default tack-ops container. Audit PII " +
			"redaction is opt-in and runs the host redaction path for one actor.",
		Params: []clispec.Param[datagenSeedInput]{
			clispec.StringParam("scale", "data volume: small, medium, or large", "small", false, func(input *datagenSeedInput, value string) { input.Scale = value }),
			clispec.IntParam("seed", "deterministic content seed", defaultDatagenSeed, func(input *datagenSeedInput, value int) { input.Seed = value }),
			clispec.BoolParam("commit", "send writes after target validation", false, func(input *datagenSeedInput, value bool) { input.Commit = value }),
			clispec.BoolParam("redact-audit-pii", "erase one generated actor's audit PII through the host redaction path", false, func(input *datagenSeedInput, value bool) { input.RedactAuditPII = value }),
		},
		New: func() datagenSeedInput {
			return datagenSeedInput{InputMarker: clispec.InputMarker{}, Scale: "small", Seed: defaultDatagenSeed}
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
	if input.Commit {
		if err := datagen.ValidateTarget(factory.Cfg); err != nil {
			return err
		}
	}
	summary, err := datagen.RunSeed(ctx, factory.Cfg, datagen.SeedRunOptions{
		Scale: input.Scale, Seed: int64(input.Seed), Commit: input.Commit,
		RedactAuditPII: input.RedactAuditPII,
	})
	if err != nil {
		slog.ErrorContext(ctx, "qa.datagen.seed_failed", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen seed: %w", err)
	}
	var remainders *auditConsumerOffsetsReport
	if input.Commit {
		report, readErr := readAuditConsumerOffsets(ctx, factory)
		if readErr != nil {
			return fmt.Errorf("qa datagen seed: %w", readErr)
		}
		remainders = &report
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

		AuditConsumerOffsets: remainders,
	})
}
