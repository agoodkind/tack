// backup_restore_drill_ledger_nil_org_fixture_test.go is the restored ledger
// the nil-org chain tests drive the leg over: rows under a tenant org and the
// nil org, chained and hashed the way the writer left them, plus the leg run
// that exports and verifies them.

package ops

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
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
