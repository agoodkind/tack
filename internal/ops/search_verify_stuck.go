package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// searchStuckWorkOutput is one work item that keeps retrying after its
// counted failures crossed the attempt limit. Kind is its work class.
type searchStuckWorkOutput struct {
	Kind      string `json:"kind"`
	ItemID    string `json:"item_id"`
	Attempts  int64  `json:"attempts"`
	LastError string `json:"last_error"`
}

// listStuckSearchWork adds every work item at or past the attempt limit to
// output.
func listStuckSearchWork(ctx context.Context, env *Env, output *searchVerifyOutput) error {
	cursor := ""
	for {
		page, err := env.Stores.SearchStuckWork(ctx, cursor)
		if err != nil {
			return searchVerifyFailure(ctx, "list search work at the attempt limit", err)
		}
		for _, stuck := range page.Work {
			output.StuckWork = append(output.StuckWork, stuckWorkOutput(stuck))
		}
		if page.Done {
			break
		}
		cursor = page.NextCursor
	}
	output.StuckWorkItems = len(output.StuckWork)
	return nil
}

func stuckWorkOutput(stuck searchdomain.StuckWork) searchStuckWorkOutput {
	return searchStuckWorkOutput{Kind: string(stuck.Class), ItemID: stuck.ItemID, Attempts: stuck.Attempts, LastError: stuck.LastError}
}

// joinVerifyFailures returns the stuck work failure and the engine check
// failure together, and nil when both are nil.
func joinVerifyFailures(ctx context.Context, stuck, physical error) error {
	joined := errors.Join(stuck, physical)
	if joined != nil {
		telemetry.L(ctx).ErrorContext(ctx, "search.verify.failed", slog.String("err", joined.Error()))
	}
	return joined
}

// stuckWorkError returns an error that lists each work item at or past the
// attempt limit with its kind, ID, attempt count, and last error. It returns
// nil when no item is listed.
func stuckWorkError(ctx context.Context, stuck []searchStuckWorkOutput) error {
	if len(stuck) == 0 {
		return nil
	}
	items := make([]string, 0, len(stuck))
	for _, item := range stuck {
		items = append(items, fmt.Sprintf("%s work %s failed %d times, last error: %s", item.Kind, item.ItemID, item.Attempts, item.LastError))
	}
	wrapped := fmt.Errorf("%d search work items crossed the attempt limit: %s", len(stuck), strings.Join(items, "; "))
	telemetry.L(ctx).ErrorContext(ctx, "search.verify.stuck_work", slog.String("err", wrapped.Error()), slog.Int("stuck_work_items", len(stuck)))
	return wrapped
}
