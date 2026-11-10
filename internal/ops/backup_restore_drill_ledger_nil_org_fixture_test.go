// The nil-organization fixtures provide tenant and nil-organization rows with
// writer-compatible hashes and chain links. The helpers invoke the drill's
// export and verification operations on those rows.

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

// restoredLedger supplies rows for multiple organizations, one organization
// per query. It uses the production filter validator. A nil-organization query
// without explicit selection fails as it did in the restored-database defect.
type restoredLedger struct {
	rowsByOrg map[uuid.UUID][]audit.Row
	served    map[uuid.UUID]int
}

func (l *restoredLedger) StreamQuery(
	_ context.Context,
	filter audit.QueryFilter,
	visit audit.RowVisitor,
) error {
	if err := filter.Validate(); err != nil {
		return fmt.Errorf("restored ledger: %w", err)
	}

	for _, row := range l.rowsByOrg[filter.OrgID] {
		if !restoredLedgerRowMatches(filter, row) {
			continue
		}

		if err := visit(row); err != nil {
			return fmt.Errorf("restored ledger row %s: %w", row.EventID, err)
		}

		l.served[filter.OrgID]++
	}

	return nil
}

// restoredLedgerRowMatches applies the action, actor, and time restrictions
// from the ledger query. A request for one actor's actions within a time window
// receives only matching rows. The drill's full-range filters match all fixture
// rows.
func restoredLedgerRowMatches(filter audit.QueryFilter, row audit.Row) bool {
	if filter.Action != "" && row.Action != filter.Action {
		return false
	}

	if filter.ActorID != uuid.Nil && row.ActorID != filter.ActorID {
		return false
	}

	return !row.EventTime.Before(filter.Oldest) && row.EventTime.Before(filter.Latest)
}

// orgs returns every fixture organization, including the nil organization,
// with its row count, matching the drill's database query.
func (l *restoredLedger) orgs() []drillLedgerOrg {
	var orgs []drillLedgerOrg

	for orgID, rows := range l.rowsByOrg {
		orgs = append(orgs, drillLedgerOrg{ID: orgID, RowCount: len(rows)})
	}

	return orgs
}

// chainedRows creates perShard linked rows for each of shards chains in one
// organization. It computes writer-compatible hashes that the production
// verifier accepts.
func chainedRows(t *testing.T, orgID uuid.UUID, shards, perShard int) []audit.Row {
	t.Helper()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)

	rows := make([]audit.Row, 0, shards*perShard)
	for shard := range shards {
		var prevHash []byte

		for seq := 1; seq <= perShard; seq++ {
			row := audit.Row{
				OrgID:       orgID,
				EventTime:   base.Add(time.Duration(shard*perShard+seq) * time.Millisecond),
				EventID:     uuid.Must(uuid.NewV7()),
				Seq:         int64(seq),
				Shard:       int16(shard),
				ActorID:     uuid.Must(uuid.NewV7()),
				ActorKind:   1,
				Action:      "auth.token_used",
				Outcome:     audit.OutcomeOK,
				EntityKind:  "user",
				EntityID:    uuid.Must(uuid.NewV7()),
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

// twoOrgLedger recreates the restored-ledger defect's organization layout.
// One tenant organization and the nil organization both contain verifiable
// chains.
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

// runChainLegOver invokes the drill's export with a temporary signing key and
// the production verifier. It returns every verification report and the drill's
// chain-verification result.
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
	verdict := verifyRestoredLedgerChains(
		context.Background(),
		ledger.orgs(),
		t.TempDir(),
		export,
		verify,
	)

	return reports, verdict
}
