// backup_restore_drill_ledger_nil_org_test.go covers the chain the restored
// ledger holds under the nil org: rows recorded before every event carried an
// org. The reader refuses a nil org by default, so a drill that asked for it
// the way it asks for a tenant org left that chain unverified and failed the
// run on the refusal.

package ops

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// restoredLedger stands in for a ledger the drill restored: rows held under
// more than one org, handed over one org at a time. It refuses the filters the
// ledger's own reader refuses, through the reader's own rule, so a drill that
// asks for the nil org the way it asks for a tenant org fails here the way it
// failed on the restored database.
type restoredLedger struct {
	rowsByOrg map[uuid.UUID][]audit.Row
	served    map[uuid.UUID]int
}

func (l *restoredLedger) StreamQuery(
	_ context.Context, filter audit.QueryFilter, visit audit.RowVisitor,
) error {
	if err := filter.Validate(); err != nil {
		return fmt.Errorf("restored ledger: %w", err)
	}
	for _, row := range l.rowsByOrg[filter.OrgID] {
		if err := visit(row); err != nil {
			return fmt.Errorf("restored ledger row %s: %w", row.EventID, err)
		}
		l.served[filter.OrgID]++
	}
	return nil
}

// orgs lists what the ledger holds the way the drill reads it from the
// database: every org with rows, the nil org included, with its row count.
func (l *restoredLedger) orgs() []drillLedgerOrg {
	var orgs []drillLedgerOrg
	for orgID, rows := range l.rowsByOrg {
		orgs = append(orgs, drillLedgerOrg{ID: orgID, RowCount: len(rows)})
	}
	return orgs
}

// chainedRows builds perShard correctly linked rows on each of shards chains
// for one org, hashed the way the writer hashed them so the real verifier
// accepts every one.
func chainedRows(t *testing.T, orgID uuid.UUID, shards, perShard int) []audit.Row {
	t.Helper()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	rows := make([]audit.Row, 0, shards*perShard)
	for shard := range shards {
		var prevHash []byte
		for seq := 1; seq <= perShard; seq++ {
			row := audit.Row{
				OrgID:      orgID,
				EventTime:  base.Add(time.Duration(shard*perShard+seq) * time.Millisecond),
				EventID:    uuid.Must(uuid.NewV7()),
				Seq:        int64(seq),
				Shard:      int16(shard),
				ActorID:    uuid.Must(uuid.NewV7()),
				ActorKind:  1,
				Action:     "auth.token_used",
				Outcome:    audit.OutcomeOK,
				EntityKind: "user", EntityID: uuid.Must(uuid.NewV7()),
				Context:     audit.EventContext{OrgID: orgID, Source: audit.SourceMCP},
				Delta:       nil,
				Error:       nil,
				Extra:       nil,
				PIIRef:      nil,
				PrevHash:    prevHash,
				RowHash:     nil,
				HashVersion: 3,
			}
			hash, err := audit.ComputeRowHash(row)
			if err != nil {
				t.Fatalf("hash row: %v", err)
			}
			row.RowHash = hash
			prevHash = hash
			rows = append(rows, row)
		}
	}
	return rows
}

// twoOrgLedger is the restored shape the drill met: one tenant org's chains
// beside the nil org's chain, both with rows the verifier can walk.
func twoOrgLedger(t *testing.T, tenant uuid.UUID) *restoredLedger {
	t.Helper()
	return &restoredLedger{
		rowsByOrg: map[uuid.UUID][]audit.Row{
			tenant:   chainedRows(t, tenant, 2, 3),
			uuid.Nil: chainedRows(t, uuid.Nil, 1, 4),
		},
		served: map[uuid.UUID]int{},
	}
}

// runChainLegOver drives the real chain leg over the ledger: the drill's own
// export, the throwaway signing key, and the production verifier. It returns
// every report the verifier produced and the leg's verdict.
func runChainLegOver(t *testing.T, ledger *restoredLedger) ([]*audit.VerifyReport, error) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	export := func(ctx context.Context, orgID uuid.UUID, dir string) (int, error) {
		return exportRestoredOrgBundle(ctx, ledger, privateKey, orgID, dir)
	}
	var reports []*audit.VerifyReport
	verify := func(dir string) (*audit.VerifyReport, error) {
		report, verifyErr := audit.VerifyBundle(dir, publicKey)
		if report != nil {
			reports = append(reports, report)
		}
		return report, verifyErr
	}
	verdict := verifyRestoredLedgerChains(context.Background(), ledger.orgs(), t.TempDir(), export, verify)
	return reports, verdict
}

