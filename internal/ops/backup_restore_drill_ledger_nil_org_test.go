// The nil-organization tests verify historical ledger rows from before every
// event recorded an organization. The reader rejects a nil organization unless
// the filter explicitly selects it. An earlier drill used the tenant filter
// for these rows, left their chain unverified, and failed on that refusal.

package ops

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestRestoredLedgerChainVerifiesTheNilOrgChain reproduces the earlier
// nil-organization refusal. Intact tenant and nil-organization chains must both
// pass export, digest, signature, and row-hash verification. The reader must
// not reject the nil organization as an absent organization.
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

// TestRestoredLedgerChainFailsOnABreakInTheNilOrgChain requires verification
// of the exported nil-organization rows. An incorrect stored row hash must fail
// the drill. The error must identify the nil organization and must not identify
// the intact tenant chain.
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

// TestDrillExportNamesTheNilOrgOnPurpose checks each organization's filter.
// NilOrg must apply only to the nil organization. The reader must still reject
// accidentally omitted organization IDs in other exports.
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
