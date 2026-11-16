// Restored-ledger verification checks exported row hashes, chain links, and
// sequence continuity through structured verifier reports, not process status.
// It requires complete export coverage against independent restored row counts.
// Success establishes integrity of restored rows, not equivalence with every
// row in the live ledger: losing newest rows can leave a valid shorter chain.

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

var errRestoredLedgerEmpty = errors.New("restored audit ledger does not contain rows")

// drillLedgerExportFunc writes an organization's signed bundle and reports
// exported rows. Tests can provide bundles without a restored database.
type drillLedgerExportFunc func(ctx context.Context, orgID uuid.UUID, dir string) (int, error)

// drillLedgerVerifyFunc returns the offline verifier's structured report using
// the run's temporary public key, matching operator audit verification.
type drillLedgerVerifyFunc func(dir string) (*audit.VerifyReport, error)

// verifyRestoredLedgerChains attempts every organization despite verification
// failures and aggregates errors. Empty ledgers fail because operator and
// export operations must have recorded events; even one intact row can pass.
func verifyRestoredLedgerChains(
	ctx context.Context,
	orgs []drillLedgerOrg,
	bundleRoot string,
	export drillLedgerExportFunc,
	verify drillLedgerVerifyFunc,
) error {
	logger := telemetry.L(ctx)
	if len(orgs) == 0 {
		err := fmt.Errorf(
			"%w; hash-chain verification requires ledger data",
			errRestoredLedgerEmpty,
		)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.failed",
			slog.String("err", err.Error()),
		)

		return err
	}

	// Reclaim each bundle before exporting another organization to bound disk
	// use by the largest organization, not total tenants. Cleanup failure must
	// stop and fail the drill instead of exhausting shared backup storage.
	var failures []string
	totalRows := 0

	for _, org := range orgs {
		orgBundle := filepath.Join(bundleRoot, org.ID.String())
		rows, err := verifyRestoredOrgChain(ctx, org, orgBundle, export, verify)
		if removeErr := os.RemoveAll(orgBundle); removeErr != nil {
			wrapped := fmt.Errorf(
				"org %s: could not free the exported bundle at %s; the drill stopped "+
					"before exporting another org onto unreclaimed storage: %w",
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

// exportWholeOrgLedger reconciles unlimited full-range export against restored
// row counts. Missing newest or oldest shard rows can leave valid chain links;
// only independent coverage counts detect those export omissions.
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
		wrapped := restoredLedgerExportEmptyError{OrgID: org.ID}
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	if rowCount != org.RowCount {
		wrapped := restoredLedgerExportCountMismatchError{
			OrgID:    org.ID,
			Exported: rowCount,
			Restored: org.RowCount,
		}
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	return nil
}

// verifyRestoredOrgChain requires complete export and successful verification,
// returning verified rows for the final coverage log.
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

	// An unavailable verifier must fail rather than skip integrity checks.
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

	// Explicit break checks reject incorrect prev_hash links or row hashes
	// independently of any future changes to the shared report verdict.
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

	// Filtered exports can legitimately omit sequence numbers. This full-range,
	// count-reconciled export cannot. A gap means missing restored rows and
	// prevents verification of the links across that missing sequence.
	if report.ChainGapCount > 0 {
		wrapped := restoredLedgerSequenceGapError{OrgID: org.ID, Gaps: report.ChainGapCount}
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()),
		)

		return 0, wrapped
	}

	// Digest, signature, and remaining row-hash failures use the shared
	// operator verification verdict as well.
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
