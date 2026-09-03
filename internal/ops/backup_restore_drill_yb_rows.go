// backup_restore_drill_yb_rows.go asserts the restored auth tables came back
// holding rows. The count is parsed rather than compared as text: an exec that
// exits zero having printed nothing is indistinguishable from a healthy table
// under a string comparison, so a count the drill cannot read fails the drill
// here instead of passing it.

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

// drillAuthTables are the auth tables a restore must bring back with rows.
// Without them nobody can authenticate against the recovered host, whatever
// else came back.
var drillAuthTables = []string{"users", "api_tokens", "org_members"}

// assertYBDrillRows fails unless every restored auth table holds rows.
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

// readYBDrillRowCount counts the rows one restored table holds. A count that
// cannot be read is an error, not a zero and not a pass.
func readYBDrillRowCount(
	ctx context.Context,
	r *restoreDrillCtx,
	container, database, table string,
) (int64, error) {
	logger := telemetry.L(ctx)
	var buf bytes.Buffer
	exitCode, stderr, err := containerExecStreaming(ctx, r.Cli, container,
		ysqlshArgs(container, database, "select count(*) from "+table),
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

// parseYBDrillRowCount reads the number out of one `ysqlsh -tAc "select
// count(*)"` result. Everything that is not a count fails: empty output, which
// is what an exec that exited zero having printed nothing leaves behind; text,
// which means the query did not answer; and a negative number, which no
// count(*) can be and so means this is not a count at all. A count nobody
// could read says nothing about whether the table came back, so it can never
// be treated as one that did.
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
