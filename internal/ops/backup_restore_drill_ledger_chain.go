// backup_restore_drill_ledger_chain.go decides whether a restored audit ledger
// still carries an intact hash chain. Counting rows cannot see a broken chain:
// a restore can land rows that look complete while the per-(org, shard)
// prev_hash links no longer name the row before them, and a restore is exactly
// where that happens silently. Re-hashing the rows is what sees it.
//
// The verdict is read from the verifier's structured report, never from a
// process exit code. A verifier that finds breaks and still exits zero would
// otherwise pass the drill, which is the failure this leg exists to remove.
//
// What a passing chain proves, and what it does not: the rows that survived
// are internally consistent with each other. It does not prove they are all
// the rows the live ledger held, because a restore that dropped a tail still
// verifies. Comparing the restored ledger against the live one is separate
// work and deliberately out of scope here.

package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

// drillLedgerExportFunc writes one org's signed bundle into dir and reports
// how many rows it wrote. The leg takes it as a parameter rather than calling
// the exporter directly so a test can drive the verdict with bundles it wrote
// itself, without standing up a database.
type drillLedgerExportFunc func(ctx context.Context, orgID uuid.UUID, dir string) (int, error)

// drillLedgerVerifyFunc verifies one exported bundle directory and returns the
// structured report. Production binds it to the same offline verifier the
// operator `audit verify` command runs, over the run's throwaway public key.
type drillLedgerVerifyFunc func(dir string) (*audit.VerifyReport, error)

// verifyRestoredLedgerChains exports one bundle per org and folds every report
// into one verdict. Every org is attempted even after one fails, so the drill
// reports the whole picture rather than the first break it meets.
//
// An empty org list fails rather than passing quietly. The artifact under
// drill is an export of the live auth-and-audit database; every operator
// command records through the ledger, and the export command that produced the
// artifact records itself, so a deployment whose ledger can be exported always
// holds rows. Zero rows therefore means the restore did not bring the ledger
// back, and a chain check with nothing to check is indistinguishable from one
// that was skipped. A sparse ledger is not the same thing: one row passes. The
// auth-row assertion already fails on zero for the same reason.
func verifyRestoredLedgerChains(
	ctx context.Context,
	orgs []uuid.UUID,
	bundleRoot string,
	export drillLedgerExportFunc,
	verify drillLedgerVerifyFunc,
) error {
	logger := telemetry.L(ctx)
	if len(orgs) == 0 {
		err := errors.New("restored audit ledger holds no rows, so its hash chain proves nothing")
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.failed", slog.String("err", err.Error()))
		return err
	}

	var failures []string
	totalRows, totalGaps := 0, 0
	for _, orgID := range orgs {
		rows, gaps, err := verifyRestoredOrgChain(
			ctx, orgID, filepath.Join(bundleRoot, orgID.String()), export, verify)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		totalRows += rows
		totalGaps += gaps
	}

	if len(failures) > 0 {
		err := fmt.Errorf("restored audit ledger failed chain verification: %s",
			strings.Join(failures, "; "))
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.failed",
			slog.String("err", err.Error()))
		return err
	}
	logger.InfoContext(ctx, "backup.restore_drill.ledger_chain.ok",
		slog.Int("orgs", len(orgs)),
		slog.Int("rows_verified", totalRows),
		slog.Int("chain_gaps", totalGaps))
	return nil
}

// verifyRestoredOrgChain exports one org and reads its report, returning the
// rows verified and the sequence gaps counted so the caller can log what the
// leg actually covered.
func verifyRestoredOrgChain(
	ctx context.Context,
	orgID uuid.UUID,
	dir string,
	export drillLedgerExportFunc,
	verify drillLedgerVerifyFunc,
) (int, int, error) {
	logger := telemetry.L(ctx)
	rowCount, err := export(ctx, orgID, dir)
	if err != nil {
		wrapped := fmt.Errorf("org %s: export from the restored ledger: %w", orgID, err)
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()))
		return 0, 0, wrapped
	}
	if rowCount == 0 {
		wrapped := fmt.Errorf(
			"org %s: the restored ledger lists this org but the export wrote no rows", orgID)
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()))
		return 0, 0, wrapped
	}

	// A verifier that cannot run fails the drill. Logging the reason and
	// carrying on would leave a drill that reports success while checking
	// nothing, which is the shape of every silent backup failure.
	report, err := verify(dir)
	if err != nil {
		wrapped := fmt.Errorf("org %s: verify the exported bundle: %w", orgID, err)
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()))
		return 0, 0, wrapped
	}

	breaks := len(report.ChainBreaks)
	logger.InfoContext(ctx, "backup.restore_drill.ledger_chain.org",
		slog.String("org_id", orgID.String()),
		slog.Int("rows_scanned", report.RowsScanned),
		slog.Int("hash_matches", report.HashMatches),
		slog.Int("chain_breaks", breaks),
		slog.Int("chain_gaps", report.ChainGapCount))

	// The break count is read here rather than delegated to the shared verdict
	// alone, so loosening that verdict cannot quietly let a broken chain pass
	// this drill. A break is a prev_hash that does not name the row before it,
	// or a row whose stored hash does not recompute, which is what a damaged
	// restore produces. A gap is a missing sequence number, which any bounded
	// export produces by leaving rows out, and which says nothing about
	// tampering; this leg therefore never fails on gaps.
	if breaks > 0 {
		wrapped := fmt.Errorf("org %s: %d chain break(s), first: %s",
			orgID, breaks, report.ChainBreaks[0])
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()))
		return 0, 0, wrapped
	}
	// The rest of the bundle's verdict (the events digest, the manifest
	// signature, and any row that scanned without matching its hash) still
	// applies, and it is the same rule the operator verify command enforces.
	if verdict := report.Err(); verdict != nil {
		wrapped := fmt.Errorf("org %s: %w", orgID, verdict)
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()))
		return 0, 0, wrapped
	}
	return report.RowsScanned, report.ChainGapCount, nil
}
