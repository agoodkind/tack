package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
)

// AwaitDatagenSessionHeaderDelete reads SearchSessionHeaderDeletes until
// the count exceeds zero. It returns an error if the count stays at zero
// through datagenSessionRowsWait.
func AwaitDatagenSessionHeaderDelete(
	ctx context.Context,
	f *cli.Factory,
	orgID uuid.UUID,
	sessionID uuid.UUID,
) error {
	if err := requireDatagenLedgerReader(ctx, f); err != nil {
		return err
	}
	reader, err := audit.NewReader(ctx, f.Cfg.AuditReaderDSN)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"qa.datagen.session_header_reader_failed",
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("open the ledger reader: %w", err)
	}
	defer reader.Close()
	deadline := clock.Now().Add(datagenSessionRowsWait)
	for {
		deletes, err := reader.SearchSessionHeaderDeletes(ctx, orgID, sessionID)
		if err != nil {
			return fmt.Errorf("read the ledger of organization %s: %w", orgID, err)
		}
		if deletes > 0 {
			return nil
		}
		if clock.Now().After(deadline) {
			err := fmt.Errorf(
				"the ledger of organization %s did not store a completed delete of the header of search session %s within %s",
				orgID,
				sessionID,
				datagenSessionRowsWait,
			)
			slog.ErrorContext(
				ctx,
				"qa.datagen.session_header_delete_missing",
				slog.String("err", err.Error()),
			)
			return err
		}
		timer := time.NewTimer(datagenSessionRowsPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			wrapped := fmt.Errorf(
				"wait for the header delete of search session %s: %w",
				sessionID,
				ctx.Err(),
			)
			slog.ErrorContext(
				ctx,
				"qa.datagen.session_header_wait_failed",
				slog.String("err", wrapped.Error()),
			)
			return wrapped
		case <-timer.C:
		}
	}
}
