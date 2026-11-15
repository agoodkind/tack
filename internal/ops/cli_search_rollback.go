package ops

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

type searchRollbackInput struct {
	Authority string `json:"authority"`
}

func (searchRollbackInput) isCommandInput() {}

func searchRollbackCommand(f *cli.Factory) *cobra.Command {
	rollbackInput := searchRollbackInput{Authority: ""}
	cmd := &cobra.Command{
		Use:   "access-rollback",
		Short: "Roll back one permission authority's access policy rollout",
		Long:  "The command defaults to a dry run that prints the authority's current rollout state. With the execute flag, the command restores the previous active version before retirement begins and stops new writes from including the candidate version. The worker removes the candidate keys after sessions that use the candidate finish. The command refuses rollback after retirement begins.",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(
		&rollbackInput.Authority,
		"authority",
		"",
		"permission authority (organization) ID",
	)
	_ = cmd.MarkFlagRequired("authority")
	input := func() (json.RawMessage, error) { return encodeCommandInput(rollbackInput) }
	spec := audit.Spec{
		Verb:                       string(audit.VerbOpsSearchAccessRollback),
		Mutates:                    true,
		Class:                      audit.OperationExternal,
		Atomic:                     false,
		Reads:                      false,
		CreatesAuditInfrastructure: false,
		Targets: func(ctx context.Context, input json.RawMessage) (
			[]audit.Target,
			error,
		) {
			var decoded searchRollbackInput
			if err := json.Unmarshal(input, &decoded); err != nil {
				return nil, searchRolloutFailure(ctx, "decode access rollback input", err)
			}

			return []audit.Target{
				externalTarget(searchdomain.AccessRolloutTargetReference(decoded.Authority)),
			}, nil
		},
		SafeInput: func(input json.RawMessage) (json.RawMessage, error) { return input, nil },
	}
	clispec.AttachExternalPreview(
		cmd,
		f,
		spec,
		input,
		func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
			err := runSearchRollback(ctx, f, rollbackInput, clispec.NewCLISink(f), false, steps)
			if errors.Is(err, searchdomain.ErrNoRolloutInProgress) ||
				errors.Is(err, searchdomain.ErrRetirementStarted) ||
				errors.Is(err, searchdomain.ErrRolloutChanged) {
				return commandOutcome(audit.OutcomeRefused, "rollback_not_allowed", err), err
			}

			if err != nil {
				return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
			}

			return commandOutcome(audit.OutcomeCompleted, "", nil), nil
		},
		recordedSearchPreview(
			f,
			spec,
			input,
			func(ctx context.Context, sink clispec.ResultSink) error {
				return runSearchRollback(ctx, f, rollbackInput, sink, true, nil)
			},
		),
	)

	return cmd
}

func runSearchRollback(
	ctx context.Context,
	f *cli.Factory,
	input searchRollbackInput,
	sink clispec.ResultSink,
	dryRun bool,
	steps audit.StepRecorder,
) error {
	authorityID, err := uuid.Parse(input.Authority)
	if err != nil {
		return searchRolloutFailure(ctx, "parse authority "+input.Authority, err)
	}

	env, err := NewEnv(ctx, f.Cfg)
	if err != nil {
		return searchRolloutFailure(ctx, "open search rollback environment", err)
	}
	defer env.Close()
	rollouts := env.Stores.SearchRollouts(opsClockSource{}, env.Stores.SearchPolicySet())
	rollout, err := rollouts.Current(ctx, authorityID)
	if err != nil {
		return searchRolloutFailure(
			ctx,
			"read access rollout of authority "+authorityID.String(),
			err,
		)
	}

	if !dryRun {
		initiation, initiationErr := commandInitiation(ctx)
		if initiationErr != nil {
			return initiationErr
		}

		planned := accessRolloutEvidence{
			AuthorityID:        authorityID.String(),
			CandidateVersion:   rollout.CandidateVersion,
			ExpectedGeneration: rollout.Generation,
			ActiveVersion:      rollout.ActiveVersion,
		}
		_, err = searchStep(
			ctx,
			steps,
			searchdomain.AccessRolloutTargetReference(authorityID.String()),
			planned,
			func(ctx context.Context) error {
				var rollbackErr error

				rollout, rollbackErr = rollouts.Rollback(
					ctx,
					searchdomain.RollbackAccessRollout{
						AuthorityID:        authorityID,
						ExpectedGeneration: planned.ExpectedGeneration,
						Initiation:         initiation,
					},
				)
				if rollbackErr != nil {
					return searchRolloutFailure(
						ctx,
						"restore access rollout of authority "+authorityID.String(),
						rollbackErr,
					)
				}

				return nil
			},
		)
		if err != nil {
			return searchRolloutFailure(
				ctx,
				"roll back access rollout of authority "+authorityID.String(),
				err,
			)
		}
	}

	return writeSearchRolloutResult(ctx, sink, rollouts, rollout, dryRun)
}
