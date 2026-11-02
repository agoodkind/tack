package foundationdb

import (
	"context"
	"errors"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"

	"goodkind.io/tack/internal/audit"
)

const retireAttemptClearOperation = "clear search session retirement attempt"

// clearRetireAttempt reads and clears the attempt record in one transaction
// after the cleanup step commits its change events in a separate transaction.
// If the first transaction attempt finds the record absent, it returns
// errRetireAttemptFenced. If a retry finds the record absent, it logs at Info
// and returns an error that wraps errRetireClearAbsent and states the session
// ID. If the clear fails, it returns the error without audit.ErrEventNotRecorded
// because the step already committed every required event.
// recoverRetireAttempts later saves an incomplete event for the attempt.
func (s *SearchRebuildStore) clearRetireAttempt(ctx context.Context, attempt retireAttempt) error {
	clearContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), audit.ExternalResultDeadline)
	defer cancel()
	sessionID := attempt.sessionID.String()
	attemptCount := 0
	absentOnRetry := false
	err := transactSession(clearContext, s.db, s.work.clock, retireAttemptClearOperation, attempt.sessionID, func(tr fdb.Transaction) error {
		attemptCount++
		absentOnRetry = false
		fenceErr := requireRetireFence(tr, attempt.key)
		if errors.Is(fenceErr, errRetireAttemptFenced) && attemptCount > 1 {
			absentOnRetry = true
			return nil
		}
		if fenceErr != nil {
			return fenceErr
		}
		tr.Clear(attempt.key)
		slog.DebugContext(ctx, "search.session.retire_clear_ready", slog.String("session_id", sessionID))
		return nil
	})
	if errors.Is(err, errRetireAttemptFenced) {
		return err
	}
	if err != nil {
		slog.ErrorContext(ctx, "search.session.retire_clear_failed", slog.String("err", err.Error()), slog.String("session_id", sessionID))
		return err
	}
	if absentOnRetry {
		slog.InfoContext(ctx, "search.session.retire_clear_absent", slog.String("session_id", sessionID))
		return sessionStepError{operation: retireAttemptClearOperation + " for session " + sessionID, err: errRetireClearAbsent}
	}
	return nil
}
