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

type datagenSearchInput struct {
	clispec.InputMarker
	Commit bool
}

type datagenSearchResult struct {
	clispec.ResultMarker
	Command  string `json:"command"`
	DryRun   bool   `json:"dry_run"`
	Verified bool   `json:"verified"`
}

func datagenSearchOp(f *cli.Factory) clispec.Operation[datagenSearchInput] {
	return clispec.Operation[datagenSearchInput]{
		Name:     clispec.Name{Canonical: "search", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsDatagenSearch), Mutates: true},
		Group:    datagenGroup,
		Short:    "Verify public search with an isolated opaque organization",
		Long: "Defaults to a dry run that validates the target only. Pass --commit in the app " +
			"environment of a QA or local target with public search enabled.",
		Params: []clispec.Param[datagenSearchInput]{
			clispec.BoolParam("commit", "create the search fixture and verify it after target validation", false, func(input *datagenSearchInput, value bool) { input.Commit = value }),
		},
		New: func() datagenSearchInput {
			return datagenSearchInput{InputMarker: clispec.InputMarker{}, Commit: false}
		},
		Run: func(ctx context.Context, input datagenSearchInput, sink clispec.ResultSink) error {
			return runDatagenSearch(ctx, f, input, sink)
		},
	}
}

// runDatagenSearch validates the target once. A dry run calls ValidateTarget.
// A committed run calls VerifySearch, which validates the target before it
// writes anything.
func runDatagenSearch(ctx context.Context, factory *cli.Factory, input datagenSearchInput, sink clispec.ResultSink) error {
	if !input.Commit {
		if err := datagen.ValidateTarget(factory.Cfg); err != nil {
			slog.ErrorContext(ctx, "qa.datagen.search_refused", slog.String("err", err.Error()))
			return fmt.Errorf("qa datagen search: %w", err)
		}
	}
	if input.Commit {
		if err := datagen.VerifySearch(ctx, factory.Cfg); err != nil {
			slog.ErrorContext(ctx, "qa.datagen.search_failed", slog.String("err", err.Error()))
			return fmt.Errorf("qa datagen search: %w", err)
		}
	}
	if err := clispec.WriteJSONValue(ctx, sink, datagenSearchResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.qa.datagen.search",
		DryRun: !input.Commit, Verified: input.Commit,
	}); err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_result_failed", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen search: %w", err)
	}
	return nil
}
