package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/telemetry"
)

// searchVerifyOutput lists nodes that search does not index, properties that
// indexed text omits, and work items that keep retrying after exceeding the
// attempt limit.
type searchVerifyOutput struct {
	clispec.ResultMarker
	ExcludedNodes    int                     `json:"excluded_nodes"`
	Exclusions       []searchExclusionOutput `json:"exclusions"`
	PropertyWarnings int                     `json:"property_warnings"`
	Warnings         []searchWarningOutput   `json:"warnings"`
	StuckWorkItems   int                     `json:"stuck_work_items"`
	StuckWork        []searchStuckWorkOutput `json:"stuck_work"`
}

// searchWarningOutput reports one property that a node's indexed text omits.
// PropertyDefID is empty for an undeclared property.
type searchWarningOutput struct {
	NodeID            string `json:"node_id"`
	OrgID             string `json:"org_id"`
	PropertyName      string `json:"property_name"`
	PropertyDefID     string `json:"property_def_id"`
	Revision          string `json:"revision"`
	ProjectionVersion string `json:"projection_version"`
	Reason            string `json:"reason"`
	Message           string `json:"message"`
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

// reportSearchWorkState writes every exclusion and every work item at or past
// the attempt limit that FoundationDB records. An excluded node does not fail
// verification. The returned stuck error lists each work item at or past the
// limit, and err reports a failed read or write.
func reportSearchWorkState(ctx context.Context, factory *cli.Factory, sink clispec.ResultSink) (stuck, err error) {
	env, err := NewEnv(ctx, factory.Cfg)
	if err != nil {
		return nil, searchVerifyFailure(ctx, "open search exclusion environment", err)
	}
	defer env.Close()
	output := searchVerifyOutput{
		ResultMarker: clispec.ResultMarker{}, ExcludedNodes: 0, Exclusions: []searchExclusionOutput{},
		PropertyWarnings: 0, Warnings: []searchWarningOutput{},
		StuckWorkItems: 0, StuckWork: []searchStuckWorkOutput{},
	}
	if err := listSearchExclusions(ctx, env, &output); err != nil {
		return nil, err
	}
	if err := listSearchWarnings(ctx, env, &output); err != nil {
		return nil, err
	}
	if err := listStuckSearchWork(ctx, env, &output); err != nil {
		return nil, err
	}
	telemetry.L(ctx).InfoContext(ctx, "search.verify.work_state_listed", slog.Int("excluded_nodes", output.ExcludedNodes),
		slog.Int("property_warnings", output.PropertyWarnings), slog.Int("stuck_work_items", output.StuckWorkItems))
	if err := clispec.WriteJSONValue(ctx, sink, output); err != nil {
		return nil, searchVerifyFailure(ctx, "write search work state", err)
	}
	return stuckWorkError(ctx, output.StuckWork), nil
}

// listSearchExclusions adds every recorded exclusion to output.
func listSearchExclusions(ctx context.Context, env *Env, output *searchVerifyOutput) error {
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
	return nil
}

// listSearchWarnings adds every verified property warning to output.
// Warnings do not fail verification.
func listSearchWarnings(ctx context.Context, env *Env, output *searchVerifyOutput) error {
	cursor := ""
	for {
		page, err := env.Stores.SearchWarnings(ctx, cursor)
		if err != nil {
			return searchVerifyFailure(ctx, "list search property warnings", err)
		}
		for _, warning := range page.Warnings {
			definitionID := ""
			if warning.PropertyDefID != uuid.Nil {
				definitionID = warning.PropertyDefID.String()
			}
			output.Warnings = append(output.Warnings, searchWarningOutput{
				NodeID: warning.NodeID.String(), OrgID: warning.OrgID.String(), PropertyName: warning.PropertyName,
				PropertyDefID: definitionID, Revision: warning.Revision, ProjectionVersion: warning.ProjectionVersion,
				Reason: string(warning.Reason), Message: warning.Message,
			})
		}
		if page.Done {
			break
		}
		cursor = page.NextCursor
	}
	output.PropertyWarnings = len(output.Warnings)
	return nil
}

func searchVerifyFailure(ctx context.Context, operation string, err error) error {
	wrapped := fmt.Errorf("%s: %w", operation, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.verify.exclusions_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
