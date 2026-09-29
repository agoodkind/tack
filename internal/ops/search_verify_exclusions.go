package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/telemetry"
)

// searchVerifyOutput lists every node that search does not index.
type searchVerifyOutput struct {
	clispec.ResultMarker
	ExcludedNodes int                     `json:"excluded_nodes"`
	Exclusions    []searchExclusionOutput `json:"exclusions"`
}

// searchExclusionOutput is one excluded node. Class and Index are the work
// class and physical index of the failure that excluded the node.
type searchExclusionOutput struct {
	NodeID     string `json:"node_id"`
	OrgID      string `json:"org_id"`
	Class      string `json:"class"`
	Index      string `json:"index"`
	Reason     string `json:"reason"`
	ExcludedAt string `json:"excluded_at"`
}

// reportSearchExclusions writes the count and every exclusion that
// FoundationDB records. An excluded node does not fail verification.
func reportSearchExclusions(ctx context.Context, factory *cli.Factory, sink clispec.ResultSink) error {
	env, err := NewEnv(ctx, factory.Cfg)
	if err != nil {
		return searchVerifyFailure(ctx, "open search exclusion environment", err)
	}
	defer env.Close()
	output := searchVerifyOutput{ResultMarker: clispec.ResultMarker{}, ExcludedNodes: 0, Exclusions: []searchExclusionOutput{}}
	cursor := ""
	for {
		page, err := env.Stores.SearchExclusions(ctx, cursor)
		if err != nil {
			return searchVerifyFailure(ctx, "list search exclusions", err)
		}
		for _, exclusion := range page.Exclusions {
			output.Exclusions = append(output.Exclusions, searchExclusionOutput{
				NodeID: exclusion.NodeID.String(), OrgID: exclusion.OrgID.String(), Class: string(exclusion.Class),
				Index: exclusion.Index, Reason: exclusion.Reason, ExcludedAt: exclusion.ExcludedAt.UTC().Format(time.RFC3339),
			})
		}
		if page.Done {
			break
		}
		cursor = page.NextCursor
	}
	output.ExcludedNodes = len(output.Exclusions)
	telemetry.L(ctx).InfoContext(ctx, "search.verify.exclusions_listed", slog.Int("excluded_nodes", output.ExcludedNodes))
	if err := clispec.WriteJSONValue(ctx, sink, output); err != nil {
		return searchVerifyFailure(ctx, "write search exclusions", err)
	}
	return nil
}

func searchVerifyFailure(ctx context.Context, operation string, err error) error {
	wrapped := fmt.Errorf("%s: %w", operation, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.verify.exclusions_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
