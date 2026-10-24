package integration_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/response"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/internal/testenv/opsoutbox"
)

// previewCommandTarget is the resource URI that the dry run of the metadata
// name index backfill records as its read target.
const previewCommandTarget = "tack:command/" + string(audit.VerbOpsBackfillMetadataNameIndex)

// TestRuntimeAuditGeneratedPreviewRecordsRead runs the dry run of a generated
// command that scans FoundationDB. It requires one completed format 2 read
// event in the operator outbox with the command resource as its target and
// the digest of the JSON result that the command printed.
func TestRuntimeAuditGeneratedPreviewRecordsRead(t *testing.T) {
	ledger := testenv.Ledger(t)
	cfg := &config.Config{DatabaseURL: ledger, FDBClusterFile: testenv.FoundationDB(t), FDBTransactionTimeout: 5 * time.Second}
	pool, err := pgxpool.New(t.Context(), ledger)
	if err != nil {
		t.Fatalf("opening the operator outbox pool failed: %v", err)
	}
	t.Cleanup(pool.Close)
	factory := cli.System(cfg)
	var output bytes.Buffer
	factory.Out, factory.Err = &output, &bytes.Buffer{}
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	factory.SetAuditReads(audit.NewPoolReadLifecycle(pool))
	root := repairCommandRoot(factory)
	operatorID := uuid.Must(uuid.NewV7())
	root.SetArgs([]string{
		"--output", "json", "--operator-id", operatorID.String(),
		"--operator-email", "preview@example.com", "--operator-name", "Preview Test",
		"ops", "backfill", "once-metadata-name-index",
	})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("the metadata name index dry run failed: %v\n%s", err, output.String())
	}
	payload := printedPayload(t, output.Bytes())
	events := opsoutbox.Events(t, pool, opsoutbox.Filter{
		Verb: audit.VerbOpsBackfillMetadataNameIndex, Path: []string{"actor", "id"}, Value: operatorID.String(),
	})
	if len(events) != 1 {
		t.Fatalf("the dry run recorded %d events instead of one read event: %+v", len(events), events)
	}
	event := events[0]
	eventContext := event.Context
	if err := audit.ValidateEvent(event); err != nil {
		t.Fatalf("the dry run read event is not valid: %v", err)
	}
	if eventContext.OperationClass != audit.OperationRead || event.Outcome != audit.OutcomeCompleted ||
		eventContext.Target == nil || eventContext.Target.URI != previewCommandTarget {
		t.Fatalf("the dry run event has context %+v and outcome %s instead of a completed read of %s",
			eventContext, event.Outcome, previewCommandTarget)
	}
	if eventContext.Read == nil || eventContext.Read.PayloadSHA256 != payloadDigest(payload) || eventContext.Read.PayloadFormat != "application/json" {
		t.Fatalf("the dry run read evidence is %+v instead of the JSON payload digest %s", eventContext.Read, payloadDigest(payload))
	}
}

// printedPayload returns the compact result of the JSON document that a dry
// run printed after its preamble.
func printedPayload(t *testing.T, printed []byte) []byte {
	t.Helper()
	start := bytes.IndexByte(printed, '{')
	if start < 0 {
		t.Fatalf("the dry run did not print a JSON document:\n%s", printed)
	}
	var document response.JSONDocument
	if err := json.Unmarshal(printed[start:], &document); err != nil {
		t.Fatalf("decoding the dry run document %q failed: %v", printed, err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, document.Result); err != nil {
		t.Fatalf("compacting the dry run result %s failed: %v", document.Result, err)
	}
	return compact.Bytes()
}
