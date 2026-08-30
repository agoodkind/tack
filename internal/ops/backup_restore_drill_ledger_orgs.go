// backup_restore_drill_ledger_orgs.go reads what the restored ledger actually
// holds: which orgs carry rows, and how many rows each one carries. The counts
// are the yardstick the chain verdict measures its own coverage against.
// Without a count taken from the ledger itself, a bundle covering part of an
// org would be indistinguishable from one covering all of it, because a bundle
// missing a shard's newest or oldest rows still chains cleanly across the rows
// it does carry.

package ops

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/telemetry"
)

// restoredLedgerOrgsSQL reads the org list and each org's row count in one
// pass. Both come from the same statement, and therefore from the same role
// and the same row-level-security view, so the count an export is reconciled
// against is a count of the rows that export was able to see.
const restoredLedgerOrgsSQL = "SELECT org_id, count(*) FROM audit.events GROUP BY org_id"

// drillLedgerOrg names one org the restored ledger holds rows for, together
// with the number of rows it holds.
type drillLedgerOrg struct {
	ID       uuid.UUID
	RowCount int
}

// restoredLedgerOrgs lists the orgs holding rows in the restored ledger and
// how many rows each holds. It reads over the container exec channel the rest
// of this leg uses, so the org list and the row assertion come from the same
// place, and it reads as the drill's reader role so a role that cannot see the
// ledger fails here rather than looking like an empty ledger later.
func restoredLedgerOrgs(
	ctx context.Context,
	r *restoreDrillCtx,
	containerName, database, roleName string,
) ([]drillLedgerOrg, error) {
	logger := telemetry.L(ctx)
	var buf bytes.Buffer
	exitCode, stderr, err := containerExecStreaming(ctx, r.Cli, containerName,
		ysqlshRoleArgs(containerName, database, roleName, restoredLedgerOrgsSQL),
		[]string{"PGPASSWORD=" + r.YBPass}, &buf)
	if err != nil || exitCode != 0 {
		wrapped := fmt.Errorf("list the restored ledger's orgs: exit %d: %s: %w",
			exitCode, strings.TrimSpace(stderr), err)
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}

	var orgs []drillLedgerOrg
	totalRows := 0
	for line := range strings.SplitSeq(buf.String(), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		org, parseErr := parseRestoredLedgerOrg(ctx, trimmed)
		if parseErr != nil {
			return nil, parseErr
		}
		orgs = append(orgs, org)
		totalRows += org.RowCount
	}
	logger.InfoContext(ctx, "backup.restore_drill.ledger_chain.orgs",
		slog.Int("count", len(orgs)), slog.Int("rows", totalRows))
	return orgs, nil
}

// parseRestoredLedgerOrg reads one `org_id|count` line of unaligned ysqlsh
// output. A line this cannot read is an error rather than a skip: a skipped
// org is an org whose chain nothing checked.
func parseRestoredLedgerOrg(ctx context.Context, line string) (drillLedgerOrg, error) {
	empty := drillLedgerOrg{ID: uuid.Nil, RowCount: 0}
	orgField, countField, split := strings.Cut(line, "|")
	if !split {
		return empty, restoredLedgerOrgError(ctx, fmt.Errorf(
			"read %q from the restored ledger: expected an org id and a row count", line))
	}
	orgID, err := uuid.Parse(strings.TrimSpace(orgField))
	if err != nil {
		return empty, restoredLedgerOrgError(ctx,
			fmt.Errorf("parse org id %q from the restored ledger: %w", orgField, err))
	}
	rowCount, err := strconv.Atoi(strings.TrimSpace(countField))
	if err != nil {
		return empty, restoredLedgerOrgError(ctx, fmt.Errorf(
			"parse row count %q for org %s in the restored ledger: %w", countField, orgID, err))
	}
	return drillLedgerOrg{ID: orgID, RowCount: rowCount}, nil
}

// restoredLedgerOrgError logs a failed read of the org list under the leg's own
// event name and hands the error back unchanged.
func restoredLedgerOrgError(ctx context.Context, err error) error {
	telemetry.L(ctx).ErrorContext(ctx, "backup.restore_drill.ledger_chain.failed",
		slog.String("err", err.Error()))
	return err
}
