package ops

import (
	"context"
	"fmt"
	"log/slog"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clispec"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

type searchRolloutOutput struct {
	clispec.ResultMarker
	AuthorityID      string   `json:"authority_id"`
	Phase            string   `json:"phase"`
	ActiveVersion    string   `json:"active_version"`
	CandidateVersion string   `json:"candidate_version,omitempty"`
	PreviousVersion  string   `json:"previous_version,omitempty"`
	WriteVersions    []string `json:"write_versions"`
	Generation       int64    `json:"generation"`
	AffectedRecorded int64    `json:"affected_recorded"`
	AffectedNodes    []string `json:"affected_nodes"`
	DryRun           bool     `json:"dry_run"`
}

// writeSearchRolloutResult writes the rollout state and affected nodes of
// one authority as the command result.
func writeSearchRolloutResult(ctx context.Context, sink clispec.ResultSink, rollouts *fdbadapter.SearchAccessRolloutStore, rollout searchdomain.AccessRollout, dryRun bool) error {
	affected, err := rollouts.AffectedNodes(ctx, rollout.AuthorityID)
	if err != nil {
		return searchRolloutFailure(ctx, "read affected nodes of authority "+rollout.AuthorityID.String(), err)
	}
	nodes := make([]string, 0, len(affected.Nodes))
	for _, nodeID := range affected.Nodes {
		nodes = append(nodes, nodeID.String())
	}
	output := searchRolloutOutput{
		ResultMarker: clispec.ResultMarker{}, AuthorityID: rollout.AuthorityID.String(), Phase: string(rollout.Phase),
		ActiveVersion: rollout.ActiveVersion, CandidateVersion: rollout.CandidateVersion, PreviousVersion: rollout.PreviousVersion,
		WriteVersions: rollout.WriteVersions, Generation: rollout.Generation, AffectedRecorded: affected.Recorded,
		AffectedNodes: nodes, DryRun: dryRun,
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
