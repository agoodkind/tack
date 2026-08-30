package ops

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// cleanLedgerReport is the shape of a report on a bundle that verified: every
// row re-hashed, no broken link, and a manifest whose digest and signature
// both check out.
func cleanLedgerReport(dir string, rows int) *audit.VerifyReport {
	return &audit.VerifyReport{
		BundleDir: dir, RowsScanned: rows, HashMatches: rows, ChainGapCount: 0,
		ChainBreaks: nil, FileSHA256OK: true, SignatureOK: true, ManifestSubject: "test-bundle",
	}
}

// fixedExport stands in for the database half of the leg: it reports the row
// count the caller asked for and writes nothing, so a test can drive the
// verdict without a restored ledger.
func fixedExport(rows int) drillLedgerExportFunc {
	return func(context.Context, uuid.UUID, string) (int, error) {
		return rows, nil
	}
}

// ledgerOrg is one org the restored ledger holds rows for. The count is what
// the export's own row count is reconciled against, so a test that wants a
// complete export passes the same number to both.
func ledgerOrg(rows int) drillLedgerOrg {
	return drillLedgerOrg{ID: uuid.Must(uuid.NewV7()), RowCount: rows}
}

// TestRestoredLedgerChainFailsOnReportedBreak is the point of the whole leg: a
// verifier that reports a broken chain must fail the drill. The report is what
// the drill reads, so a verifier that found breaks and still exited zero
// cannot pass here either.
func TestRestoredLedgerChainFailsOnReportedBreak(t *testing.T) {
	org := ledgerOrg(4)
	verify := func(dir string) (*audit.VerifyReport, error) {
		report := cleanLedgerReport(dir, 4)
		report.ChainBreaks = []string{"row 019dd226-440e-729a-a442-281aaf73ca30 has previous-hash link mismatch"}
		return report, nil
	}

	err := verifyRestoredLedgerChains(
		context.Background(), []drillLedgerOrg{org}, t.TempDir(), fixedExport(4), verify)
	if err == nil {
		t.Fatal("the drill passed a restored ledger whose chain the verifier reported as broken")
	}
	if !strings.Contains(err.Error(), "1 chain break(s)") || !strings.Contains(err.Error(), org.ID.String()) {
		t.Fatalf("error = %v, want it to name the break count and the org", err)
	}
}

// TestRestoredLedgerChainPassesOnCleanReport pins the other half: a ledger
// whose bundles verify must not fail the drill, or the check is useless noise.
func TestRestoredLedgerChainPassesOnCleanReport(t *testing.T) {
	orgs := []drillLedgerOrg{ledgerOrg(3), ledgerOrg(3)}
	verify := func(dir string) (*audit.VerifyReport, error) {
		return cleanLedgerReport(dir, 3), nil
	}

	if err := verifyRestoredLedgerChains(
		context.Background(), orgs, t.TempDir(), fixedExport(3), verify); err != nil {
		t.Fatalf("clean bundles failed the drill: %v", err)
	}
}

// TestRestoredLedgerChainFailsOnSequenceGap pins the rule this leg reads a gap
// by. The drill exports every row the ledger holds and reconciles the bundle
// against the ledger's own count, so nothing was left out on purpose. A missing
// sequence number in that bundle is a row the restore did not bring back, and
// the verifier cannot compare prev_hash across it: the chain is unverified
// exactly where the ledger is incomplete, so the drill fails. Tolerating gaps
// here is only correct for a time-bounded export, which this is not.
func TestRestoredLedgerChainFailsOnSequenceGap(t *testing.T) {
	org := ledgerOrg(6)
	verify := func(dir string) (*audit.VerifyReport, error) {
		report := cleanLedgerReport(dir, 6)
		report.ChainGapCount = 11
		return report, nil
	}

	err := verifyRestoredLedgerChains(
		context.Background(), []drillLedgerOrg{org}, t.TempDir(), fixedExport(6), verify)
	if err == nil {
		t.Fatal("the drill passed a whole-ledger export with rows missing from inside the chain")
	}
	if !strings.Contains(err.Error(), "11 sequence gap(s)") || !strings.Contains(err.Error(), org.ID.String()) {
		t.Fatalf("error = %v, want it to name the gap count and the org", err)
	}
}

