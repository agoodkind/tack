package integration

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

// attributionClockSkew widens the export window before the ledger clock
// reading. The test process clock stamps the event time.
const attributionClockSkew = 10 * time.Second

// requireVerifiedBundle exports the agent's break-glass audit.events rows
// recorded since since with audit.Export and requires audit.VerifyBundle to
// pass on a bundle of exactly count rows.
func requireVerifiedBundle(t *testing.T, ledgerDSN string, pool *pgxpool.Pool, since time.Time, count int) {
	t.Helper()
	reader, err := audit.NewReader(t.Context(), ledgerDSN)
	if err != nil {
		t.Fatalf("open the audit reader: %v", err)
	}
	t.Cleanup(reader.Close)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate the export key: %v", err)
	}
	filter := audit.QueryFilter{
		OrgID: audit.SystemOrgID(), Oldest: since.Add(-attributionClockSkew), Latest: ledgerNow(t, pool).Add(time.Minute),
		Action: string(audit.VerbOpsDBBreakGlass), ActorID: cli.ServiceActorID(attributionService),
		EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: 0,
	}
	dir := t.TempDir()
	manifest, err := audit.Export(t.Context(), reader, privateKey, audit.KeyIdentifier(publicKey), filter, dir)
	if err != nil {
		t.Fatalf("export the agent's audit.events rows: %v", err)
	}
	report, err := audit.VerifyBundle(dir, publicKey)
	if err != nil {
		t.Fatalf("verify the exported bundle: %v", err)
	}
	if verdict := report.Err(); verdict != nil || manifest.RowCount != count || report.HashMatches != count {
		t.Fatalf("bundle of %d rows with %d hash matches failed verification (%v), want %d verified rows",
			manifest.RowCount, report.HashMatches, verdict, count)
	}
}
