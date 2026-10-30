package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

// TestOperatorReadDLQSummary records exact summary-group identities and the
// printed payload digest without returning individual event payloads.
func TestOperatorReadDLQSummary(t *testing.T) {
	pool := ledgerAdminPool(t)
	topic := "operator-summary-" + uuid.NewString()
	first := topic + " first failure"
	second := topic + " second failure"
	statement := readTestSQL(t, "legacy_audit/dead_letter_insert.sql")
	for index, failure := range []string{first, first, second} {
		if _, err := pool.Exec(t.Context(), statement, topic, int32(0), int64(index), []byte{}, failure); err != nil {
			t.Fatalf("store synthetic dead-letter summary input: %v", err)
		}
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.WithoutCancel(t.Context()), readTestSQL(t, "operator_read_dlq_cleanup.sql"), topic); err != nil {
			t.Errorf("delete synthetic summary input: %v", err)
		}
	})
	cfg := &config.Config{DatabaseURL: testenv.Ledger(t), AuditReaderDSN: testenv.Ledger(t)}
	operatorID := uuid.Must(uuid.NewV7())
	root, output := searchReadCommand(t, cfg, pool, operatorID, "ops", "audit", "dlq", "inspect")
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("inspect the dead-letter summary: %v\n%s", err, output.String())
	}
	payload := printedPayload(t, output.Bytes())
	var report struct {
		Command string `json:"command"`
		Rows    int    `json:"rows"`
		ByError []struct {
			Error  string `json:"error"`
			Count  int    `json:"count"`
			Oldest string `json:"oldest"`
			Newest string `json:"newest"`
		} `json:"by_error"`
	}
	if err := json.Unmarshal(payload, &report); err != nil {
		t.Fatalf("decode the summary result: %v", err)
	}
	var targets []audit.Target
	var total int
	found := make(map[string]int)
	for _, group := range report.ByError {
		total += group.Count
		found[group.Error] = group.Count
		digest := sha256.Sum256([]byte(group.Error))
		targets = append(targets, audit.Target{
			Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: "",
			URI:          "tack:audit/dlq/summary/" + hex.EncodeToString(digest[:]),
			RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
		})
		if group.Oldest == "" || group.Newest == "" {
			t.Fatalf("summary omitted the group time bounds: %+v", group)
		}
	}
	if report.Command != "ops.audit.dlq.inspect" || total != report.Rows || found[first] != 2 || found[second] != 1 {
		t.Fatalf("summary differs from the stored groups: %+v", report)
	}
	event := requireSearchReadEvent(t, pool, operatorID, audit.VerbOpsAuditDLQInspect, "audit/dlq/summary")
	requireSearchReadPayload(t, event, payload, targets)
	pipeline := newPartsPipeline(t)
	pipeline.startConsumer(t)
	pipeline.startRelay(t, nil)
	row := pipeline.stack().waitForRow(t, event.EventID)
	ledger := publicLedger{stack: pipeline.stack(), since: clock.Now().Add(-time.Hour)}
	if row.Outcome != audit.OutcomeCompleted || row.Context.Read == nil || *row.Context.Read != *event.Context.Read ||
		!slices.Equal(ledger.manifestTargets(t, row), targets) {
		t.Fatalf("the permanent summary evidence differs from the printed result: %+v", row)
	}
	t.Logf("ledger event=%s operation=%s verb=%s outcome=%s count=%d sha256=%s",
		row.EventID, row.Context.OperationID, row.Action, row.Outcome, row.Context.Read.Count, row.Context.Read.PayloadSHA256)
}
