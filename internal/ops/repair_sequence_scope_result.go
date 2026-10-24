package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/domain"
)

// scopeRepairResult logs repair.scope_ids.completed with the repaired, skipped,
// and refused counts. scopeRepairResult returns the repaired and skipped counts
// with a nil error when the refused count is zero. When the refused count exceeds
// zero, scopeRepairResult also logs repair.scope_ids.refused and returns the
// counts with an error that wraps domain.ErrConflict and states the refused count.
func scopeRepairResult(ctx context.Context, env *Env, repaired, skipped, refused int) (int, int, error) {
	env.Log.InfoContext(ctx, "repair.scope_ids.completed",
		slog.Int("repaired", repaired), slog.Int("skipped", skipped), slog.Int("refused", refused))
	if refused == 0 {
		return repaired, skipped, nil
	}
	err := fmt.Errorf("repair sequence scope IDs: the repair refused %d writes; the operator must run the repair again to read the current records: %w", refused, domain.ErrConflict)
	env.Log.ErrorContext(ctx, "repair.scope_ids.refused", slog.Int("refused", refused), slog.String("err", err.Error()))
	return repaired, skipped, err
}
