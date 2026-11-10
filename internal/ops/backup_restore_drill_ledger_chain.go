// The restored audit ledger must retain an intact per-(org, shard) hash
// chain. Row counts alone cannot detect a prev_hash that does not identify
// the preceding row. Verification recomputes row hashes and checks the links.
//
// The drill reads the verifier's structured report instead of relying on its
// process exit code. A verifier can report breaks while exiting successfully.
// The report must make that drill fail.
//
// The drill rejects partial organization exports and gaps in a whole-range
// bundle. It cannot verify links across missing rows by checking only the
// rows on either side.
//
// A passing check establishes that the export includes every restored row,
// each hash matches, and each link follows the previous row without a missing
// sequence number. It does not establish that restore preserved every live
// row: dropping the newest rows can leave an intact chain. Comparison with
// the live ledger remains separate work outside this check.

package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

// drillLedgerExportFunc writes one organization's signed bundle to dir and
// returns its row count. Tests supply bundles directly through this parameter
// to exercise verdicts without a database.
type drillLedgerExportFunc func(ctx context.Context, orgID uuid.UUID, dir string) (int, error)

// drillLedgerVerifyFunc verifies one bundle directory and returns a
// structured report. Production uses the offline verifier for audit verify
// with the run's temporary public key.
type drillLedgerVerifyFunc func(dir string) (*audit.VerifyReport, error)

// verifyRestoredLedgerChains exports and verifies a bundle for each
// organization. It continues after an organization's verification failure to
// report every such failure.
//
// An empty organization list fails. The artifact restores the live auth and
// audit database, and the binding audit contract requires every operator
// command, including the export command, to record its operation. Under that
// contract, an exportable ledger contains rows. An empty restore cannot
// establish chain preservation and must not count as a skipped check that
// passes. A sparse ledger with one row can pass. The auth-row assertion also
// rejects an empty restore.
func verifyRestoredLedgerChains(
	ctx context.Context,
	orgs []drillLedgerOrg,
	bundleRoot string,
	export drillLedgerExportFunc,
	verify drillLedgerVerifyFunc,
) error {
	logger := telemetry.L(ctx)
	if len(orgs) == 0 {
		err := errors.New("restored audit ledger holds no rows, so its hash chain proves nothing")
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.failed",
			slog.String("err", err.Error()),
		)

		return err
	}

	// The loop removes each organization's bundle immediately after reading its
	// verdict. Disk use depends on the largest bundle instead of total tenant
	// count, which can grow in a multi-tenant product.
	//
	// A cleanup failure stops and fails the drill. Continuing would export more
	// bundles without reclaiming space on the volume shared with backups. That
	// could exhaust the volume, and failed cleanup does not establish a working
	// rehearsal.
	var failures []string
	totalRows := 0

	for _, org := range orgs {
		orgBundle := filepath.Join(bundleRoot, org.ID.String())
		rows, err := verifyRestoredOrgChain(ctx, org, orgBundle, export, verify)
		if removeErr := os.RemoveAll(orgBundle); removeErr != nil {
			wrapped := fmt.Errorf(
				"org %s: could not free the exported bundle at %s, so the drill stopped "+
					"rather than export another org onto a volume it cannot reclaim: %w",
				org.ID,
				orgBundle,
				removeErr,
			)
			logger.ErrorContext(
				ctx,
				"backup.restore_drill.ledger_chain.failed",
				slog.String("err", wrapped.Error()),
			)

			return wrapped
		}

		if err != nil {
			failures = append(failures, err.Error())
			continue
		}

		totalRows += rows
	}

	if len(failures) > 0 {
		err := fmt.Errorf(
			"restored audit ledger failed chain verification: %s",
			strings.Join(failures, "; "),
		)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.failed",
			slog.String("err", err.Error()),
		)

		return err
	}

	logger.InfoContext(
		ctx,
		"backup.restore_drill.ledger_chain.ok",
		slog.Int("orgs", len(orgs)),
		slog.Int("rows_verified", totalRows),
	)

	return nil
}

