package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestParseSignerSetAcceptsTrimsAndDedupes(t *testing.T) {
	t.Parallel()
	set, err := ParseSignerSet(" ed25519:12bdb0b77570f79e, ed25519:f04a5d764fecb815 ,ed25519:12bdb0b77570f79e,, ")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !set.Configured() {
		t.Fatal("a set with two members reports unconfigured")
	}
	if got := set.Identifiers(); len(got) != 2 || got[0] != "ed25519:12bdb0b77570f79e" || got[1] != "ed25519:f04a5d764fecb815" {
		t.Fatalf("identifiers = %v, want the two listed once each in order", got)
	}
	if !set.Allows("ed25519:f04a5d764fecb815") || set.Allows("ed25519:0000000000000000") {
		t.Fatal("membership is wrong")
	}
}

func TestParseSignerSetRefusesAMalformedEntry(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"ed25519:12bdb0b77570f79", "12bdb0b77570f79e", "ed25519:12BDB0B77570F79E", "rsa:12bdb0b77570f79e"} {
		if _, err := ParseSignerSet(raw); err == nil {
			t.Fatalf("parse %q accepted, want a shape error", raw)
		}
	}
	empty, err := ParseSignerSet("")
	if err != nil || empty.Configured() {
		t.Fatalf("empty = (%v, configured %v), want an unconfigured set and no error", err, empty.Configured())
	}
}

// TestKeyIdentifierIsTheOneTheNotarizerStamps pins that the identifier a
// loaded signing key carries is the same derivation the set lists and the
// export manifest names, so a key measured on a host matches the ledger.
func TestKeyIdentifierIsTheOneTheNotarizerStamps(t *testing.T) {
	t.Parallel()
	keyPath := writeTempSigningKey(t)
	priv, id, err := loadEd25519Key(keyPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("public half is not ed25519")
	}
	if id != KeyIdentifier(pub) {
		t.Fatalf("loaded id %s != KeyIdentifier %s", id, KeyIdentifier(pub))
	}
	if !keyIdentifierShape.MatchString(id) {
		t.Fatalf("identifier %q is not the shape the set accepts", id)
	}
}

// TestVerifyBundleRejectsASignerOutsideTheSet exports a real bundle, verifies
// it against a set that names its signer, then against one that does not:
// the signature is valid both times and only the second run fails, naming
// the signer.
func TestVerifyBundleRejectsASignerOutsideTheSet(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	orgID := uuid.Must(uuid.NewV7())
	dir := t.TempDir()
	now := time.Now().UTC()
	filter := QueryFilter{OrgID: orgID, Oldest: now.Add(-2 * time.Hour), Latest: now.Add(time.Hour), Limit: 0}
	if _, err := Export(context.Background(), exportTestRows(t, orgID, 3, nil), priv, KeyIdentifier(pub), filter, dir); err != nil {
		t.Fatalf("export: %v", err)
	}

	allowed, err := ParseSignerSet(KeyIdentifier(pub))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	report, err := VerifyBundleWithSigners(dir, pub, allowed)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !report.SignerSetConfigured || !report.SignerAllowed || report.ManifestSigner != KeyIdentifier(pub) {
		t.Fatalf("report = configured %v allowed %v signer %s, want the signer accepted", report.SignerSetConfigured, report.SignerAllowed, report.ManifestSigner)
	}
	if err := report.Err(); err != nil {
		t.Fatalf("a bundle signed inside the set failed: %v", err)
	}

	other, err := ParseSignerSet("ed25519:0000000000000000")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	report, err = VerifyBundleWithSigners(dir, pub, other)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !report.SignatureOK {
		t.Fatal("the signature stopped verifying; the test is not isolating the set check")
	}
	verdict := report.Err()
	if verdict == nil || !strings.Contains(verdict.Error(), "outside the valid signer set") || !strings.Contains(verdict.Error(), KeyIdentifier(pub)) {
		t.Fatalf("verdict = %v, want a rejection naming the signer", verdict)
	}

	unconfigured, err := VerifyBundleWithSigners(dir, pub, SignerSet{ordered: nil, members: nil})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if unconfigured.SignerSetConfigured || unconfigured.Err() != nil {
		t.Fatalf("with no set the bundle must be reported, not judged: configured %v err %v", unconfigured.SignerSetConfigured, unconfigured.Err())
	}
}