// TestRestoredLedgerChainFailsWhenTheExportHitsItsRowLimit is the second half
// of the same rule, one step earlier. The export is capped so the drill cannot
// be killed for memory on a production-sized org, and a cap that truncates
// silently would leave the drill verifying the newest rows and reporting
// success for the whole ledger, with older corruption and entire quiet shards
// never read. Reaching the cap is a verdict the drill cannot reach, so it
// fails and names how much of the ledger it did not check.
func TestRestoredLedgerChainFailsWhenTheExportHitsItsRowLimit(t *testing.T) {
	const held = drillLedgerExportRowLimit * 3
	org := drillLedgerOrg{ID: uuid.Must(uuid.NewV7()), RowCount: held}
	verify := func(string) (*audit.VerifyReport, error) {
		t.Fatal("the verifier ran on a bundle holding only part of the org's rows")
		return nil, nil
	}

	err := verifyRestoredLedgerChains(
		context.Background(), []drillLedgerOrg{org}, t.TempDir(),
		fixedExport(drillLedgerExportRowLimit), verify)
	if err == nil {
		t.Fatal("the drill passed an export that stopped at its row limit")
	}
	for _, want := range []string{
		org.ID.String(),
		strconv.Itoa(drillLedgerExportRowLimit),
		strconv.Itoa(held),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to name %q", err, want)
		}
	}
}

// TestRestoredLedgerChainFailsOnAnyShortfall pins that the reconciliation is
// against the ledger's row count and not against the cap alone. An export that
// stops short for any other reason, such as a filter or a role that cannot see
// every row, leaves the same unverified rows behind and fails the same way.
func TestRestoredLedgerChainFailsOnAnyShortfall(t *testing.T) {
	org := ledgerOrg(9)
	verify := func(string) (*audit.VerifyReport, error) {
		t.Fatal("the verifier ran on a bundle holding only part of the org's rows")
		return nil, nil
	}

	err := verifyRestoredLedgerChains(
		context.Background(), []drillLedgerOrg{org}, t.TempDir(), fixedExport(7), verify)
	if err == nil {
		t.Fatal("the drill passed an export that wrote fewer rows than the ledger holds")
	}
	if !strings.Contains(err.Error(), "wrote 7 of the 9 rows") {
		t.Fatalf("error = %v, want it to name what the export covered", err)
	}
}

// TestRestoredLedgerChainFailsOnEmptyLedger records the decision that a
// restored ledger holding no rows is a failure. Every operator command records
// through the ledger and the export command records itself, so a ledger that
// could be exported holds rows; zero rows means the restore did not bring it
// back, and a chain check with nothing to check is the silent skip this leg
// exists to remove.
func TestRestoredLedgerChainFailsOnEmptyLedger(t *testing.T) {
	err := verifyRestoredLedgerChains(
		context.Background(), nil, t.TempDir(), fixedExport(0), func(string) (*audit.VerifyReport, error) {
			t.Fatal("the verifier ran on a ledger with no orgs")
			return nil, nil
		})
	if err == nil || !strings.Contains(err.Error(), "holds no rows") {
		t.Fatalf("error = %v, want the empty ledger named as a failure", err)
	}
}

// TestRestoredLedgerChainFailsWhenVerifyCannotRun pins that a verifier which
// cannot run fails the drill rather than being logged and skipped.
func TestRestoredLedgerChainFailsWhenVerifyCannotRun(t *testing.T) {
	verify := func(string) (*audit.VerifyReport, error) {
		return nil, errors.New("bundle manifest unreadable")
	}

	err := verifyRestoredLedgerChains(
		context.Background(), []drillLedgerOrg{ledgerOrg(2)}, t.TempDir(), fixedExport(2), verify)
	if err == nil || !strings.Contains(err.Error(), "bundle manifest unreadable") {
		t.Fatalf("error = %v, want the unrunnable verify to fail the drill", err)
	}
}

// TestRestoredLedgerChainFailsWhenExportFails pins the same rule one step
// earlier: no bundle means no verdict, which is a failed drill and never a
// pass.
func TestRestoredLedgerChainFailsWhenExportFails(t *testing.T) {
	export := func(context.Context, uuid.UUID, string) (int, error) {
		return 0, errors.New("ledger unreachable")
	}
	verify := func(string) (*audit.VerifyReport, error) {
		t.Fatal("the verifier ran on a bundle the export never wrote")
		return nil, nil
	}

	err := verifyRestoredLedgerChains(
		context.Background(), []drillLedgerOrg{ledgerOrg(2)}, t.TempDir(), export, verify)
	if err == nil || !strings.Contains(err.Error(), "ledger unreachable") {
		t.Fatalf("error = %v, want the failed export to fail the drill", err)
	}
}

