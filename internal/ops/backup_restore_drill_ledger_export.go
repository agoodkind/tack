// Restored-ledger export creates a run-specific reader role and signs one
// bundle per organization for offline chain verification.

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

// drillLedgerSigningKeyID identifies an ephemeral per-run signing key. The
// drill never uses the production signing key. Signatures establish bundle
// identity; independent row hashes and chain links establish ledger integrity.
const drillLedgerSigningKeyID = "ed25519:restore-drill"

// drillLedgerExportUnlimited removes the row cap. Streaming bounds memory use
// independently of organization size without operator-adjusted export limits.
const drillLedgerExportUnlimited = 0

// Full-range sentinels prevent intentional mid-chain omissions. Filtered
// bundles can contain legitimate sequence gaps; this full-range export treats
// gaps as incomplete restored data instead.
var (
	drillLedgerOldest = time.Unix(0, 0).UTC()
	drillLedgerLatest = time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC)
)

// assertRestoredLedgerChain creates a scratch reader, lists restored orgs, and
// exports signed bundles for complete chain verification.
func assertRestoredLedgerChain(
	ctx context.Context,
	r *restoreDrillCtx,
	containerName string,
	database string,
) error {
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
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.failed",
			slog.String("err", wrapped.Error()),
		)

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
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.failed",
			slog.String("err", wrapped.Error()),
		)

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

// drillLedgerRoleName normalizes timestamp-and-PID run IDs to lowercase letters,
// digits, and underscores for an unquoted SQL login role.
func drillLedgerRoleName(runID string) string {
	safe := strings.Map(
		func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				return r
			case r >= 'A' && r <= 'Z':
				return r + ('a' - 'A')
			default:
				return '_'
			}
		},
		runID,
	)

	return "tack_drill_reader_" + safe
}

// createDrillLedgerReader grants audit_reader to a run-specific login role.
// Reads exercise the product's base-role policy rather than bootstrap superuser
// bypass. FORCE ROW LEVEL SECURITY requires audit_reader membership for SELECT.
func createDrillLedgerReader(
	ctx context.Context,
	r *restoreDrillCtx,
	containerName string,
	database string,
	roleName string,
) error {
	logger := telemetry.L(ctx)

	statements := []string{
		fmt.Sprintf(
			"CREATE ROLE %s LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD %s",
			roleName,
			quoteSQLStringLiteral(r.YBPass),
		),
		"GRANT audit_reader TO " + roleName,
	}
	for _, statement := range statements {
		if err := ybRunSQL(
			ctx,
			r,
			containerName,
			database,
			"-v",
			"ON_ERROR_STOP=1",
			"-c",
			statement,
		); err != nil {
			wrapped := fmt.Errorf("create the drill's ledger reader role %s: %w", roleName, err)
			logger.ErrorContext(
				ctx,
				"backup.restore_drill.ledger_chain.failed",
				slog.String("err", wrapped.Error()),
			)

			return wrapped
		}
	}

	logger.InfoContext(
		ctx,
		"backup.restore_drill.ledger_chain.reader_created",
		slog.String("role", roleName),
	)

	return nil
}

// exportRestoredOrgBundle uses the operator audit export implementation with
// unlimited rows and full time bounds. Historical nil-org rows use the same
// per-(org, shard) chain and require explicit NilOrg permission to override
// the reader's default missing-org refusal.
func exportRestoredOrgBundle(
	ctx context.Context,
	reader audit.RowSource,
	signer ed25519.PrivateKey,
	orgID uuid.UUID,
	dir string,
) (int, error) {
	manifest, err := audit.Export(
		ctx,
		reader,
		signer,
		drillLedgerSigningKeyID,
		audit.QueryFilter{
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
		},
		dir,
	)
	if err != nil {
		wrapped := fmt.Errorf("export org %s from the restored ledger: %w", orgID, err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.export_failed",
			slog.String("org_id", orgID.String()),
			slog.String("err", wrapped.Error()),
		)

		return 0, wrapped
	}

	return manifest.RowCount, nil
}
