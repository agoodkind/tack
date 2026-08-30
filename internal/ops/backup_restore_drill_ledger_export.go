// backup_restore_drill_ledger_export.go is the ledger half of the yugabyte
// drill leg: it creates the run-scoped reader role in the restored database,
// finds the orgs holding rows, and exports one signed bundle per org for the
// chain verdict to read.

package ops

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// drillLedgerSigningKeyID labels the throwaway signing key in the bundle
	// manifest. The drill signs with a key it generates per run and discards,
	// so the production signing key never enters the drill and the signature
	// check proves only that the bundle is the one the export just wrote. The
	// row-hash and chain-link checks, which are what this leg exists for,
	// cover the ledger's own integrity and involve no key at all.
	drillLedgerSigningKeyID = "ed25519:restore-drill"

	// drillLedgerExportRowLimit bounds one org's bundle. The export holds
	// every matching row before it writes, and the drill runs inside the
	// tack-ops sidecar under a 512 MiB memory limit, so an unbounded export of
	// a production-sized org would be killed instead of returning a verdict
	// (TACK-463 removed that failure by bounding what the export materialises).
	// The bound is not a tolerance: an export that reaches it covered only the
	// newest rows, and the chain verdict fails the drill rather than pass on
	// the slice it could see. Raising this raises how large a ledger the drill
	// can still reach a verdict on, and costs memory in proportion.
	drillLedgerExportRowLimit = 50000
)

// drillLedgerOldest and drillLedgerLatest bound the export by nothing. The
// reader requires both bounds, so the full range is stated with sentinels no
// stored row can fall outside. Bounding by a time window instead would leave
// rows out mid-chain, and every omission raises the report's chain-gap count.
// Over a windowed export that count is an artifact of the window and says
// nothing about the ledger, which is why the shared verifier counts a gap
// rather than calling it a break. Asking for the whole range is what makes the
// count mean something here: nothing was left out on purpose, so a gap is a
// row the restore did not bring back, and the chain verdict fails on it.
var (
	drillLedgerOldest = time.Unix(0, 0).UTC()
	drillLedgerLatest = time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC)
)

// assertRestoredLedgerChain verifies the hash chain of the ledger the yugabyte
// leg restored. It creates a run-scoped reader role in the throwaway database,
// lists the orgs holding rows, exports a signed bundle per org, and fails the
// drill on any chain break the verifier reports.
func assertRestoredLedgerChain(ctx context.Context, r *restoreDrillCtx, containerName, database string) error {
	logger := telemetry.L(ctx)
	logger.InfoContext(ctx, "backup.restore_drill.ledger_chain.start")

	roleName := drillLedgerRoleName(r.RunID)
	if err := createDrillLedgerReader(ctx, r, containerName, database, roleName); err != nil {
		return err
	}
	orgs, err := restoredLedgerOrgs(ctx, r, containerName, database, roleName)
	if err != nil {
		return err
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		wrapped := fmt.Errorf("generate the drill's throwaway export key: %w", err)
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	reader, err := openRestoredLedgerReader(ctx, r, containerName, database, roleName)
	if err != nil {
		return err
	}
	defer reader.Close()

	bundleRoot := filepath.Join(r.Cfg.BackupRoot, "restore-drill-chain-"+r.RunID)
	if err := os.MkdirAll(bundleRoot, 0o700); err != nil {
		wrapped := fmt.Errorf("mkdir ledger bundle root %s: %w", bundleRoot, err)
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer os.RemoveAll(bundleRoot)

	export := func(exportCtx context.Context, orgID uuid.UUID, dir string) (int, error) {
		return exportRestoredOrgBundle(exportCtx, reader, privateKey, orgID, dir)
	}
	verify := func(dir string) (*audit.VerifyReport, error) {
		return audit.VerifyBundle(dir, publicKey)
	}
	return verifyRestoredLedgerChains(ctx, orgs, bundleRoot, export, verify)
}

// drillLedgerRoleName derives the run-scoped login role the drill reads the
// restored ledger as. The run id carries a timestamp and a pid, so it is
// folded to the lower-case letters, digits, and underscores an unquoted SQL
// identifier accepts.
func drillLedgerRoleName(runID string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, runID)
	return "tack_drill_reader_" + safe
}

// createDrillLedgerReader creates the run-scoped login role and grants it the
// ledger's read base role. The drill reads through the same base role the
// product reads through rather than through the scratch database's bootstrap
// user, because audit.events forces row-level security: only a member of
// audit_reader is covered by a select policy, so any other identity either
// sees nothing or depends on happening to hold a superuser bypass.
func createDrillLedgerReader(ctx context.Context, r *restoreDrillCtx, containerName, database, roleName string) error {
	logger := telemetry.L(ctx)
	statements := []string{
		fmt.Sprintf("CREATE ROLE %s LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD %s",
			roleName, quoteSQLStringLiteral(r.YBPass)),
		"GRANT audit_reader TO " + roleName,
	}
	for _, statement := range statements {
		if err := ybRunSQL(ctx, r, containerName, database, "-v", "ON_ERROR_STOP=1", "-c", statement); err != nil {
			wrapped := fmt.Errorf("create the drill's ledger reader role %s: %w", roleName, err)
			logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.failed",
				slog.String("err", wrapped.Error()))
			return wrapped
		}
	}
	logger.InfoContext(ctx, "backup.restore_drill.ledger_chain.reader_created",
		slog.String("role", roleName))
	return nil
}

// exportRestoredOrgBundle writes one org's signed bundle with the same export
// the operator `audit export` command runs, so the drill rehearses the
// documented procedure rather than a private imitation of it.
func exportRestoredOrgBundle(
	ctx context.Context,
	reader *audit.Reader,
	signer ed25519.PrivateKey,
	orgID uuid.UUID,
	dir string,
) (int, error) {
	manifest, err := audit.Export(ctx, reader, signer, drillLedgerSigningKeyID, audit.QueryFilter{
		OrgID:     orgID,
		Oldest:    drillLedgerOldest,
		Latest:    drillLedgerLatest,
		Action:    "",
		ActorID:   uuid.Nil,
		EntityID:  uuid.Nil,
		RequestID: "",
		TraceID:   "",
		Limit:     drillLedgerExportRowLimit,
	}, dir)
	if err != nil {
		wrapped := fmt.Errorf("export org %s from the restored ledger: %w", orgID, err)
		telemetry.L(ctx).ErrorContext(ctx, "backup.restore_drill.ledger_chain.export_failed",
			slog.String("org_id", orgID.String()), slog.String("err", wrapped.Error()))
		return 0, wrapped
	}
	return manifest.RowCount, nil
}
