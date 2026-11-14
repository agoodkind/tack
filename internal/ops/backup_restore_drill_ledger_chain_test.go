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

// cleanLedgerReport creates a report with verified row hashes, links, digest,
// and manifest signature.
func cleanLedgerReport(dir string, rows int) *audit.VerifyReport {
	return &audit.VerifyReport{
		BundleDir:        dir,
		RowsScanned:      rows,
		HashMatches:      rows,
		ChainGapCount:    0,
		ChainBreaks:      nil,
		FileSHA256OK:     true,
		SignatureOK:      true,
		ManifestRowCount: rows,
		ManifestSubject:  "test-bundle",
	}
}

// fixedExport supplies a row count without writing a bundle for verdict tests.
func fixedExport(rows int) drillLedgerExportFunc {
	return func(context.Context, uuid.UUID, string) (int, error) {
		return rows, nil
	}
}

// ledgerOrg supplies an organization with an independent expected row count.
func ledgerOrg(rows int) drillLedgerOrg {
	return drillLedgerOrg{ID: uuid.Must(uuid.NewV7()), RowCount: rows}
}

// TestRestoredLedgerChainFailsOnReportedBreak checks structured break reports
// even when the verifier returns a nil error.
func TestRestoredLedgerChainFailsOnReportedBreak(t *testing.T) {
	org := ledgerOrg(4)
	verify := func(dir string) (*audit.VerifyReport, error) {
		report := cleanLedgerReport(dir, 4)
		report.ChainBreaks = []string{
			"row 019dd226-440e-729a-a442-281aaf73ca30 has previous-hash link mismatch",
		}

		return report, nil
	}

	err := verifyRestoredLedgerChains(
		context.Background(),
		[]drillLedgerOrg{org},
		t.TempDir(),
		fixedExport(4),
		verify,
	)
	if err == nil {
		t.Fatal("the drill accepted a chain that the verifier reported as broken")
	}

	if !strings.Contains(err.Error(), "1 chain break(s)") ||
		!strings.Contains(err.Error(), org.ID.String()) {
		t.Fatalf("error = %v, want the break count and organization ID", err)
	}
}

// TestRestoredLedgerChainPassesOnCleanReport checks successful verified bundles.
func TestRestoredLedgerChainPassesOnCleanReport(t *testing.T) {
	orgs := []drillLedgerOrg{ledgerOrg(3), ledgerOrg(3)}
	verify := func(dir string) (*audit.VerifyReport, error) {
		return cleanLedgerReport(dir, 3), nil
	}

	if err := verifyRestoredLedgerChains(
		context.Background(),
		orgs,
		t.TempDir(),
		fixedExport(3),
		verify,
	); err != nil {
		t.Fatalf("clean bundles failed the drill: %v", err)
	}
}

// TestRestoredLedgerChainFailsOnSequenceGap checks full-ledger continuity.
// Unlike filtered exports, this count-reconciled export cannot intentionally
// omit rows. Missing sequences prevent verification of links across them.
func TestRestoredLedgerChainFailsOnSequenceGap(t *testing.T) {
	org := ledgerOrg(6)
	verify := func(dir string) (*audit.VerifyReport, error) {
		report := cleanLedgerReport(dir, 6)
		report.ChainGapCount = 11

		return report, nil
	}

	err := verifyRestoredLedgerChains(
		context.Background(),
		[]drillLedgerOrg{org},
		t.TempDir(),
		fixedExport(6),
		verify,
	)
	if err == nil {
		t.Fatal("the drill accepted missing interior rows in a full-ledger export")
	}

	if !strings.Contains(err.Error(), "11 sequence gap(s)") ||
		!strings.Contains(err.Error(), org.ID.String()) {
		t.Fatalf("error = %v, want the gap count and organization ID", err)
	}
}

// TestDrillExportRequestsFullLedgerRange checks unlimited export with explicit
// organization and full time bounds. A valid partial bundle cannot establish
// complete restored-ledger integrity.
func TestDrillExportRequestsFullLedgerRange(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	orgID := uuid.Must(uuid.NewV7())
	ledger := &capturedLedgerRead{}

	if _, err := exportRestoredOrgBundle(
		context.Background(),
		ledger,
		privateKey,
		orgID,
		t.TempDir(),
	); err != nil {
		t.Fatalf("export: %v", err)
	}

	if ledger.calls != 1 {
		t.Fatalf("ledger reads = %d, want exactly one", ledger.calls)
	}

	if ledger.filter.Limit != 0 {
		t.Fatalf("export limit = %d, want 0 for complete coverage", ledger.filter.Limit)
	}

	if ledger.filter.OrgID != orgID {
		t.Fatalf("export org = %s, want %s", ledger.filter.OrgID, orgID)
	}

	// A bounded time window would intentionally create sequence gaps.
	if !ledger.filter.Oldest.Equal(drillLedgerOldest) ||
		!ledger.filter.Latest.Equal(drillLedgerLatest) {
		t.Fatalf(
			"export range = [%s, %s), want the whole range [%s, %s)",
			ledger.filter.Oldest,
			ledger.filter.Latest,
			drillLedgerOldest,
			drillLedgerLatest,
		)
	}
}

