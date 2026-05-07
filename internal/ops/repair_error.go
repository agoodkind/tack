package ops

import (
	"context"
	"fmt"

	"goodkind.io/tack/internal/telemetry"
)

func loggedRepairError(ctx context.Context, message string, cause error) error {
	wrapped := fmt.Errorf("%s: %w", message, cause)
	telemetry.L(ctx).Error("repair.error", "err", wrapped.Error())
	return wrapped
}