// TestRestoredLedgerChainFailsOnTamperedBundleThroughTheRealVerifier runs the
// production verifier over a bundle whose rows do not re-hash, so the break
// the drill acts on is one audit.VerifyBundle actually produced rather than
// one the test asserted. This is the wiring: swapping the drill's break check
// for a row count leaves the report-level tests above green and this one red.
func TestRestoredLedgerChainFailsOnTamperedBundleThroughTheRealVerifier(t *testing.T) {
	org := ledgerOrg(2)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	export := func(_ context.Context, exported uuid.UUID, dir string) (int, error) {
		return writeUnhashableBundle(t, dir, exported, privateKey), nil
	}
	verify := func(dir string) (*audit.VerifyReport, error) {
		return audit.VerifyBundle(dir, publicKey)
	}

	err = verifyRestoredLedgerChains(
		context.Background(), []drillLedgerOrg{org}, t.TempDir(), export, verify)
	if err == nil {
		t.Fatal("the real verifier reported a broken bundle and the drill still passed")
	}
	if !strings.Contains(err.Error(), "chain break(s)") {
		t.Fatalf("error = %v, want the reported chain breaks to be the reason", err)
	}
}

// writeUnhashableBundle writes a correctly signed bundle whose two rows carry
// stored hashes that do not recompute from the rows. The events digest and the
// manifest signature are both valid, so the only thing that can reject this
// bundle is the chain check the drill reads.
func writeUnhashableBundle(t *testing.T, dir string, orgID uuid.UUID, signer ed25519.PrivateKey) int {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create bundle dir: %v", err)
	}
	stamp := time.Now().UTC().Truncate(time.Microsecond)
	var jsonl []byte
	for seq := int64(1); seq <= 2; seq++ {
		row := audit.Row{
			OrgID: orgID, EventTime: stamp, EventID: uuid.Must(uuid.NewV7()),
			Seq: seq, Shard: 1, ActorID: uuid.Must(uuid.NewV7()), ActorKind: 5,
			Action: "ops.test.restore_drill", Outcome: audit.OutcomeOK,
			EntityKind: "system", EntityID: orgID,
			Context:     audit.EventContext{OrgID: orgID, Source: audit.SourceSystem},
			Delta:       nil,
			Error:       nil,
			Extra:       nil,
			PIIRef:      nil,
			PrevHash:    nil,
			RowHash:     []byte("this is not the hash of this row"),
			HashVersion: 3,
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("marshal row: %v", err)
		}
		jsonl = append(jsonl, encoded...)
		jsonl = append(jsonl, '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), jsonl, 0o600); err != nil {
		t.Fatalf("write events: %v", err)
	}
	writeBundleManifest(t, dir, orgID, signer, jsonl, 2)
	return 2
}

// writeBundleManifest signs the manifest exactly as the export does, so the
// verifier's digest and signature checks pass and the chain check is the only
// thing left to fail.
func writeBundleManifest(
	t *testing.T,
	dir string,
	orgID uuid.UUID,
	signer ed25519.PrivateKey,
	jsonl []byte,
	rowCount int,
) {
	t.Helper()
	digest := sha256.Sum256(jsonl)
	manifest := audit.ExportManifest{
		ExportID: uuid.Must(uuid.NewV7()), OrgID: orgID,
		Oldest: drillLedgerOldest, Latest: drillLedgerLatest, RowCount: rowCount,
		FileSHA256: hex.EncodeToString(digest[:]), SignatureBy: drillLedgerSigningKeyID,
		Signature: "",
	}
	signable, err := json.Marshal(struct {
		ExportID    uuid.UUID `json:"export_id"`
		OrgID       uuid.UUID `json:"org_id"`
		Oldest      time.Time `json:"oldest"`
		Latest      time.Time `json:"latest"`
		RowCount    int       `json:"row_count"`
		FileSHA256  string    `json:"file_sha256"`
		SignatureBy string    `json:"signing_key_id"`
	}{
		manifest.ExportID, manifest.OrgID, manifest.Oldest, manifest.Latest,
		manifest.RowCount, manifest.FileSHA256, manifest.SignatureBy,
	})
	if err != nil {
		t.Fatalf("marshal signable manifest: %v", err)
	}
	manifest.Signature = hex.EncodeToString(ed25519.Sign(signer, signable))
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), encoded, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

