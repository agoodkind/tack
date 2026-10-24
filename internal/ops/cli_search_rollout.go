package ops

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	invalidInputCode      = "input_invalid"
	rolloutInProgressCode = "rollout_in_progress"
)

var errUnknownCandidate = errors.New("the candidate is not a registered access policy version")

type searchRolloutInput struct {
	Authority string `json:"authority"`
	Candidate string `json:"candidate"`
}

func (searchRolloutInput) isCommandInput() {}

type opsClockSource struct{}

func (opsClockSource) Now() time.Time { return opsNow() }

func (opsClockSource) Since(start time.Time) time.Duration { return opsNow().Sub(start) }

func searchRolloutCommand(f *cli.Factory) *cobra.Command {
	in := searchRolloutInput{Authority: "", Candidate: ""}
	cmd := &cobra.Command{
		Use: "access-rollout", Short: "Begin an access policy rollout for one permission authority.", Args: cobra.NoArgs,
	}
	cmd.Flags().StringVar(&in.Authority, "authority", "", "Specify the permission authority (organization) UUID.")
	cmd.Flags().StringVar(&in.Candidate, "candidate", "", "Specify the registered policy version to roll out.")
	_ = cmd.MarkFlagRequired("authority")
	_ = cmd.MarkFlagRequired("candidate")
	input := func() (json.RawMessage, error) { return encodeCommandInput(in) }
	clispec.AttachExternalPreview(cmd, f, searchRolloutSpec(), input,
		func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
			return runSearchRolloutRecorded(ctx, f, in, clispec.NewCLISink(f), steps)
		},
		func(ctx context.Context) error {
			return previewSearchRollout(ctx, f, in, clispec.NewCLISink(f))
		})
	return cmd
}

func searchRolloutSpec() audit.Spec {
	return audit.Spec{
		Verb: string(audit.VerbOpsSearchAccessRollout), Mutates: true, Class: audit.OperationExternal,
		Targets: func(ctx context.Context, input json.RawMessage) ([]audit.Target, error) {
			var decoded searchRolloutInput
			if err := json.Unmarshal(input, &decoded); err != nil {
				return nil, searchRolloutFailure(ctx, "decode access rollout input", err)
			}
			return []audit.Target{externalTarget(searchdomain.AccessRolloutTargetReference(decoded.Authority))}, nil
		},
		SafeInput: func(input json.RawMessage) (json.RawMessage, error) { return input, nil },
	}
}

type accessRolloutEvidence struct {
	AuthorityID        string   `json:"authority_id"`
	CandidateVersion   string   `json:"candidate_version"`
	ExpectedGeneration int64    `json:"expected_generation"`
	ActiveVersion      string   `json:"active_version"`
	Phase              string   `json:"phase,omitempty"`
	WriteVersions      []string `json:"write_versions,omitempty"`
	Generation         int64    `json:"generation,omitempty"`
}

func (accessRolloutEvidence) isStepEvidence() {}

func runSearchRolloutRecorded(
	ctx context.Context,
	f *cli.Factory,
	in searchRolloutInput,
	sink clispec.ResultSink,
	steps audit.StepRecorder,
) (audit.OutcomeDetails, error) {
	authorityID, err := uuid.Parse(in.Authority)
	if err != nil {
		wrapped := searchRolloutFailure(ctx, "parse authority "+in.Authority, err)
		return commandOutcome(audit.OutcomeRefused, invalidInputCode, wrapped), wrapped
	}
	env, err := NewEnv(ctx, f.Cfg)
	if err != nil {
		wrapped := searchRolloutFailure(ctx, "open search rollout environment", err)
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, wrapped), wrapped
	}
	defer env.Close()
	policies := env.Stores.SearchPolicySet()
	if !policies.Supports(in.Candidate) {
		wrapped := searchRolloutFailure(ctx, "check candidate version "+in.Candidate, errUnknownCandidate)
		return commandOutcome(audit.OutcomeRefused, invalidInputCode, wrapped), wrapped
	}
	rollouts := env.Stores.SearchRollouts(opsClockSource{}, policies)
	current, err := rollouts.Current(ctx, authorityID)
	if err != nil {
		wrapped := searchRolloutFailure(ctx, "read access rollout of authority "+authorityID.String(), err)
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, wrapped), wrapped
	}
	if current.Phase != searchdomain.AccessStable {
		refused := commandOutcome(audit.OutcomeRefused, rolloutInProgressCode, searchdomain.ErrRolloutInProgress)
		return refused, writeSearchRolloutResult(ctx, sink, rollouts, current, false)
	}
	planned := accessRolloutEvidence{
		AuthorityID: authorityID.String(), CandidateVersion: in.Candidate, ExpectedGeneration: current.Generation,
		ActiveVersion: current.ActiveVersion, Phase: "", WriteVersions: nil, Generation: 0,
	}
	var begun searchdomain.AccessRollout
	details, err := searchStep(ctx, steps, searchdomain.AccessRolloutTargetReference(authorityID.String()), planned,
		func(ctx context.Context) error {
			rollout, beginErr := rollouts.Begin(ctx, searchdomain.BeginAccessRollout{
				AuthorityID: authorityID, CandidateVersion: in.Candidate, ExpectedGeneration: current.Generation,
			})
			if beginErr != nil {
				return searchRolloutFailure(ctx, "begin access rollout of authority "+authorityID.String(), beginErr)
			}
			begun = rollout
			return nil
		},
		func(ctx context.Context) (json.RawMessage, error) {
			observed := planned
			observed.Phase, observed.WriteVersions, observed.Generation = string(begun.Phase), begun.WriteVersions, begun.Generation
			return encodeEvidence(ctx, observed)
		})
	if err != nil {
		return details, err
	}
	return details, writeSearchRolloutResult(ctx, sink, rollouts, begun, false)
}

func previewSearchRollout(ctx context.Context, f *cli.Factory, in searchRolloutInput, sink clispec.ResultSink) error {
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
	current, err := rollouts.Current(ctx, authorityID)
	if err != nil {
		return searchRolloutFailure(ctx, "read access rollout of authority "+authorityID.String(), err)
	}
	return writeSearchRolloutResult(ctx, sink, rollouts, current, true)
}
