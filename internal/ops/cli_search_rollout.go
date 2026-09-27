package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

type searchRolloutInput struct {
	clispec.InputMarker
	Authority string
	Candidate string
}

type searchRolloutOutput struct {
	clispec.ResultMarker
	AuthorityID      string   `json:"authority_id"`
	Phase            string   `json:"phase"`
	ActiveVersion    string   `json:"active_version"`
	CandidateVersion string   `json:"candidate_version,omitempty"`
	WriteVersions    []string `json:"write_versions"`
	Generation       int64    `json:"generation"`
	DryRun           bool     `json:"dry_run"`
}

type opsClockSource struct{}

func (opsClockSource) Now() time.Time { return opsNow() }

func (opsClockSource) Since(start time.Time) time.Duration { return opsNow().Sub(start) }

func searchRolloutOp(f *cli.Factory) clispec.Operation[searchRolloutInput] {
	return clispec.Operation[searchRolloutInput]{
		Name: clispec.Name{Canonical: "access-rollout", CLIOverride: ""}, Lifetime: clispec.Permanent,
		Audit: audit.Spec{Verb: string(audit.VerbOpsSearchAccessRollout), Mutates: true}, Group: searchGroup,
		Short: "Begin an access policy rollout for one permission authority",
		Params: []clispec.Param[searchRolloutInput]{
			clispec.StringParam("authority", "This parameter accepts the permission authority (organization) ID.", "", true, func(in *searchRolloutInput, value string) { in.Authority = value }),
			clispec.StringParam("candidate", "This parameter accepts the registered policy version to roll out.", "", true, func(in *searchRolloutInput, value string) { in.Candidate = value }),
		},
		New: func() searchRolloutInput {
			return searchRolloutInput{InputMarker: clispec.InputMarker{}, Authority: "", Candidate: ""}
		},
		DryRun: func(ctx context.Context, in searchRolloutInput, sink clispec.ResultSink) error {
			return runSearchRollout(ctx, f, in, sink, true)
		},
		Run: func(ctx context.Context, in searchRolloutInput, sink clispec.ResultSink) error {
			return runSearchRollout(ctx, f, in, sink, false)
		},
	}
}

func runSearchRollout(ctx context.Context, f *cli.Factory, in searchRolloutInput, sink clispec.ResultSink, dryRun bool) error {
	authorityID, err := uuid.Parse(in.Authority)
	if err != nil {
		return searchRolloutFailure(ctx, "parse authority "+in.Authority, err)
	}
	env, err := NewEnv(ctx, f.Cfg)
	if err != nil {
		return searchRolloutFailure(ctx, "open search rollout environment", err)
	}
	defer env.Close()
	rollouts := env.Stores.SearchRollouts(opsClockSource{}, env.Stores.SearchPolicySet())
	rollout, err := rollouts.Current(ctx, authorityID)
	if err != nil {
		return searchRolloutFailure(ctx, "read access rollout of authority "+authorityID.String(), err)
	}
	if !dryRun {
		rollout, err = rollouts.Begin(ctx, searchdomain.BeginAccessRollout{
			AuthorityID: authorityID, CandidateVersion: in.Candidate, ExpectedGeneration: rollout.Generation,
		})
		if err != nil {
			return searchRolloutFailure(ctx, "begin access rollout of authority "+authorityID.String(), err)
		}
	}
	output := searchRolloutOutput{
		ResultMarker: clispec.ResultMarker{}, AuthorityID: authorityID.String(), Phase: string(rollout.Phase),
		ActiveVersion: rollout.ActiveVersion, CandidateVersion: rollout.CandidateVersion,
		WriteVersions: rollout.WriteVersions, Generation: rollout.Generation, DryRun: dryRun,
	}
	if err := clispec.WriteJSONValue(ctx, sink, output); err != nil {
		return searchRolloutFailure(ctx, "write access rollout result", err)
	}
	return nil
}

func searchRolloutFailure(ctx context.Context, operation string, err error) error {
	wrapped := fmt.Errorf("%s: %w", operation, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.rollout.command_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
