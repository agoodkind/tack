package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/datagen"
)

const (
	defaultDatagenSeed         = 245
	defaultDatagenSoakDuration = "30s"
	defaultDatagenSoakRate     = 5
)

var (
	qaGroup = &clispec.Group{
		Use: "qa", Short: "QA environment operations", Long: "", Parent: opsGroup,
	}
	datagenGroup = &clispec.Group{
		Use: "datagen", Short: "Generate request-faithful QA data",
		Long: "", Parent: qaGroup,
	}
)

type datagenSoakInput struct {
	clispec.InputMarker
	Duration string
	Rate     int
	MaxOps   int
	Scale    string
	Seed     int
	Commit   bool
}

type datagenSoakResult struct {
	clispec.ResultMarker
	Command       string `json:"command"`
	Seed          int64  `json:"seed"`
	DryRun        bool   `json:"dry_run"`
	StopReason    string `json:"stop_reason"`
	Operations    int    `json:"operations"`
	Created       int    `json:"created"`
	Updated       int    `json:"updated"`
	Relationships int    `json:"relationships"`
	Comments      int    `json:"comments"`
	Reads         int    `json:"reads"`
	QuietOps      int    `json:"quiet_ops"`
	SpikeOps      int    `json:"spike_ops"`
	Elapsed       string `json:"elapsed"`
}

func datagenSoakOp(f *cli.Factory) clispec.Operation[datagenSoakInput] {
	return clispec.Operation[datagenSoakInput]{
		Name:     clispec.Name{Canonical: "soak", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsDatagenSoak), Mutates: true},
		Group:    datagenGroup,
		Short:    "Run continuous bursty QA traffic through authenticated MCP calls",
		Long: "Defaults to a dry run. A zero duration runs until SIGINT or SIGTERM. " +
			"Pass --commit only in the same app environment required by seed.",
		Params: []clispec.Param[datagenSoakInput]{
			clispec.StringParam("duration", "run duration; zero waits for a signal", defaultDatagenSoakDuration, false, func(input *datagenSoakInput, value string) { input.Duration = value }),
			clispec.IntParam("rate", "target average operations per second", defaultDatagenSoakRate, func(input *datagenSoakInput, value int) { input.Rate = value }),
			clispec.IntParam("max-ops", "hard operation cap; zero is unlimited", 0, func(input *datagenSoakInput, value int) { input.MaxOps = value }),
			clispec.StringParam("scale", "initial corpus scale", "small", false, func(input *datagenSoakInput, value string) { input.Scale = value }),
			clispec.IntParam("seed", "deterministic operation seed", defaultDatagenSeed, func(input *datagenSoakInput, value int) { input.Seed = value }),
			clispec.BoolParam("commit", "send writes after target validation", false, func(input *datagenSoakInput, value bool) { input.Commit = value }),
		},
		New: func() datagenSoakInput {
			return datagenSoakInput{InputMarker: clispec.InputMarker{}, Duration: defaultDatagenSoakDuration, Rate: defaultDatagenSoakRate, Scale: "small", Seed: defaultDatagenSeed}
		},
		Run: func(ctx context.Context, input datagenSoakInput, sink clispec.ResultSink) error {
			return runDatagenSoak(ctx, f, input, sink)
		},
	}
}

func runDatagenSoak(ctx context.Context, factory *cli.Factory, input datagenSoakInput, sink clispec.ResultSink) error {
	duration, err := time.ParseDuration(input.Duration)
	if err != nil {
		return fmt.Errorf("qa datagen soak: parse duration %q: %w", input.Duration, err)
	}
	if input.Commit {
		if err := datagen.ValidateTarget(factory.Cfg); err != nil {
			return err
		}
	}
	signalContext, stopSignals := signal.NotifyContext(
		ctx, os.Interrupt, syscall.SIGTERM,
	)
	defer stopSignals()
	callContext := context.WithoutCancel(signalContext)
	summary, err := datagen.RunSoak(signalContext, callContext, factory.Cfg, datagen.SoakOptions{
		Duration: duration, Rate: input.Rate, MaxOps: input.MaxOps,
		Scale: input.Scale, Seed: int64(input.Seed), Commit: input.Commit,
	}, signalContext.Done())
	if err != nil {
		slog.ErrorContext(ctx, "qa.datagen.soak_failed", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen soak: %w", err)
	}
	return clispec.WriteJSONValue(ctx, sink, datagenSoakResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.qa.datagen.soak",
		Seed: summary.Seed, DryRun: summary.DryRun, StopReason: summary.StopReason,
		Operations: summary.Operations, Created: summary.Created, Updated: summary.Updated,
		Relationships: summary.Relationships, Comments: summary.Comments, Reads: summary.Reads,
		QuietOps: summary.QuietOps, SpikeOps: summary.SpikeOps, Elapsed: summary.Elapsed.String(),
	})
}