// capturedLedgerRead records the production export's ledger query filter.
type capturedLedgerRead struct {
	filter audit.QueryFilter
	calls  int
}

func (c *capturedLedgerRead) StreamQuery(
	_ context.Context,
	filter audit.QueryFilter,
	_ audit.RowVisitor,
) error {
	c.filter = filter
	c.calls++

	return nil
}

// TestRestoredLedgerChainFailsOnAnyShortfall checks independent row-count
// reconciliation before verification. Missing shard endpoints can leave valid
// contiguous links but cannot satisfy complete export coverage.
func TestRestoredLedgerChainFailsOnAnyShortfall(t *testing.T) {
	org := ledgerOrg(9)
	verify := func(string) (*audit.VerifyReport, error) {
		t.Fatal("the verifier ran on an incomplete organization's bundle")
		return nil, nil
	}

	err := verifyRestoredLedgerChains(
		context.Background(),
		[]drillLedgerOrg{org},
		t.TempDir(),
		fixedExport(7),
		verify,
	)
	if err == nil {
		t.Fatal("the drill accepted an export missing restored ledger rows")
	}

	if !strings.Contains(err.Error(), "wrote 7 of the 9 rows") {
		t.Fatalf("error = %v, want the export coverage counts", err)
	}
}

// TestRestoredLedgerChainFailsOnEmptyLedger checks refusal of an empty restore.
// Operator and export operations require recorded ledger rows.
func TestRestoredLedgerChainFailsOnEmptyLedger(t *testing.T) {
	err := verifyRestoredLedgerChains(
		context.Background(),
		nil,
		t.TempDir(),
		fixedExport(0),
		func(string) (*audit.VerifyReport, error) {
			t.Fatal("the verifier ran without any ledger organizations")
			return nil, nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "does not contain rows") {
		t.Fatalf("error = %v, want an empty-ledger failure", err)
	}
}

// TestRestoredLedgerChainFailsWhenVerifyCannotRun checks verification failure
// instead of silently skipping an unavailable verifier.
func TestRestoredLedgerChainFailsWhenVerifyCannotRun(t *testing.T) {
	verify := func(string) (*audit.VerifyReport, error) {
		return nil, errors.New("bundle manifest unreadable")
	}

	err := verifyRestoredLedgerChains(
		context.Background(),
		[]drillLedgerOrg{ledgerOrg(2)},
		t.TempDir(),
		fixedExport(2),
		verify,
	)
	if err == nil || !strings.Contains(err.Error(), "bundle manifest unreadable") {
		t.Fatalf("error = %v, want verifier failure to fail the drill", err)
	}
}

// TestRestoredLedgerChainFailsWhenExportFails checks export failure without
// attempting verification of an unwritten bundle.
func TestRestoredLedgerChainFailsWhenExportFails(t *testing.T) {
	export := func(context.Context, uuid.UUID, string) (int, error) {
		return 0, errors.New("ledger unreachable")
	}
	verify := func(string) (*audit.VerifyReport, error) {
		t.Fatal("the verifier ran without an exported bundle")
		return nil, nil
	}

	err := verifyRestoredLedgerChains(
		context.Background(),
		[]drillLedgerOrg{ledgerOrg(2)},
		t.TempDir(),
		export,
		verify,
	)
	if err == nil || !strings.Contains(err.Error(), "ledger unreachable") {
		t.Fatalf("error = %v, want export failure to fail the drill", err)
	}
}

// TestRestoredLedgerChainRejectsTamperedSignedBundle uses the production
// verifier on a validly signed bundle with invalid stored row hashes. Replacing
// chain verification with row-count checks would incorrectly accept it.
func TestRestoredLedgerChainRejectsTamperedSignedBundle(t *testing.T) {
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
		context.Background(),
		[]drillLedgerOrg{org},
		t.TempDir(),
		export,
		verify,
	)
	if err == nil {
		t.Fatal("the drill accepted a bundle that failed real chain verification")
	}

	if !strings.Contains(err.Error(), "chain break(s)") {
		t.Fatalf("error = %v, want reported chain breaks as the reason", err)
	}
}

