// The YugabyteDB restore drill creates a reader role in the restored database,
// finds organizations with audit rows, and exports one signed bundle per
// organization for chain verification.

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

// drillLedgerSigningKeyID identifies the temporary signing key in each bundle
// manifest. Each run generates and discards its own key without reading the
// production signing key. The signature check verifies the bundle that this
// export wrote. Row-hash and chain-link checks verify ledger integrity without
// a signing key.
const drillLedgerSigningKeyID = "ed25519:restore-drill"

// drillLedgerExportUnlimited requests every matching row. The export writes
// each streamed row without collecting the full result in memory. This avoids
// a row-count cap that an operator would need to raise as the ledger grows.
// Compose sets a 512 MiB memory limit for tack-ops. Streaming does not
// guarantee that every ledger size fits that budget.
const drillLedgerExportUnlimited = 0

// drillLedgerOldest and drillLedgerLatest supply the time bounds the reader
// requires. The configured range starts at the Unix epoch and ends before
// January 1, 9999. The row-count reconciliation rejects exports that omit
// counted rows outside that range.
//
// A narrow time window can omit interior chain rows. The shared verifier
// counts those omissions as sequence gaps rather than broken links. This drill
// requests the full configured range and rejects reported gaps because it
// cannot verify prev_hash across them.
var (
	drillLedgerOldest = time.Unix(0, 0).UTC()
	drillLedgerLatest = time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC)
)

// assertRestoredLedgerChain checks the audit hash chains in the YugabyteDB
// restore. It creates a temporary reader role, lists organizations with rows,
// exports a signed bundle for each, and fails the drill when verification
// reports a broken chain.
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

// drillLedgerRoleName derives the run-specific login role for restored ledger
// reads. Run IDs contain a timestamp and process ID. The function converts
// them to lowercase letters, digits, and underscores for an unquoted SQL
// identifier.
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

// createDrillLedgerReader creates the run-specific login and grants it
// audit_reader, the base role for ledger reads. audit.events forces row-level
// security. Membership supplies the SELECT policy without relying on the
// scratch database's bootstrap user or a superuser bypass.
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

// exportRestoredOrgBundle uses audit.Export, the export operation that the
// operator audit export command invokes, to write one signed organization
// bundle. The drill therefore exercises that operation directly.
//
// The RowSource interface permits tests to inspect the requested filter. The
// filter requests every matching row; a subset could still produce a valid
// bundle without establishing complete ledger coverage.
//
// The organization list includes the nil organization when historical rows
// predate mandatory organization recording. Those rows use the same per-org
// and shard chains as tenant rows. The filter explicitly selects the nil
// organization because the reader otherwise rejects it.
func exportRestoredOrgBundle(
	ctx context.Context,
	reader audit.RowSource,
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
		Limit:     drillLedgerExportUnlimited,
		NilOrg:    orgID == uuid.Nil,
	}, dir)
	if err != nil {
		wrapped := fmt.Errorf("export org %s from the restored ledger: %w", orgID, err)
		telemetry.L(ctx).ErrorContext(ctx, "backup.restore_drill.ledger_chain.export_failed",
			slog.String("org_id", orgID.String()), slog.String("err", wrapped.Error()))
		return 0, wrapped
	}
	return manifest.RowCount, nil
}
