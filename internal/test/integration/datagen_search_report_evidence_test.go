package integration_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

func requireDatagenSearchReportEvidence(t *testing.T, ledger publicLedger, output []byte, format string) {
	t.Helper()
	document := printedDatagenDocument(t, output, format)
	var result struct {
		SessionVerbs json.RawMessage `json:"session_verbs"`
		Search       struct {
			OrgID uuid.UUID `json:"org_id"`
		} `json:"search"`
	}
	if err := json.Unmarshal(document.Result, &result); err != nil {
		t.Fatalf("decode the returned session report: %v", err)
	}
	operatorID := uuid.MustParse("019dd226-440e-729a-a442-281aaf73ca30")
	var rows []audit.Row
	waitFor(t, "permanent search report evidence", func() bool {
		all, err := ledger.stack.reader.Query(t.Context(), audit.QueryFilter{
			OrgID: audit.SystemOrgID(), Oldest: ledger.since, Latest: time.Now().Add(time.Hour),
			Action: "", ActorID: operatorID, EntityID: uuid.Nil, RequestID: document.Meta.RequestID, TraceID: "", Limit: 100,
		})
		if err != nil {
			t.Fatalf("query permanent search report evidence: %v", err)
		}
		rows = nil
		for _, row := range all {
			if row.ActorID == operatorID && (row.Action == string(audit.VerbOpsDatagenSearch) || row.Context.OperationClass == audit.OperationRead) {
				rows = append(rows, row)
			}
		}
		return len(rows) >= 3
	})
	parent := requireDatagenMutationOutcome(t, rows, audit.OutcomeOK, audit.VerbOpsDatagenSearch)
	count := 0
	for _, row := range rows {
		var extra datagenReportExtra
		if err := json.Unmarshal(row.Extra, &extra); err != nil {
			t.Fatalf("decode session report selection: %v", err)
		}
		if extra.Input.Report != "ops.qa.datagen.search_session_verbs" {
			continue
		}
		if len(extra.Input.OrgIDs) != 1 || extra.Input.OrgIDs[0] != result.Search.OrgID ||
			extra.Input.Wait != "2m0s" || extra.Input.Poll != "2s" || len(extra.Input.Verbs) != sessionVerbCount ||
			row.Context.ParentOperationID != parent || row.Context.OperationID == parent ||
			row.Context.TraceID != document.Meta.TraceID || row.Context.RequestID != document.Meta.RequestID {
			t.Fatalf("session report did not preserve its selection and initiating command: %+v, extra=%+v", row, extra)
		}
		payload := result.SessionVerbs
		requireReadRow(t, row, audit.VerbAuditRead, audit.OutcomeCompleted, "", payload)
		targets := expectedDatagenVerbTargets(t, row, extra, payload)
		digest, parts, err := audit.ReadManifestDigest(targets)
		if err != nil || row.Context.Read.Count != uint64(len(targets)) || row.Context.Read.ManifestSHA256 != digest ||
			row.Context.Read.ManifestParts != parts || !slices.Equal(ledger.manifestTargets(t, row), targets) {
			t.Fatalf("session report manifest differs from returned aggregate identities: %+v, error=%v", row.Context.Read, err)
		}
		count++
		t.Logf("permanent search report event=%s parent=%s operation=%s count=%d sha256=%s", row.EventID, parent, row.Context.OperationID, row.Context.Read.Count, row.Context.Read.PayloadSHA256)
	}
	if count != 1 {
		t.Fatalf("public session report recorded %d read events, want one event for the complete poll", count)
	}
}
