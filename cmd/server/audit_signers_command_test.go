package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

// TestAuditSignersCommandWiresTheFlagsThroughTheReader runs the real command
// against a migrated test ledger. Cfg supplies the signer set, key path, and
// reader DSN. The test inserts rows under the local key and another key,
// then checks the returned errors with that other key included or excluded
// from the set. It checks --allow-unverified and --since after the
// configuration checks permit reader verification.
func TestAuditSignersCommandWiresTheFlagsThroughTheReader(t *testing.T) {
	dsn := testenv.Ledger(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}

	t.Cleanup(pool.Close)
	orgID := uuid.Must(uuid.NewV7())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM audit.notarizations WHERE org_id = $1`, orgID)
	})

	dir := t.TempDir()
	keyPath := writeVerifyTestKey(t, dir)
	localPriv, localID, err := loadAuditKey(keyPath)
	if err != nil {
		t.Fatalf("load key: %v", err)
	}

	otherPub, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("other key: %v", err)
	}

	otherID := audit.KeyIdentifier(otherPub)
	since := time.Now().UTC().Add(-time.Minute)
	insertSignedNotarization(t, pool, orgID, localPriv, localID, since.Add(time.Second))
	insertSignedNotarization(t, pool, orgID, otherPriv, otherID, since.Add(2*time.Second))

	run := func(signers string, allowUnverified bool, sinceFlag string) error {
		factory := &cli.Factory{
			Cfg: &config.Config{
				AuditReaderDSN:      dsn,
				AuditSigningKeyPath: keyPath,
				AuditValidSigners:   signers,
			},
			In:  nil,
			Out: os.Stdout,
			Err: os.Stderr,
		}
		input := auditSignersInput{
			InputMarker:     clispec.InputMarker{},
			Since:           sinceFlag,
			Signers:         "",
			Pub:             "",
			AllowUnverified: allowUnverified,
		}

		return auditSignersOp(factory).Run(ctx, input, clispec.NewCLISink(factory))
	}

	sinceFlag := since.Format(time.RFC3339)
	if err := run(localID, false, sinceFlag); err == nil ||
		!strings.Contains(err.Error(), otherID) {
		t.Fatalf("err = %v, want the other key rejected by name", err)
	}

	if err := run(localID+","+otherID, false, sinceFlag); err == nil ||
		!strings.Contains(err.Error(), "allow-unverified") {
		t.Fatalf("err = %v, want the other key's row to fail as unverified without the flag", err)
	}

	if err := run(localID+","+otherID, true, sinceFlag); err != nil {
		t.Fatalf("err = %v, want the acknowledged run to pass", err)
	}

	// A --since value after both inserted rows excludes them from the query.
	// The other key's row must not cause rejection.
	if err := run(localID, false, since.Add(time.Minute).Format(time.RFC3339)); err != nil &&
		strings.Contains(err.Error(), otherID) {
		t.Fatalf("err = %v, want --since to exclude the older rows", err)
	}
}

func insertSignedNotarization(
	t *testing.T,
	pool *pgxpool.Pool,
	orgID uuid.UUID,
	priv ed25519.PrivateKey,
	signingKey string,
	at time.Time,
) {
	t.Helper()
	root := make([]byte, 32)
	if _, err := rand.Read(root); err != nil {
		t.Fatalf("root: %v", err)
	}

	_, err := pool.Exec(
		context.Background(),
		`
		INSERT INTO audit.notarizations (org_id, notarized_at, merkle_root, shard_heads, signature, signing_key, signing_host)
		VALUES ($1, $2, $3, '[]'::jsonb, $4, $5, 'command-test')
	`,
		orgID,
		at,
		root,
		ed25519.Sign(priv, root),
		signingKey,
	)
	if err != nil {
		t.Fatalf("insert notarization: %v", err)
	}
}