// exportWholeOrgLedger exports one organization and checks that the bundle
// contains every row that the restored database stores for it.
//
// The export requests the whole range without a row limit. A short export
// therefore indicates a defect rather than a size cap. Count reconciliation
// detects omitted oldest or newest rows in a shard: the remaining rows can
// remain contiguous and correctly linked. Chain verification alone cannot
// detect that loss, including an off-by-one error at a streaming boundary.
// A missing row in the middle instead produces a sequence gap.
func exportWholeOrgLedger(
	ctx context.Context,
	org drillLedgerOrg,
	dir string,
	export drillLedgerExportFunc,
) error {
	logger := telemetry.L(ctx)
	rowCount, err := export(ctx, org.ID, dir)
	if err != nil {
		wrapped := fmt.Errorf("org %s: export from the restored ledger: %w", org.ID, err)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	if rowCount == 0 {
		wrapped := fmt.Errorf(
			"org %s: the restored ledger lists this org but the export wrote no rows",
			org.ID,
		)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	if rowCount != org.RowCount {
		wrapped := fmt.Errorf(
			"org %s: the export wrote %d of the %d rows the restored ledger holds, "+
				"so the chain of the rows it left out is unchecked",
			org.ID,
			rowCount,
			org.RowCount,
		)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	return nil
}

// verifyRestoredOrgChain exports one organization, checks its report, and
// returns the verified row count for logging actual coverage.
func verifyRestoredOrgChain(
	ctx context.Context,
	org drillLedgerOrg,
	dir string,
	export drillLedgerExportFunc,
	verify drillLedgerVerifyFunc,
) (int, error) {
	logger := telemetry.L(ctx)
	if err := exportWholeOrgLedger(ctx, org, dir, export); err != nil {
		return 0, err
	}

	// A verifier failure must fail the drill. Logging it and continuing could
	// report success without verifying the backup.
	report, err := verify(dir)
	if err != nil {
		wrapped := fmt.Errorf("org %s: verify the exported bundle: %w", org.ID, err)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()),
		)

		return 0, wrapped
	}

	breaks := len(report.ChainBreaks)
	logger.InfoContext(
		ctx,
		"backup.restore_drill.ledger_chain.org",
		slog.String("org_id", org.ID.String()),
		slog.Int("rows_scanned", report.RowsScanned),
		slog.Int("hash_matches", report.HashMatches),
		slog.Int("chain_breaks", breaks),
		slog.Int("chain_gaps", report.ChainGapCount),
	)

	// The drill checks ChainBreaks directly as well as the shared verdict.
	// Weakening the shared verdict must not permit a broken restored chain to
	// pass. A break identifies an incorrect prev_hash or a stored hash that does
	// not match the recomputed hash.
	if breaks > 0 {
		wrapped := fmt.Errorf(
			"org %s: %d chain break(s), first: %s",
			org.ID,
			breaks,
			report.ChainBreaks[0],
		)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()),
		)

		return 0, wrapped
	}

	// A gap indicates a missing sequence number. A filtered or time-limited
	// export can intentionally omit rows that remain in the ledger. The shared
	// verifier counts those gaps separately from breaks to accept valid partial
	// bundles.
	//
	// This drill requests the whole organization range and has reconciled the
	// export against the restored ledger's row count. It does not intentionally
	// omit rows. A gap therefore indicates missing restored rows between retained
	// rows. The verifier cannot compare prev_hash across that gap. The incomplete
	// restore must fail the rehearsal instead of using the shared tolerance.
	if report.ChainGapCount > 0 {
		wrapped := fmt.Errorf(
			"org %s: %d sequence gap(s) in a whole-ledger export, so rows are missing from "+
				"inside the chain and the links across them are unverified",
			org.ID,
			report.ChainGapCount,
		)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()),
		)

		return 0, wrapped
	}

	// The shared verdict still checks the events digest, manifest signature, and
	// rows that scanned without a matching hash. The operator verify command
	// enforces that verdict too.
	if verdict := report.Err(); verdict != nil {
		wrapped := fmt.Errorf("org %s: %w", org.ID, verdict)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()),
		)

		return 0, wrapped
	}

	return report.RowsScanned, nil
}
