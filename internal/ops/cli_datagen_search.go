package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/datagen"
)

type datagenSearchInput struct {
	clispec.InputMarker
	Commit       bool
	PrepareOnly  bool
	VerifyCohort bool
	Seed         int
	Corpus       string
}

type datagenSearchResult struct {
	clispec.ResultMarker
	Command  string                            `json:"command"`
	DryRun   bool                              `json:"dry_run"`
	Verified bool                              `json:"verified"`
	Manifest *datagen.SearchManifest           `json:"manifest,omitempty"`
	Cohort   *datagen.SearchCohortVerification `json:"cohort,omitempty"`
}

// errCohortNotVerified reports a cohort verification with a failed case.
var errCohortNotVerified = errors.New("at least one manifest case did not pass")

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
			"without search requests; public search may remain disabled. " +
			"Pass --verify-cohort --commit with a positive --seed to prepare a cohort from the embedded corpus. " +
			"The command calls public tack_search to check every manifest case.",
		Params: []clispec.Param[datagenSearchInput]{
			clispec.BoolParam("commit", "create the search fixture and verify it after target validation", false, func(input *datagenSearchInput, value bool) { input.Commit = value }),
			clispec.BoolParam("prepare-only", "create isolated fixture nodes without enabling or calling public search", false, func(input *datagenSearchInput, value bool) { input.PrepareOnly = value }),
			clispec.BoolParam("verify-cohort", "Prepare a cohort from the embedded corpus with --commit and a positive --seed. Check every manifest case with public tack_search.", false, func(input *datagenSearchInput, value bool) { input.VerifyCohort = value }),
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

// validateDatagenSearchInput rejects flag combinations before any write.
func validateDatagenSearchInput(input datagenSearchInput) error {
	if input.VerifyCohort && (input.PrepareOnly || input.Corpus != "") {
		return errors.New("qa datagen search: --verify-cohort conflicts with --prepare-only or a nonempty --corpus")
	}
	if input.VerifyCohort && input.Seed <= 0 {
		return errors.New("qa datagen search: --verify-cohort requires --seed greater than 0")
	}
	if input.VerifyCohort && !input.Commit {
		return errors.New("qa datagen search: --verify-cohort requires --commit")
	}
	if input.PrepareOnly && (input.Seed <= 0 || input.Corpus == "") {
		return errors.New("qa datagen search: --prepare-only requires --seed greater than 0 and a nonempty --corpus")
	}
	if !input.PrepareOnly && !input.VerifyCohort && (input.Seed != 0 || input.Corpus != "") {
		return errors.New("qa datagen search: --corpus and --seed without --verify-cohort require --prepare-only")
	}
	return nil
}

// runDatagenSearch writes the created fixture identities to its result when preparation fails.
func runDatagenSearch(ctx context.Context, factory *cli.Factory, input datagenSearchInput, sink clispec.ResultSink) error {
	if err := validateDatagenSearchInput(input); err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_input_refused", slog.String("err", err.Error()))
		return err
	}
	if !input.Commit {
		if err := datagen.ValidateTarget(factory.Cfg); err != nil {
			slog.ErrorContext(ctx, "qa.datagen.search_refused", slog.String("err", err.Error()))
			return fmt.Errorf("qa datagen search: %w", err)
		}
	}
	result := datagenSearchResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.qa.datagen.search",
		DryRun: !input.Commit, Verified: false, Manifest: nil, Cohort: nil,
	}
	var runError error
	switch {
	case !input.Commit:
	case input.PrepareOnly:
		prepared, err := datagen.PrepareSearchManifest(ctx, factory.Cfg, int64(input.Seed), input.Corpus)
		result.Manifest, runError = &prepared, err
	case input.VerifyCohort:
		prepared, cohort, err := datagen.VerifySearchCohort(ctx, factory.Cfg, int64(input.Seed))
		result.Manifest, result.Cohort, result.Verified, runError = &prepared, &cohort, cohort.Verified, err
		if runError == nil && !cohort.Verified {
			runError = errCohortNotVerified
		}
	default:
		if err := datagen.VerifySearch(ctx, factory.Cfg); err != nil {
			slog.ErrorContext(ctx, "qa.datagen.search_failed", slog.String("err", err.Error()))
			return fmt.Errorf("qa datagen search: %w", err)
		}
		result.Verified = true
	}
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_result_failed", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen search: %w", err)
	}
	if runError != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_preparation_failed", slog.String("err", runError.Error()))
		return fmt.Errorf("qa datagen search: %w", runError)
	}
	return nil
}
