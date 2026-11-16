// Ledger organization row counts establish complete chain-verification
// coverage. An internally valid bundle can still omit a shard's newest or
// oldest rows; independent restored-ledger counts detect that incomplete export.

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

// restoredLedgerOrgsSQL reads organization IDs and counts in one statement,
// using the same reader role and row-level-security visibility as export.
const restoredLedgerOrgsSQL = "SELECT org_id, count(*) FROM audit.events GROUP BY org_id"

// drillLedgerOrg records an organization and its restored ledger row count.
type drillLedgerOrg struct {
	ID       uuid.UUID
	RowCount int
}

// restoredLedgerOrgs reads IDs and counts over scratch container exec as the
// drill reader role. Permission failures must not appear as an empty ledger.
func restoredLedgerOrgs(
	ctx context.Context,
	r *restoreDrillCtx,
	containerName string,
	database string,
	roleName string,
) ([]drillLedgerOrg, error) {
	logger := telemetry.L(ctx)
	var buf bytes.Buffer
	exitCode, stderr, err := containerExecStreaming(
		ctx,
		r.Cli,
		containerName,
		ysqlshRoleArgs(ybScratchHost(containerName), database, roleName, restoredLedgerOrgsSQL),
		[]string{"PGPASSWORD=" + r.YBPass},
		&buf,
	)
	if err != nil || exitCode != 0 {
		wrapped := fmt.Errorf(
			"list the restored ledger's orgs: exit %d: %s: %w",
			exitCode,
			strings.TrimSpace(stderr),
			err,
		)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.failed",
			slog.String("err", wrapped.Error()),
		)

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

	logger.InfoContext(
		ctx,
		"backup.restore_drill.ledger_chain.orgs",
		slog.Int("count", len(orgs)),
		slog.Int("rows", totalRows),
	)

	return orgs, nil
}

// parseRestoredLedgerOrg parses one org_id|count line. Invalid lines fail
// instead of silently excluding an organization's chain from verification.
func parseRestoredLedgerOrg(ctx context.Context, line string) (drillLedgerOrg, error) {
	empty := drillLedgerOrg{ID: uuid.Nil, RowCount: 0}
	orgField, countField, split := strings.Cut(line, "|")
	if !split {
		return empty, restoredLedgerOrgError(
			ctx,
			fmt.Errorf(
				"read %q from the restored ledger: expected an org id and a row count",
				line,
			),
		)
	}

	orgID, err := uuid.Parse(strings.TrimSpace(orgField))
	if err != nil {
		return empty, restoredLedgerOrgError(
			ctx,
			fmt.Errorf("parse org id %q from the restored ledger: %w", orgField, err),
		)
	}

	rowCount, err := strconv.Atoi(strings.TrimSpace(countField))
	if err != nil {
		return empty, restoredLedgerOrgError(
			ctx,
			fmt.Errorf(
				"parse row count %q for org %s in the restored ledger: %w",
				countField,
				orgID,
				err,
			),
		)
	}

	return drillLedgerOrg{ID: orgID, RowCount: rowCount}, nil
}

// restoredLedgerOrgError logs the organization read failure and returns the
// original error.
func restoredLedgerOrgError(ctx context.Context, err error) error {
	telemetry.L(ctx).ErrorContext(
		ctx,
		"backup.restore_drill.ledger_chain.failed",
		slog.String("err", err.Error()),
	)

	return err
}
