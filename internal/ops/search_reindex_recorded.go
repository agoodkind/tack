package ops

import (
	"context"
	"encoding/json"
	"fmt"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// replacementInProgressCode is the error code for a reindex run that does not
// begin a replacement because another replacement is in progress.
const replacementInProgressCode = "replacement_in_progress"

// indexReplacementEvidence records the serving index and requested settings.
// It records the replacement ID, target index, state, and generation after
// BeginRebuild returns.
type indexReplacementEvidence struct {
	Mode          string `json:"mode"`
	SourceIndex   string `json:"source_index"`
	PrimaryShards int    `json:"primary_shards"`
	RoutingShards int    `json:"routing_shards"`
	Replicas      int    `json:"replicas"`
	Restored      bool   `json:"restored"`
	RebuildID     string `json:"rebuild_id,omitempty"`
	TargetIndex   string `json:"target_index,omitempty"`
	State         string `json:"state,omitempty"`
	Generation    int64  `json:"generation,omitempty"`
}

func (indexReplacementEvidence) isStepEvidence() {}

func replacementEvidence(rebuild searchdomain.Rebuild) indexReplacementEvidence {
	return indexReplacementEvidence{
		Mode: string(rebuild.Mode), SourceIndex: rebuild.SourceIndex, PrimaryShards: rebuild.PrimaryShards,
		RoutingShards: rebuild.RoutingShards, Replicas: rebuild.Replicas, Restored: rebuild.Restored,
		RebuildID: rebuild.ID.String(), TargetIndex: rebuild.TargetIndex, State: string(rebuild.State),
		Generation: rebuild.Generation,
	}
}

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
		refused := commandOutcome(audit.OutcomeRefused, replacementInProgressCode,
			fmt.Errorf("index replacement %s from %s is in progress", plan.current.ID, plan.current.SourceIndex))
		return refused, writeReindexResult(ctx, sink, plan.current, true, false)
	}
	planned := indexReplacementEvidence{
		Mode: string(plan.request.Mode), SourceIndex: plan.serving, PrimaryShards: plan.request.PrimaryShards,
		RoutingShards: plan.request.RoutingShards, Replicas: plan.request.Replicas, Restored: plan.request.Restored,
		RebuildID: "", TargetIndex: "", State: "", Generation: 0,
	}
	plannedJSON, err := encodeEvidence(ctx, planned)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	var begun searchdomain.Rebuild
	step := audit.ExternalStep(externalTarget("opensearch/index/"+plan.serving+"/replacement"), plannedJSON)
	details, err := recordedStep(ctx, steps, step,
		func(ctx context.Context) error {
			rebuild, beginErr := env.Stores.SearchRebuilds(opsClockSource{}).BeginRebuild(ctx, plan.request)
			if beginErr != nil {
				return searchReindexFailure(ctx, "begin index replacement", beginErr)
			}
			begun = rebuild
			return nil
		},
		func(ctx context.Context) (json.RawMessage, error) {
			return encodeEvidence(ctx, replacementEvidence(begun))
		})
	if err != nil {
		return details, err
	}
	return details, writeReindexResult(ctx, sink, begun, false, false)
}
