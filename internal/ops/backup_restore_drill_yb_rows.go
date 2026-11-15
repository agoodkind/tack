// Restored authentication tables must contain rows. Numeric parsing rejects
// empty or malformed output that a string comparison against "0" could accept.

package ops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

var errCountQueryResultMissing = errors.New("the count query did not print a result")

// drillAuthTables lists restored tables required for authentication.
var drillAuthTables = []string{"users", "api_tokens", "org_members"}

// assertYBDrillRows requires a positive row count in each authentication table.
func assertYBDrillRows(ctx context.Context, r *restoreDrillCtx, container, database string) error {
	logger := telemetry.L(ctx)

	rowCounts := make([]any, 0, len(drillAuthTables))
	for _, table := range drillAuthTables {
		count, err := readYBDrillRowCount(ctx, r, container, database, table)
		if err != nil {
			return err
		}

		if count == 0 {
			wrapped := fmt.Errorf("restored table %s has 0 rows", table)
			logger.ErrorContext(
				ctx,
				"backup.restore_drill.yb.failed",
				slog.String("err", wrapped.Error()),
			)

			return wrapped
		}

		rowCounts = append(rowCounts, slog.Int64(table, count))
	}

	logger.InfoContext(ctx, "backup.restore_drill.yb.rows", rowCounts...)

	return nil
}

// readYBDrillRowCount counts one restored table and rejects unreadable output
// rather than assuming an empty or populated table.
func readYBDrillRowCount(
	ctx context.Context,
	r *restoreDrillCtx,
	container string,
	database string,
	table string,
) (int64, error) {
	logger := telemetry.L(ctx)
	var buf bytes.Buffer
	exitCode, stderr, err := containerExecStreaming(
		ctx,
		r.Cli,
		container,
		ysqlshArgs(ybScratchHost(container), database, "select count(*) from "+table),
		[]string{"PGPASSWORD=" + r.YBPass},
		&buf,
	)
	if err != nil || exitCode != 0 {
		wrapped := fmt.Errorf(
			"count %s: exit %d: %s: %w",
			table,
			exitCode,
			strings.TrimSpace(stderr),
			err,
		)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.yb.failed",
			slog.String("err", wrapped.Error()),
		)

		return 0, wrapped
	}

	count, err := parseYBDrillRowCount(buf.String())
	if err != nil {
		wrapped := fmt.Errorf("count restored table %s: %w", table, err)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.yb.failed",
			slog.String("err", wrapped.Error()),
		)

		return 0, wrapped
	}

	return count, nil
}

// parseYBDrillRowCount accepts only a nonnegative integer from ysqlsh
// count(*). Empty output, text, and negative numbers cannot establish a
// table's row count.
func parseYBDrillRowCount(out string) (int64, error) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return 0, errCountQueryResultMissing
	}

	count, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("read %q as a row count: it is not a whole number", trimmed)
	}

	if count < 0 {
		return 0, fmt.Errorf("read %q as a row count: a count cannot be negative", trimmed)
	}

	return count, nil
}
