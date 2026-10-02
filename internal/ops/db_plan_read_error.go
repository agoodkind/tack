package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// dbPlanReadError is a failed read of the operator outbox or of the plan rows
// while a plan command waits or decides. The command stops at the first one:
// it is not a refusal, it writes no refused row, and it sends no refusal or
// incomplete-summary mail. The function that returns it does not log it.
type dbPlanReadError struct {
	action string
	err    error
}

// Error returns the action that failed and its cause.
func (e *dbPlanReadError) Error() string {
	return e.action + ": " + e.err.Error()
}

// Unwrap returns the cause.
func (e *dbPlanReadError) Unwrap() error {
	return e.err
}

// isDBPlanReadError reports whether err is or wraps a *dbPlanReadError.
func isDBPlanReadError(err error) bool {
	var readErr *dbPlanReadError
	return errors.As(err, &readErr)
}

// dbPlanReadFailed logs err, a failed read under planID, and returns it
// wrapped with command. The command function that returns the read failure
// uses it on that return, and its log is the one log of the failure.
func dbPlanReadFailed(ctx context.Context, command string, planID uuid.UUID, err error) error {
	slog.ErrorContext(ctx, "db.plan.read_failed", slog.String("plan_id", planID.String()), slog.String("err", err.Error()))
	return fmt.Errorf("%s stopped at a failed read under plan %s: %w", command, planID, err)
}
