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
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// cleanLedgerReport returns a successful bundle report. Every scanned row
// matches its hash, the chain has neither gaps nor broken links, and the
// manifest digest and signature pass verification.
func cleanLedgerReport(dir string, rows int) *audit.VerifyReport {
	return &audit.VerifyReport{
		BundleDir: dir, RowsScanned: rows, HashMatches: rows, ChainGapCount: 0,
		ChainBreaks: nil, FileSHA256OK: true, SignatureOK: true,
		ManifestRowCount: rows, ManifestSubject: "test-bundle",
	}
}

// fixedExport replaces the database export in tests. It returns the supplied
// row count without writing a bundle. Tests can select a verification outcome
// without restoring a ledger.
func fixedExport(rows int) drillLedgerExportFunc {
	return func(context.Context, uuid.UUID, string) (int, error) {
		return rows, nil
	}
}

// ledgerOrg returns an organization with the supplied restored row count.
// The drill compares that count with the export's row count. Tests of complete
// exports supply the same count to both helpers.
func ledgerOrg(rows int) drillLedgerOrg {
	return drillLedgerOrg{ID: uuid.Must(uuid.NewV7()), RowCount: rows}
}

// TestRestoredLedgerChainFailsOnReportedBreak requires a reported chain break
// to fail the drill. The drill checks the report even when the verifier returns
// without an error.
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

// TestRestoredLedgerChainPassesOnCleanReport requires intact verified bundles
// to pass the drill. A false failure would make the check unreliable.
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

// TestRestoredLedgerChainFailsOnSequenceGap requires a sequence gap to fail
// the drill. The export requests every row and reconciles its count with the
// restored ledger. A gap prevents comparison of prev_hash across the missing
// sequence numbers. The drill cannot verify that part of the chain.
//
// A time window can legitimately omit rows from a bundle. This drill requests
// the full configured range and does not accept such gaps.
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

// TestDrillExportAsksTheLedgerForEveryRow inspects the filter from the drill's
// production export. A fixed row cap would need increases as the ledger grows.
// Until that adjustment, the drill could verify only the exported subset and
// could not establish a result for larger organizations.
func TestDrillExportAsksTheLedgerForEveryRow(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	orgID := uuid.Must(uuid.NewV7())
	ledger := &capturedLedgerRead{}

	if _, err := exportRestoredOrgBundle(
		context.Background(), ledger, privateKey, orgID, t.TempDir()); err != nil {
		t.Fatalf("export: %v", err)
	}

	if ledger.calls != 1 {
		t.Fatalf("ledger reads = %d, want exactly one", ledger.calls)
	}
	if ledger.filter.Limit != 0 {
		t.Fatalf("export limit = %d, want 0 so the bundle covers every row", ledger.filter.Limit)
	}
	if ledger.filter.OrgID != orgID {
		t.Fatalf("export org = %s, want %s", ledger.filter.OrgID, orgID)
	}
	// The range sentinels also require coverage. A time window can omit interior
	// chain rows, and the drill fails when the verifier reports the resulting gaps.
	if !ledger.filter.Oldest.Equal(drillLedgerOldest) || !ledger.filter.Latest.Equal(drillLedgerLatest) {
		t.Fatalf("export range = [%s, %s), want the whole range [%s, %s)",
			ledger.filter.Oldest, ledger.filter.Latest, drillLedgerOldest, drillLedgerLatest)
	}
}

// capturedLedgerRead replaces the restored ledger and records the drill's
// requested filter.
type capturedLedgerRead struct {
	filter audit.QueryFilter
	calls  int
}

func (c *capturedLedgerRead) StreamQuery(
	_ context.Context, filter audit.QueryFilter, _ audit.RowVisitor,
) error {
	c.filter = filter
	c.calls++
	return nil
}

// TestRestoredLedgerChainFailsOnAnyShortfall compares the export count with
// the restored ledger's count. Omitting a shard's first or last rows can leave
// the remaining rows contiguous and correctly linked. A chain check alone
// cannot detect that shortfall.
//
// A stopped export, a streaming read that omits a boundary row, or a role that
// cannot read all counted rows must fail this reconciliation.
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

// TestRestoredLedgerChainFailsOnEmptyLedger requires an empty restored ledger
// to fail. Every operator command must record audit evidence, including the
// export command. An exported ledger should therefore contain rows. An empty
// restore cannot establish chain integrity and must not pass by skipping the
// check.
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

// TestRestoredLedgerChainFailsWhenVerifyCannotRun requires a verifier error
// to fail the drill. Logging the error and skipping verification must not pass.
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

// TestRestoredLedgerChainFailsWhenExportFails requires an export error to
// fail the drill. An absent bundle cannot establish a verification result.
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

// TestRestoredLedgerChainFailsOnTamperedBundleThroughTheRealVerifier invokes
// audit.VerifyBundle on rows with incorrect stored hashes. The production
// verifier supplies the break report. A drill that checked only row counts
// would miss this corruption, even if the report-based tests passed.
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

// writeUnhashableBundle writes two rows with stored hashes that do not match
// their contents. The events digest and manifest signature remain valid.
// Verification must detect the invalid row hashes through the chain checks.
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

// writeBundleManifest signs the manifest with the export's signable fields.
// The digest and signature checks can pass independently of the row hashes
// that the chain verification checks.
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

// TestRestoredLedgerOrgCarriesItsRowCount checks the row-count input format.
// Every export uses this count for reconciliation. A parsing error could reject
// an organization or compare its export with a count the ledger did not return.
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

// TestRestoredLedgerOrgWithoutARowCountIsAnError requires an absent or
// unreadable row count to fail parsing. Skipping that organization would leave
// its chain unverified while allowing the drill to pass.
func TestRestoredLedgerOrgWithoutARowCountIsAnError(t *testing.T) {
	ctx := context.Background()
	if _, err := parseRestoredLedgerOrg(ctx, "019dd226-440e-729a-a442-281aaf73ca30"); err == nil {
		t.Fatal("a line carrying no row count parsed as an org")
	}
	if _, err := parseRestoredLedgerOrg(ctx, "019dd226-440e-729a-a442-281aaf73ca30|many"); err == nil {
		t.Fatal("a line carrying an unreadable row count parsed as an org")
	}
}

// TestDrillLedgerRoleNameIsAnUnquotedIdentifier checks the role name that
// CREATE ROLE interpolates. Run IDs include a timestamp and process ID. A
// hyphen would make the unquoted identifier invalid, and uppercase letters
// would fold to lowercase and could disagree with the connection string.
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

// TestScratchLedgerDSNTargetsTheScratchContainer checks that the connection
// string uses the scratch address and run-specific role. The IPv6 address
// requires brackets, as addresses from the production bridge do.
func TestScratchLedgerDSNTargetsTheScratchContainer(t *testing.T) {
	address := netip.MustParseAddr("3d06:bad:b01:0:7ac::5")
	const role, throwaway = "tack_drill_reader_rt1", "drill-rt1"

	// The URL parser checks each connection field without storing a complete
	// connection string literal in the source. Repository secret scanners reject
	// such literals.
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
