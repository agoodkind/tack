// The restore drill requires rows in restored auth tables and parses counts as
// numbers. Previously, comparing output text could accept an exec that exited
// successfully without printing a count. The drill now rejects counts it cannot
// read.

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

// drillAuthTables lists auth tables that restoration must repopulate.
// Authentication against the recovered host requires these tables regardless of
// other recovered data.
var drillAuthTables = []string{"users", "api_tokens", "org_members"}

// assertYBDrillRows fails unless every restored auth table contains rows.
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
			logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
			return wrapped
		}
		rowCounts = append(rowCounts, slog.Int64(table, count))
	}
	logger.InfoContext(ctx, "backup.restore_drill.yb.rows", rowCounts...)
	return nil
}

// readYBDrillRowCount counts rows in one restored table. It returns an error
// when it cannot read a count instead of treating that result as zero or
// success.
func readYBDrillRowCount(
	ctx context.Context,
	r *restoreDrillCtx,
	container, database, table string,
) (int64, error) {
	logger := telemetry.L(ctx)
	var buf bytes.Buffer
	exitCode, stderr, err := containerExecStreaming(ctx, r.Cli, container,
		ysqlshArgs(ybScratchHost(container), database, "select count(*) from "+table),
		[]string{"PGPASSWORD=" + r.YBPass}, &buf)
	if err != nil || exitCode != 0 {
		wrapped := fmt.Errorf("count %s: exit %d: %s: %w", table, exitCode, strings.TrimSpace(stderr), err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return 0, wrapped
	}
	count, err := parseYBDrillRowCount(buf.String())
	if err != nil {
		wrapped := fmt.Errorf("count restored table %s: %w", table, err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return 0, wrapped
	}
	return count, nil
}

// parseYBDrillRowCount reads one `ysqlsh -tAc "select count(*)"` result.
// It rejects empty output, text, and negative values. A successful exec can
// print empty output without answering the query; text does not supply a count,
// and count(*) cannot produce a negative value. Unreadable output cannot
// establish successful table recovery.
func parseYBDrillRowCount(out string) (int64, error) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return 0, errors.New("the count query printed no result")
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
