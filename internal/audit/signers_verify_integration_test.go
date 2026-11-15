package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// insertNotarization writes the production notarization format with the
// supplied key, including unauthorized keys for signer rejection tests.
func insertNotarization(
	t *testing.T,
	pool *pgxpool.Pool,
	orgID uuid.UUID,
	priv ed25519.PrivateKey,
	signingKey string,
	host string,
	at time.Time,
	tamper bool,
) {
	t.Helper()
	root := make([]byte, 32)
	if _, err := rand.Read(root); err != nil {
		t.Fatalf("root: %v", err)
	}

	signature := ed25519.Sign(priv, root)
	if tamper {
		signature[0] ^= 0xff
	}

	_, err := pool.Exec(
		context.Background(),
		`
		INSERT INTO audit.notarizations (org_id, notarized_at, merkle_root, shard_heads, signature, signing_key, signing_host)
		VALUES ($1, $2, $3, '[]'::jsonb, $4, $5, $6)
	`,
		orgID,
		at,
		root,
		signature,
		signingKey,
		host,
	)
	if err != nil {
		t.Fatalf("insert notarization: %v", err)
	}
}

// Verification requires explicit acknowledgment when an allowed signer's key
// is unavailable locally. Acknowledgment must not accept a failed local
// signature (TACK-437).
func TestVerifySignersRejectsAKeyOutsideTheSet(t *testing.T) {
	dsn := integrationDSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}

	t.Cleanup(pool.Close)
	orgID := uuid.Must(uuid.NewV7())
	otherOrgID := uuid.Must(uuid.NewV7())
	t.Cleanup(func() {
		_, _ = pool.Exec(
			ctx,
			`DELETE FROM audit.notarizations WHERE org_id = $1 OR org_id = $2`,
			orgID,
			otherOrgID,
		)
	})

	issuedPub, issuedPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("issued key: %v", err)
	}

	roguePub, roguePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("rogue key: %v", err)
	}

	issued := KeyIdentifier(issuedPub)
	rogue := KeyIdentifier(roguePub)
	// Compare timestamps at the column's stored microsecond precision.
	since := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	insertNotarization(
		t,
		pool,
		orgID,
		issuedPriv,
		issued,
		"tack-qa-owner",
		since.Add(time.Second),
		false,
	)
	// Insert unauthorized rows across two orgs in reverse time order. The
	// report's time span must use timestamps rather than scan order.
	insertNotarization(
		t,
		pool,
		orgID,
		roguePriv,
		rogue,
		"rogue-guest",
		since.Add(3*time.Second),
		false,
	)
	insertNotarization(
		t,
		pool,
		otherOrgID,
		roguePriv,
		rogue,
		"rogue-guest",
		since.Add(2*time.Second),
		false,
	)

	reader := openTestReader(t, pool)
	set, err := ParseSignerSet(issued)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	report, err := reader.VerifySigners(
		ctx,
		SignerCheck{Set: set, LocalPub: issuedPub, Since: since, AcknowledgeUnverified: false},
	)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if report.RowsScanned != 3 || report.Accepted != 1 || report.SignatureVerified != 1 ||
		report.RejectedUnknownSigner != 2 ||
		report.SignatureFailed != 0 {
		t.Fatalf("report = %+v, want 3 scanned, 1 accepted and verified, 2 rejected", *report)
	}

	if len(report.UnknownSigners) != 1 || report.UnknownSigners[0].SigningKey != rogue ||
		report.UnknownSigners[0].Rows != 2 ||
		len(report.UnknownSigners[0].SigningHosts) != 1 ||
		report.UnknownSigners[0].SigningHosts[0] != "rogue-guest" {
		t.Fatalf(
			"unknown signers = %+v, want the rogue key from rogue-guest with 2 rows",
			report.UnknownSigners,
		)
	}

	if report.LocalSigner != issued {
		t.Fatalf("local signer = %s, want %s", report.LocalSigner, issued)
	}

	if first, last := report.UnknownSigners[0].First, report.UnknownSigners[0].Last; !first.Equal(
		since.Add(2*time.Second),
	) ||
		!last.Equal(since.Add(3*time.Second)) {
		t.Fatalf(
			"rogue span = %s to %s, want the earliest and latest row times regardless of scan order",
			first,
			last,
		)
	}

	verdict := report.Err()
	if verdict == nil || !strings.Contains(verdict.Error(), rogue) {
		t.Fatalf("verdict = %v, want a rejection naming %s", verdict, rogue)
	}

	// A claimed accepted identifier does not validate another key's signature.
	insertNotarization(
		t,
		pool,
		orgID,
		roguePriv,
		issued,
		"rogue-guest",
		since.Add(4*time.Second),
		false,
	)
	report, err = reader.VerifySigners(
		ctx,
		SignerCheck{Set: set, LocalPub: issuedPub, Since: since, AcknowledgeUnverified: false},
	)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if report.SignatureFailed != 1 || report.SignatureVerified != 1 {
		t.Fatalf(
			"report = %+v, want the forged-identifier row counted as a signature failure",
			*report,
		)
	}

	// Adding the identifier allows membership but not local signature
	// verification. These rows still require explicit acknowledgement.
	both, err := ParseSignerSet(issued + "," + rogue)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	report, err = reader.VerifySigners(
		ctx,
		SignerCheck{Set: both, LocalPub: issuedPub, Since: since, AcknowledgeUnverified: false},
	)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if report.RejectedUnknownSigner != 0 || report.AllowedUnverifiedHere != 2 ||
		len(report.UnknownSigners) != 0 {
		t.Fatalf(
			"report = %+v, want zero rejected identifiers and two locally unverified rows",
			*report,
		)
	}

	if verdict := report.Err(); verdict == nil ||
		!strings.Contains(verdict.Error(), "allow-unverified") {
		t.Fatalf("verdict = %v, want unverified rows to fail without acknowledgement", verdict)
	}

	acknowledged, err := reader.VerifySigners(
		ctx,
		SignerCheck{Set: both, LocalPub: issuedPub, Since: since, AcknowledgeUnverified: true},
	)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	// Acknowledging unverified rows must not accept a failed local signature.
	if verdict := acknowledged.Err(); verdict == nil ||
		strings.Contains(verdict.Error(), "allow-unverified") ||
		!strings.Contains(verdict.Error(), "signature") {
		t.Fatalf("verdict = %v, want only the signature failure left", verdict)
	}

	if _, err := reader.VerifySigners(
		ctx,
		SignerCheck{
			Set:                   SignerSet{ordered: nil, members: nil},
			LocalPub:              issuedPub,
			Since:                 since,
			AcknowledgeUnverified: false,
		},
	); err == nil {
		t.Fatal("verification ran without a signer set")
	}

	if _, err := reader.VerifySigners(
		ctx,
		SignerCheck{Set: set, LocalPub: nil, Since: since, AcknowledgeUnverified: false},
	); err == nil {
		t.Fatal("verification ran without a local key")
	}

	// Reject a local key outside the set before scanning. Acknowledgement
	// must not permit a report that cannot verify any allowed local signatures.
	rogueOnly, err := ParseSignerSet(rogue)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if _, err := reader.VerifySigners(
		ctx,
		SignerCheck{Set: rogueOnly, LocalPub: issuedPub, Since: since, AcknowledgeUnverified: true},
	); !errors.Is(err, ErrLocalKeyNotInSet) {
		t.Fatalf("err = %v, want the refusal for a local key outside the set", err)
	}
}