// TestRestoredLedgerChainVerifiesTheNilOrgChain is the defect: a restored
// ledger holding a tenant org and the nil org must pass when both chains are
// intact, with the nil org's rows exported, digested, signed, and re-hashed
// with the same rigor as the tenant's rather than refused as a missing org.
func TestRestoredLedgerChainVerifiesTheNilOrgChain(t *testing.T) {
	tenant := uuid.Must(uuid.NewV7())
	ledger := twoOrgLedger(t, tenant)

	reports, verdict := runChainLegOver(t, ledger)
	if verdict != nil {
		t.Fatalf("a restored ledger with intact chains under a tenant and the nil org failed: %v", verdict)
	}
	for _, orgID := range []uuid.UUID{tenant, uuid.Nil} {
		if ledger.served[orgID] != len(ledger.rowsByOrg[orgID]) {
			t.Fatalf("org %s: %d of %d rows exported", orgID, ledger.served[orgID], len(ledger.rowsByOrg[orgID]))
		}
	}
	if len(reports) != 2 {
		t.Fatalf("verifier ran %d times, want once per org", len(reports))
	}
	scanned := 0
	for _, report := range reports {
		if !report.SignatureOK || !report.FileSHA256OK || report.HashMatches != report.RowsScanned {
			t.Fatalf("a bundle passed the leg without verifying: %+v", report)
		}
		scanned += report.RowsScanned
	}
	if want := len(ledger.rowsByOrg[tenant]) + len(ledger.rowsByOrg[uuid.Nil]); scanned != want {
		t.Fatalf("rows verified = %d, want every row of both orgs (%d)", scanned, want)
	}
}

// TestRestoredLedgerChainFailsOnABreakInTheNilOrgChain pins that the nil org's
// chain is judged, not merely exported: a row under it whose stored hash does
// not recompute fails the leg, and the failure names the nil org and no other.
func TestRestoredLedgerChainFailsOnABreakInTheNilOrgChain(t *testing.T) {
	tenant := uuid.Must(uuid.NewV7())
	ledger := twoOrgLedger(t, tenant)
	ledger.rowsByOrg[uuid.Nil][2].RowHash = []byte("this is not the hash of this row")

	_, verdict := runChainLegOver(t, ledger)
	if verdict == nil {
		t.Fatal("the drill passed a restored ledger whose nil-org chain is broken")
	}
	if !strings.Contains(verdict.Error(), "chain break(s)") {
		t.Fatalf("error = %v, want the chain break to be the reason", verdict)
	}
	if !strings.Contains(verdict.Error(), uuid.Nil.String()) {
		t.Fatalf("error = %v, want it to name the nil org", verdict)
	}
	if strings.Contains(verdict.Error(), tenant.String()) {
		t.Fatalf("error = %v, want the intact tenant chain left out of the failure", verdict)
	}
}

// TestDrillExportNamesTheNilOrgOnPurpose pins the filter the drill hands the
// ledger for each org it lists: the nil org is named as such, and a tenant org
// is not, so the reader's guard against a forgotten org still applies to every
// other export.
func TestDrillExportNamesTheNilOrgOnPurpose(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	for _, orgID := range []uuid.UUID{uuid.Nil, uuid.Must(uuid.NewV7())} {
		ledger := &capturedLedgerRead{}
		if _, err := exportRestoredOrgBundle(
			context.Background(), ledger, privateKey, orgID, t.TempDir()); err != nil {
			t.Fatalf("export org %s: %v", orgID, err)
		}
		if ledger.filter.OrgID != orgID {
			t.Fatalf("export org = %s, want %s", ledger.filter.OrgID, orgID)
		}
		if ledger.filter.NilOrg != (orgID == uuid.Nil) {
			t.Fatalf("org %s: nil_org = %v, want it set exactly for the nil org",
				orgID, ledger.filter.NilOrg)
		}
	}
}