// TestRestoredLedgerOrgCarriesItsRowCount pins the format the row count is read
// in. The count is what every export is reconciled against, so a line this
// misreads would either fail every org or, worse, hand the verdict a number the
// ledger never reported.
func TestRestoredLedgerOrgCarriesItsRowCount(t *testing.T) {
	const orgID = "019dd226-440e-729a-a442-281aaf73ca30"
	org, err := parseRestoredLedgerOrg(context.Background(), orgID+"|412903")
	if err != nil {
		t.Fatalf("parse a ledger org row: %v", err)
	}
	if org.ID.String() != orgID {
		t.Fatalf("org id = %s, want %s", org.ID, orgID)
	}
	if org.RowCount != 412903 {
		t.Fatalf("row count = %d, want 412903", org.RowCount)
	}
}

// TestRestoredLedgerOrgWithoutARowCountIsAnError pins that a line the drill
// cannot read fails rather than being skipped. A skipped line is an org whose
// chain nothing checked, which is the silent pass this leg exists to remove.
func TestRestoredLedgerOrgWithoutARowCountIsAnError(t *testing.T) {
	ctx := context.Background()
	if _, err := parseRestoredLedgerOrg(ctx, "019dd226-440e-729a-a442-281aaf73ca30"); err == nil {
		t.Fatal("a line carrying no row count parsed as an org")
	}
	if _, err := parseRestoredLedgerOrg(ctx, "019dd226-440e-729a-a442-281aaf73ca30|many"); err == nil {
		t.Fatal("a line carrying an unreadable row count parsed as an org")
	}
}

// TestDrillLedgerRoleNameIsAnUnquotedIdentifier pins that the run-scoped role
// name survives interpolation into CREATE ROLE. The run id carries a timestamp
// and a pid, and an identifier holding a dash or an upper-case letter would
// either fail the statement or bind under a name the connection string does
// not match.
func TestDrillLedgerRoleNameIsAnUnquotedIdentifier(t *testing.T) {
	name := drillLedgerRoleName("rt20260830T101500Z-4711")
	if name != "tack_drill_reader_rt20260830t101500z_4711" {
		t.Fatalf("role name = %q", name)
	}
	for _, r := range name {
		lower := r >= 'a' && r <= 'z'
		digit := r >= '0' && r <= '9'
		if !lower && !digit && r != '_' {
			t.Fatalf("role name %q holds %q, which an unquoted identifier cannot carry", name, r)
		}
	}
}

// TestScratchLedgerDSNTargetsTheScratchContainer pins that the only connection
// string this leg builds names the scratch container's own address and the
// run-scoped role, bracketing the IPv6 literal the production bridge hands out.
func TestScratchLedgerDSNTargetsTheScratchContainer(t *testing.T) {
	address := netip.MustParseAddr("3d06:bad:b01:0:7ac::5")
	const role, throwaway = "tack_drill_reader_rt1", "drill-rt1"

	// Asserted through the parser rather than against a literal string: the
	// parts are what has to be right, and a literal connection string in a
	// source file is what the repo's secret scanners exist to reject.
	parsed, err := url.Parse(scratchLedgerDSN(address, "tack", role, throwaway))
	if err != nil {
		t.Fatalf("parse the drill dsn: %v", err)
	}
	if parsed.Host != "[3d06:bad:b01:0:7ac::5]:5433" {
		t.Fatalf("host = %q, want the scratch container's own address on the ysql port", parsed.Host)
	}
	if parsed.User.Username() != role {
		t.Fatalf("user = %q, want the run-scoped drill role %q", parsed.User.Username(), role)
	}
	if carried, set := parsed.User.Password(); !set || carried != throwaway {
		t.Fatalf("password set = %v, want the run's throwaway password carried", set)
	}
	if parsed.Path != "/tack" {
		t.Fatalf("database path = %q, want /tack", parsed.Path)
	}
	if parsed.Query().Get("sslmode") != "disable" {
		t.Fatalf("query = %q, want sslmode disabled on the throwaway container", parsed.RawQuery)
	}
}
