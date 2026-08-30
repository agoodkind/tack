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
// Every verdict here fails closed, for the same reason. A check that cannot
// see all of what it is checking has not checked it, so a bundle covering part
// of an org's rows fails rather than passing on the part it could read, and a
// full-range bundle with a hole in a chain fails rather than verifying the rows
// on either side of it.
//
// What a passing chain proves, and what it does not: every row the restored
// ledger holds was exported, re-hashed, and linked to the row before it, with
// no missing sequence number in between. It does not prove they are all the
// rows the live ledger held, because a restore that dropped the newest rows
// leaves an intact chain behind it. Comparing the restored ledger against the
// live one is separate work and deliberately out of scope here.

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
	orgs []drillLedgerOrg,
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

	// Each org's bundle is removed as soon as its verdict is read, so the disk
	// this needs is the largest single org rather than the sum of every org.
	// Keeping them all until the run ends would make the drill's footprint grow
	// with tenant count, which is the one number a multi-tenant product is
	// certain to grow.
	var failures []string
	totalRows := 0
	for _, org := range orgs {
		orgBundle := filepath.Join(bundleRoot, org.ID.String())
		rows, err := verifyRestoredOrgChain(ctx, org, orgBundle, export, verify)
		if removeErr := os.RemoveAll(orgBundle); removeErr != nil {
			logger.WarnContext(ctx, "backup.restore_drill.ledger_chain.bundle_not_removed",
				slog.String("path", orgBundle), slog.String("err", removeErr.Error()))
		}
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		totalRows += rows
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
		slog.Int("rows_verified", totalRows))
	return nil
}

// exportWholeOrgLedger writes one org's bundle and establishes that the bundle
// holds every row the restored ledger has for that org.
//
// The export asks for the org's whole range with no row limit, so a shortfall
// is a defect rather than a corpus that outgrew a cap. Reconciling the bundle
// against the ledger's own count is still the check that catches one, and it is
// the only check that can: a row dropped from the middle of a chain shows up as
// a sequence gap, but a row dropped from the newest or oldest end of a shard
// leaves the remaining rows contiguous and correctly linked, so the chain
// verdict alone would pass a bundle that quietly lost a shard's head. The ends
// are exactly where a streaming read's off-by-one lands.
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
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()))
		return wrapped
	}
	if rowCount == 0 {
		wrapped := fmt.Errorf(
			"org %s: the restored ledger lists this org but the export wrote no rows", org.ID)
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()))
		return wrapped
	}
	if rowCount != org.RowCount {
		wrapped := fmt.Errorf(
			"org %s: the export wrote %d of the %d rows the restored ledger holds, "+
				"so the chain of the rows it left out is unchecked",
			org.ID, rowCount, org.RowCount)
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}

// verifyRestoredOrgChain exports one org and reads its report, returning the
// rows verified so the caller can log what the leg actually covered.
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

	// A verifier that cannot run fails the drill. Logging the reason and
	// carrying on would leave a drill that reports success while checking
	// nothing, which is the shape of every silent backup failure.
	report, err := verify(dir)
	if err != nil {
		wrapped := fmt.Errorf("org %s: verify the exported bundle: %w", org.ID, err)
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()))
		return 0, wrapped
	}

	breaks := len(report.ChainBreaks)
	logger.InfoContext(ctx, "backup.restore_drill.ledger_chain.org",
		slog.String("org_id", org.ID.String()),
		slog.Int("rows_scanned", report.RowsScanned),
		slog.Int("hash_matches", report.HashMatches),
		slog.Int("chain_breaks", breaks),
		slog.Int("chain_gaps", report.ChainGapCount))

	// The break count is read here rather than delegated to the shared verdict
	// alone, so loosening that verdict cannot quietly let a broken chain pass
	// this drill. A break is a prev_hash that does not name the row before it,
	// or a row whose stored hash does not recompute, which is what a damaged
	// restore produces.
	if breaks > 0 {
		wrapped := fmt.Errorf("org %s: %d chain break(s), first: %s",
			org.ID, breaks, report.ChainBreaks[0])
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()))
		return 0, wrapped
	}
	// A gap is a sequence number the bundle does not carry, and whether that is
	// an artifact or a finding depends entirely on what the export asked for.
	// Over a filtered or time-bounded export it is an artifact: the window cut
	// the chain, the omitted rows are still in the ledger, and failing on it
	// would reject every honest bundle. That is why the shared verifier counts
	// a gap instead of calling it a break, and why this leg must not inherit
	// that tolerance. This export asks for the org's whole range, and the
	// bundle has already been reconciled against the ledger's own row count, so
	// nothing here left a row out on purpose. A gap is therefore a row the
	// restore did not bring back, sitting between two rows it did: the verifier
	// cannot compare prev_hash across it, so the chain is unverified exactly
	// where the ledger is incomplete. That is an incomplete restore, and an
	// incomplete restore is not a passing rehearsal.
	if report.ChainGapCount > 0 {
		wrapped := fmt.Errorf(
			"org %s: %d sequence gap(s) in a whole-ledger export, so rows are missing from "+
				"inside the chain and the links across them are unverified",
			org.ID, report.ChainGapCount)
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()))
		return 0, wrapped
	}
	// The rest of the bundle's verdict (the events digest, the manifest
	// signature, and any row that scanned without matching its hash) still
	// applies, and it is the same rule the operator verify command enforces.
	if verdict := report.Err(); verdict != nil {
		wrapped := fmt.Errorf("org %s: %w", org.ID, verdict)
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.org_failed",
			slog.String("err", wrapped.Error()))
		return 0, wrapped
	}
	return report.RowsScanned, nil
}
