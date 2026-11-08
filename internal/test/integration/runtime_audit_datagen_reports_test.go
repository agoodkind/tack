//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// withDatagenReportCoverage requires nested report evidence in addition to
// the mutation command evidence that the runtime inventory already checks.
func withDatagenReportCoverage(base coverageCase, search bool) coverageCase {
	base.check = func(t *testing.T, env *coverageEnv, rows []audit.Row) {
		t.Helper()
		if search && !env.searchEnabled {
			return
		}
		want := 6
		if search {
			want = 1
		}
		var reports []audit.Row
		waitFor(t, "nested generated-data runtime report evidence", func() bool {
			all, err := env.ledger.stack.reader.Query(t.Context(), audit.QueryFilter{
				OrgID: audit.SystemOrgID(), Oldest: env.ledger.since, Latest: time.Now().Add(time.Hour),
				Action: "", ActorID: rows[0].ActorID, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: 20,
			})
			if err != nil {
				t.Fatalf("query generated-data runtime report evidence: %v", err)
			}
			reports = nil
			for _, row := range all {
				if row.Context.OperationClass != audit.OperationRead {
					continue
				}
				var extra datagenReportExtra
				if err := json.Unmarshal(row.Extra, &extra); err != nil {
					t.Fatalf("decode runtime report selection: %v", err)
				}
				if extra.Input.Report != "" {
					reports = append(reports, row)
				}
			}
			return len(reports) >= want
		})
		if len(reports) != want {
			t.Fatalf("nested runtime report events = %d, want %d", len(reports), want)
		}
		for _, report := range reports {
			if report.Outcome != audit.OutcomeCompleted || report.Context.OperationClass != audit.OperationRead ||
				report.Context.ParentOperationID == uuid.Nil || report.Context.Target == nil || report.Context.Read == nil ||
				report.Context.Read.Count != uint64(len(env.ledger.manifestTargets(t, report))) ||
				report.Context.Read.PayloadSHA256 == "" || report.Context.Read.PayloadFormat != "application/json" {
				t.Fatalf("runtime report did not record its aggregate result: %+v", report)
			}
		}
	}
	return base
}
