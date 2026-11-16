// Nil-org ledger rows predate mandatory organization IDs. The reader rejects
// nil orgs by default, but restore verification must explicitly export and
// verify this historical chain as well as tenant chains.

package ops

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestRestoredLedgerChainVerifiesTheNilOrgChain checks complete export, digest,
// signature, and row-hash verification for tenant and nil-org chains.
func TestRestoredLedgerChainVerifiesTheNilOrgChain(t *testing.T) {
	tenant := uuid.Must(uuid.NewV7())
	ledger := twoOrgLedger(t, tenant)

	reports, verdict := runChainLegOver(t, ledger)
	if verdict != nil {
		t.Fatalf("a restored ledger with intact tenant and nil-org chains failed: %v", verdict)
	}

	for _, orgID := range []uuid.UUID{tenant, uuid.Nil} {
		if ledger.served[orgID] != len(ledger.rowsByOrg[orgID]) {
			t.Fatalf(
				"org %s: %d of %d rows exported",
				orgID,
				ledger.served[orgID],
				len(ledger.rowsByOrg[orgID]),
			)
		}
	}

	if len(reports) != 2 {
		t.Fatalf("verifier ran %d times, want once per org", len(reports))
	}

	scanned := 0

	for _, report := range reports {
		if !report.SignatureOK || !report.FileSHA256OK || report.HashMatches != report.RowsScanned {
			t.Fatalf("a bundle passed without complete verification: %+v", report)
		}

		scanned += report.RowsScanned
	}

	if want := len(ledger.rowsByOrg[tenant]) + len(ledger.rowsByOrg[uuid.Nil]); scanned != want {
		t.Fatalf("rows verified = %d, want every row of both orgs (%d)", scanned, want)
	}
}

// TestRestoredLedgerChainFailsOnABreakInTheNilOrgChain checks that a corrupt
// nil-org row fails verification and identifies only the broken chain's org.
func TestRestoredLedgerChainFailsOnABreakInTheNilOrgChain(t *testing.T) {
	tenant := uuid.Must(uuid.NewV7())
	ledger := twoOrgLedger(t, tenant)
	ledger.rowsByOrg[uuid.Nil][2].RowHash = []byte("this is not the hash of this row")

	_, verdict := runChainLegOver(t, ledger)
	if verdict == nil {
		t.Fatal("the drill accepted a broken nil-org chain")
	}

	if !strings.Contains(verdict.Error(), "chain break(s)") {
		t.Fatalf("error = %v, want the chain break as the reason", verdict)
	}

	if !strings.Contains(verdict.Error(), uuid.Nil.String()) {
		t.Fatalf("error = %v, want the nil org identifier", verdict)
	}

	if strings.Contains(verdict.Error(), tenant.String()) {
		t.Fatalf("error = %v, the intact tenant chain must not appear", verdict)
	}
}

// TestDrillExportEnablesNilOrgOnlyForNilIdentity checks explicit nil-org
// permission while preserving the reader's omitted-org guard for tenant exports.
func TestDrillExportEnablesNilOrgOnlyForNilIdentity(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	for _, orgID := range []uuid.UUID{uuid.Nil, uuid.Must(uuid.NewV7())} {
		ledger := &capturedLedgerRead{}
		if _, err := exportRestoredOrgBundle(
			context.Background(),
			ledger,
			privateKey,
			orgID,
			t.TempDir(),
		); err != nil {
			t.Fatalf("export org %s: %v", orgID, err)
		}

		if ledger.filter.OrgID != orgID {
			t.Fatalf("export org = %s, want %s", ledger.filter.OrgID, orgID)
		}

		if ledger.filter.NilOrg != (orgID == uuid.Nil) {
			t.Fatalf(
				"org %s: nil_org = %v, want it set exactly for the nil org",
				orgID,
				ledger.filter.NilOrg,
			)
		}
	}
}
