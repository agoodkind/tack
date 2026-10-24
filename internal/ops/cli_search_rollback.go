package ops

import (
	"context"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

type searchRollbackInput struct {
	clispec.InputMarker
	Authority string
}

func searchRollbackOp(f *cli.Factory) clispec.Operation[searchRollbackInput] {
	return clispec.Operation[searchRollbackInput]{
		Name: clispec.Name{Canonical: "access-rollback", CLIOverride: ""}, Lifetime: clispec.Permanent,
		Audit: audit.Spec{Verb: string(audit.VerbOpsSearchAccessRollback), Mutates: true}, Group: searchGroup,
		Short: "Roll back one permission authority's access policy rollout",
		Long:  "The command defaults to a dry run that prints the authority's current rollout state. With the execute flag, the command restores the previous active version before retirement begins and stops new writes from including the candidate version. The worker removes the candidate keys after sessions that use the candidate finish. The command refuses rollback after retirement begins.",
		Params: []clispec.Param[searchRollbackInput]{
			clispec.StringParam("authority", "permission authority (organization) ID", "", true, func(in *searchRollbackInput, value string) { in.Authority = value }),
		},
		New: func() searchRollbackInput {
			return searchRollbackInput{InputMarker: clispec.InputMarker{}, Authority: ""}
		},
		DryRun: func(ctx context.Context, in searchRollbackInput, sink clispec.ResultSink) error {
			return runSearchRollback(ctx, f, in, sink, true)
		},
		Run: func(ctx context.Context, in searchRollbackInput, sink clispec.ResultSink) error {
			return runSearchRollback(ctx, f, in, sink, false)
		},
	}
}

func runSearchRollback(ctx context.Context, f *cli.Factory, in searchRollbackInput, sink clispec.ResultSink, dryRun bool) error {
	authorityID, err := uuid.Parse(in.Authority)
	if err != nil {
		return searchRolloutFailure(ctx, "parse authority "+in.Authority, err)
	}
	env, err := NewEnv(ctx, f.Cfg)
	if err != nil {
		return searchRolloutFailure(ctx, "open search rollback environment", err)
	}
	defer env.Close()
	rollouts := env.Stores.SearchRollouts(opsClockSource{}, env.Stores.SearchPolicySet())
	rollout, err := rollouts.Current(ctx, authorityID)
	if err != nil {
		return searchRolloutFailure(ctx, "read access rollout of authority "+authorityID.String(), err)
	}
	if !dryRun {
		rollout, err = rollouts.Rollback(ctx, searchdomain.RollbackAccessRollout{
			AuthorityID: authorityID, ExpectedGeneration: rollout.Generation,
		})
		if err != nil {
			return searchRolloutFailure(ctx, "roll back access rollout of authority "+authorityID.String(), err)
		}
	}
	return writeSearchRolloutResult(ctx, sink, rollouts, rollout, dryRun)
}
