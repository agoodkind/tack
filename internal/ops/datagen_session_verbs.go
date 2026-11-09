package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
)

const (
	// datagenSessionRowsWait limits how long ops qa datagen search waits for
	// the relay and the audit consumer to store the session rows of the run.
	datagenSessionRowsWait = 2 * time.Minute
	// datagenSessionRowsPoll separates two reads of the session verb report.
	datagenSessionRowsPoll = 2 * time.Second
)

// awaitDatagenSearchSessionVerbs reads the session verb report of orgID until
// the ledger stores at least one row of every verb. It returns an error that
// lists the verbs without rows after datagenSessionRowsWait.
func awaitDatagenSearchSessionVerbs(ctx context.Context, f *cli.Factory, orgID uuid.UUID) (
	datagenLedgerVerbsReport,
	error,
) {
	deadline := clock.Now().Add(datagenSessionRowsWait)
	for {
		report, err := readDatagenSearchSessionVerbs(ctx, f, orgID)
		if err != nil {
			return report, err
		}
		missing := datagenVerbsWithoutRows(report)
		if len(missing) == 0 {
			return report, nil
		}
		if clock.Now().After(deadline) {
			err := fmt.Errorf(
				"the ledger of organization %s stored no rows of %s within %s",
				orgID,
				strings.Join(missing, ", "),
				datagenSessionRowsWait,
			)
			slog.ErrorContext(
				ctx,
				"qa.datagen.session_rows_missing",
				slog.String("err", err.Error()),
			)
			return report, err
		}
		timer := time.NewTimer(datagenSessionRowsPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			wrapped := fmt.Errorf(
				"wait for the session rows of organization %s: %w",
				orgID,
				ctx.Err(),
			)
			slog.ErrorContext(
				ctx,
				"qa.datagen.session_rows_wait_failed",
				slog.String("err", wrapped.Error()),
			)
			return report, wrapped
		case <-timer.C:
		}
	}
}

func datagenVerbsWithoutRows(report datagenLedgerVerbsReport) []string {
	missing := []string{}
	for _, verb := range report.Verbs {
		if verb.Rows == 0 {
			missing = append(missing, verb.Verb)
		}
	}
	return missing
}
