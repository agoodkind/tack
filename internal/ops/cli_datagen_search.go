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
	Commit      bool
	PrepareOnly bool
	Seed        int
	Corpus      string
}

type datagenSearchResult struct {
	clispec.ResultMarker
	Command  string                  `json:"command"`
	DryRun   bool                    `json:"dry_run"`
	Verified bool                    `json:"verified"`
	Manifest *datagen.SearchManifest `json:"manifest,omitempty"`
}

func datagenSearchOp(f *cli.Factory) clispec.Operation[datagenSearchInput] {
	return clispec.Operation[datagenSearchInput]{
		Name:     clispec.Name{Canonical: "search", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsDatagenSearch), Mutates: true},
		Group:    datagenGroup,
		Short:    "Verify public search with an isolated opaque organization",
		Long: "Defaults to a dry run that validates the QA or local target. " +
			"Pass --commit to verify a fixture when public search is enabled. " +
			"Pass --prepare-only --commit with --seed and --corpus to export fixture IDs " +
			"without search requests; public search may remain disabled.",
		Params: []clispec.Param[datagenSearchInput]{
			clispec.BoolParam("commit", "create the search fixture and verify it after target validation", false, func(input *datagenSearchInput, value bool) { input.Commit = value }),
			clispec.BoolParam("prepare-only", "create isolated fixture nodes without enabling or calling public search", false, func(input *datagenSearchInput, value bool) { input.PrepareOnly = value }),
			clispec.IntParam("seed", "existing QA workspace seed for fixture preparation", 0, func(input *datagenSearchInput, value int) { input.Seed = value }),
			clispec.StringParam("corpus", "approved semantic corpus JSON file for fixture preparation", "", false, func(input *datagenSearchInput, value string) { input.Corpus = value }),
		},
		New: func() datagenSearchInput {
			return datagenSearchInput{InputMarker: clispec.InputMarker{}, Commit: false}
		},
		Run: func(ctx context.Context, input datagenSearchInput, sink clispec.ResultSink) error {
			return runDatagenSearch(ctx, f, input, sink)
		},
	}
}

// runDatagenSearch writes the created fixture identities to its result when preparation fails.
func runDatagenSearch(ctx context.Context, factory *cli.Factory, input datagenSearchInput, sink clispec.ResultSink) error {
	if input.PrepareOnly && (input.Seed <= 0 || input.Corpus == "") {
		return fmt.Errorf("qa datagen search: prepare-only requires a positive seed and corpus path")
	}
	if !input.PrepareOnly && (input.Seed != 0 || input.Corpus != "") {
		return fmt.Errorf("qa datagen search: seed and corpus require prepare-only")
	}
	if !input.Commit {
		if err := datagen.ValidateTarget(factory.Cfg); err != nil {
			slog.ErrorContext(ctx, "qa.datagen.search_refused", slog.String("err", err.Error()))
			return fmt.Errorf("qa datagen search: %w", err)
		}
	}
	var manifest *datagen.SearchManifest
	var preparationError error
	if input.Commit && input.PrepareOnly {
		prepared, err := datagen.PrepareSearchManifest(ctx, factory.Cfg, int64(input.Seed), input.Corpus)
		manifest, preparationError = &prepared, err
	}
	if input.Commit && !input.PrepareOnly {
		if err := datagen.VerifySearch(ctx, factory.Cfg); err != nil {
			slog.ErrorContext(ctx, "qa.datagen.search_failed", slog.String("err", err.Error()))
			return fmt.Errorf("qa datagen search: %w", err)
		}
	}
	if err := clispec.WriteJSONValue(ctx, sink, datagenSearchResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.qa.datagen.search",
		DryRun: !input.Commit, Verified: input.Commit && !input.PrepareOnly, Manifest: manifest,
	}); err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_result_failed", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen search: %w", err)
	}
	if preparationError != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_preparation_failed", slog.String("err", preparationError.Error()))
		return fmt.Errorf("qa datagen search: %w", preparationError)
	}
	return nil
}