// writeUnhashableBundle signs a valid manifest and events digest for two rows
// with incorrect stored hashes. Only row and chain integrity checks can reject it.
func writeUnhashableBundle(
	t *testing.T,
	dir string,
	orgID uuid.UUID,
	signer ed25519.PrivateKey,
) int {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create bundle dir: %v", err)
	}

	stamp := time.Now().UTC().Truncate(time.Microsecond)
	var jsonl []byte

	for seq := int64(1); seq <= 2; seq++ {
		row := audit.Row{
			OrgID:       orgID,
			EventTime:   stamp,
			EventID:     uuid.Must(uuid.NewV7()),
			Seq:         seq,
			Shard:       1,
			ActorID:     uuid.Must(uuid.NewV7()),
			ActorKind:   5,
			Action:      "ops.test.restore_drill",
			Outcome:     audit.OutcomeOK,
			EntityKind:  "system",
			EntityID:    orgID,
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

// writeBundleManifest uses export's signing format to isolate chain defects
// from digest and signature failures.
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
		ExportID:    uuid.Must(uuid.NewV7()),
		OrgID:       orgID,
		Oldest:      drillLedgerOldest,
		Latest:      drillLedgerLatest,
		RowCount:    rowCount,
		FileSHA256:  hex.EncodeToString(digest[:]),
		SignatureBy: drillLedgerSigningKeyID,
		Signature:   "",
	}
	signable, err := json.Marshal(
		struct {
			ExportID    uuid.UUID `json:"export_id"`
			OrgID       uuid.UUID `json:"org_id"`
			Oldest      time.Time `json:"oldest"`
			Latest      time.Time `json:"latest"`
			RowCount    int       `json:"row_count"`
			FileSHA256  string    `json:"file_sha256"`
			SignatureBy string    `json:"signing_key_id"`
		}{
			manifest.ExportID,
			manifest.OrgID,
			manifest.Oldest,
			manifest.Latest,
			manifest.RowCount,
			manifest.FileSHA256,
			manifest.SignatureBy,
		},
	)
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

// TestRestoredLedgerOrgParsesRowCount checks exact organization ID and count
// parsing for export coverage reconciliation.
func TestRestoredLedgerOrgParsesRowCount(t *testing.T) {
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

// TestRestoredLedgerOrgRejectsInvalidRowCount checks rejection rather than
// silently excluding an organization's chain after an unreadable count.
func TestRestoredLedgerOrgRejectsInvalidRowCount(t *testing.T) {
	ctx := context.Background()
	if _, err := parseRestoredLedgerOrg(ctx, "019dd226-440e-729a-a442-281aaf73ca30"); err == nil {
		t.Fatal("a missing row count must fail organization parsing")
	}

	if _, err := parseRestoredLedgerOrg(
		ctx,
		"019dd226-440e-729a-a442-281aaf73ca30|many",
	); err == nil {
		t.Fatal("an unreadable row count must fail organization parsing")
	}
}

// TestDrillLedgerRoleNameIsAnUnquotedIdentifier checks exact normalization and
// SQL identifier characters for timestamp-and-PID run IDs.
func TestDrillLedgerRoleNameIsAnUnquotedIdentifier(t *testing.T) {
	name := drillLedgerRoleName("rt20260830T101500Z-4711")
	if name != "tack_drill_reader_rt20260830t101500z_4711" {
		t.Fatalf("role name = %q", name)
	}

	for _, r := range name {
		lower := r >= 'a' && r <= 'z'
		digit := r >= '0' && r <= '9'
		if !lower && !digit && r != '_' {
			t.Fatalf("role name %q contains invalid unquoted-identifier character %q", name, r)
		}
	}
}

// TestScratchLedgerDSNTargetsTheScratchContainer checks parsed scratch address,
// IPv6 brackets, run-specific role, temporary password, database, and TLS mode.
func TestScratchLedgerDSNTargetsTheScratchContainer(t *testing.T) {
	address := netip.MustParseAddr("3d06:bad:b01:0:7ac::5")
	const role, throwaway = "tack_drill_reader_rt1", "drill-rt1"

	// Parsing avoids a literal credential-bearing connection string in source.
	parsed, err := url.Parse(scratchLedgerDSN(address, "tack", role, throwaway))
	if err != nil {
		t.Fatalf("parse the drill dsn: %v", err)
	}

	if parsed.Host != "[3d06:bad:b01:0:7ac::5]:5433" {
		t.Fatalf(
			"host = %q, want the scratch container address on the YSQL port",
			parsed.Host,
		)
	}

	if parsed.User.Username() != role {
		t.Fatalf("user = %q, want the run-specific drill role %q", parsed.User.Username(), role)
	}

	if carried, set := parsed.User.Password(); !set || carried != throwaway {
		t.Fatalf("password set = %v, want the run's temporary password", set)
	}

	if parsed.Path != "/tack" {
		t.Fatalf("database path = %q, want /tack", parsed.Path)
	}

	if parsed.Query().Get("sslmode") != "disable" {
		t.Fatalf("query = %q, want sslmode disabled for the scratch container", parsed.RawQuery)
	}
}
