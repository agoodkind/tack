package ops

import (
	"context"
	"fmt"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// replacementInProgressCode is the error code for a reindex run that does not
// begin a replacement because another replacement is in progress.
const replacementInProgressCode = "replacement_in_progress"

// indexReplacementEvidence records the selected index and requested settings.
type indexReplacementEvidence struct {
	Mode          string `json:"mode"`
	SourceIndex   string `json:"source_index"`
	PrimaryShards int    `json:"primary_shards"`
	RoutingShards int    `json:"routing_shards"`
	Replicas      int    `json:"replicas"`
	Restored      bool   `json:"restored"`
}

func (indexReplacementEvidence) isStepEvidence() {}

// runSearchReindexRecorded records an index replacement as one step on the
// serving index and prints the replacement. If another replacement is in
// progress, it prints that replacement and returns a refused outcome without
// starting a step.
func runSearchReindexRecorded(
	ctx context.Context,
	f *cli.Factory,
	in searchReindexInput,
	sink clispec.ResultSink,
	steps audit.StepRecorder,
) (audit.OutcomeDetails, error) {
	env, err := NewEnv(ctx, f.Cfg)
	if err != nil {
		wrapped := searchReindexFailure(ctx, "open search reindex environment", err)
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, wrapped), wrapped
	}
	defer env.Close()
	plan, err := planSearchReindex(ctx, f, env, in)
	if err != nil {
		return commandOutcome(audit.OutcomeRefused, commandConfigCode, err), err
	}

	if plan.running {
		refused := commandOutcome(
			audit.OutcomeRefused,
			replacementInProgressCode,
			fmt.Errorf(
				"index replacement %s from %s is in progress",
				plan.current.ID,
				plan.current.SourceIndex,
			),
		)

		return refused, writeReindexResult(ctx, sink, plan.current, true, false)
	}

	plan.request.Initiation, err = commandInitiation(ctx)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}

	planned := indexReplacementEvidence{
		Mode:          string(plan.request.Mode),
		SourceIndex:   plan.serving,
		PrimaryShards: plan.request.PrimaryShards,
		RoutingShards: plan.request.RoutingShards,
		Replicas:      plan.request.Replicas,
		Restored:      plan.request.Restored,
	}
	plannedJSON, err := encodeEvidence(ctx, planned)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}

	var begun searchdomain.Rebuild
	step := audit.ExternalStep(
		externalTarget("opensearch/index/"+plan.serving+"/replacement"),
		plannedJSON,
	)
	details, err := recordedStep(
		ctx,
		steps,
		step,
		func(ctx context.Context) error {
			rebuild, beginErr := env.Stores.SearchRebuilds(opsClockSource{}).BeginRebuild(
				ctx,
				plan.request,
			)
			if beginErr != nil {
				return searchReindexFailure(ctx, "begin index replacement", beginErr)
			}

			begun = rebuild

			return nil
		},
	)
	if err != nil {
		return details, err
	}

	return details, writeReindexResult(ctx, sink, begun, false, false)
}
