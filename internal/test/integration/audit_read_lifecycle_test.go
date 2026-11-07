package integration_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

const (
	readLifecycleJSONFormat = "application/json"
	readLifecycleBadOrgFlag = "not-a-uuid"
)

type readLifecycleOutput struct {
	Result json.RawMessage `json:"result"`
}

type readLifecycleQueryResult struct {
	RowCount int `json:"row_count"`
	Rows     []struct {
		EventID uuid.UUID `json:"EventID"`
	} `json:"rows"`
}

type readLifecycleGetResult struct {
	Row struct {
		EventID uuid.UUID `json:"EventID"`
	} `json:"row"`
}

func TestAuditQueryAndGetRecordReadLifecycle(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	orgID := uuid.Must(uuid.NewV7())
	storedEvent, _ := storeManifestRead(t, ledger, orgID)
	now := clock.Now()
	window := []string{"--oldest", now.Add(-time.Hour).UTC().Format(time.RFC3339), "--latest", now.Add(time.Hour).UTC().Format(time.RFC3339)}

	getOperator, getRun := runReadLifecycleCommand(t, ledger, "audit", "get", "--event-id", storedEvent.String())
	if getRun.err != nil {
		t.Fatalf("audit get failed: %v\n%s", getRun.err, getRun.output())
	}
	getOutput := decodeReadLifecycleOutput(t, getRun)
	var got readLifecycleGetResult
	if err := json.Unmarshal(getOutput.Result, &got); err != nil || got.Row.EventID != storedEvent {
		t.Fatalf("audit get result = %+v with error %v, want event %s\n%s", got, err, storedEvent, getRun.output())
	}
	requireCompletedReadRow(t, awaitReadLifecycleRow(t, ledger, getOperator, audit.VerbAuditRead),
		uriTarget(audit.EventURIPrefix+storedEvent.String()), getOutput.Result, []string{audit.EventURIPrefix + storedEvent.String()})

	queryArgs := append([]string{"audit", "query", "--org", orgID.String()}, window...)
	queryOperator, queryRun := runReadLifecycleCommand(t, ledger, queryArgs...)
	if queryRun.err != nil {
		t.Fatalf("audit query failed: %v\n%s", queryRun.err, queryRun.output())
	}
	queryOutput := decodeReadLifecycleOutput(t, queryRun)
	var queried readLifecycleQueryResult
	if err := json.Unmarshal(queryOutput.Result, &queried); err != nil {
		t.Fatalf("decode the audit query result: %v\n%s", err, queryRun.output())
	}
	returned := make([]string, 0, len(queried.Rows))
	foundStored := false
	for _, row := range queried.Rows {
		returned = append(returned, audit.EventURIPrefix+row.EventID.String())
		if row.EventID == storedEvent {
			foundStored = true
		}
	}
	if !foundStored || queried.RowCount != len(returned) {
		t.Fatalf("audit query returned %d rows %v, want the stored event %s\n%s", queried.RowCount, returned, storedEvent, queryRun.output())
	}
	requireCompletedReadRow(t, awaitReadLifecycleRow(t, ledger, queryOperator, audit.VerbAuditRead),
		uriTarget(audit.AuditOrgURIPrefix+orgID.String()), queryOutput.Result, returned)

	missingEvent := uuid.Must(uuid.NewV7())
	missingOperator, missingRun := runReadLifecycleCommand(t, ledger, "audit", "get", "--event-id", missingEvent.String())
	requireFailedReadRun(t, missingRun)
	requireFailedReadRow(t, awaitReadLifecycleRow(t, ledger, missingOperator, audit.VerbAuditRead),
		uriTarget(audit.EventURIPrefix+missingEvent.String()), "no event with id")

	badOrgArgs := append([]string{"audit", "query", "--org", readLifecycleBadOrgFlag}, window...)
	badOperator, badRun := runReadLifecycleCommand(t, ledger, badOrgArgs...)
	requireFailedReadRun(t, badRun)
	failedRow := awaitReadLifecycleRow(t, ledger, badOperator, audit.VerbAuditRead)
	requireFailedReadRow(t, failedRow, uriTarget(audit.AuditOrgURIPrefix+audit.InvalidURISegment), "--org")
	if strings.Contains(string(failedRow.Extra), readLifecycleBadOrgFlag) {
		t.Fatalf("row %s stores the malformed flag value: %s", failedRow.EventID, failedRow.Extra)
	}
}

func runReadLifecycleCommand(t *testing.T, ledger *nodeAuditLedger, command ...string) (uuid.UUID, builtAuditRun) {
	t.Helper()
	operatorID := uuid.Must(uuid.NewV7())
	args := append([]string{
		"--execute", "--operator-id", operatorID.String(), "--operator-email", "read-lifecycle@example.test", "--output", "json",
	}, command...)
	return operatorID, builtAuditCommand(t, ledger.cfg.AuditReaderDSN, args...)
}

func decodeReadLifecycleOutput(t *testing.T, run builtAuditRun) readLifecycleOutput {
	t.Helper()
	var output readLifecycleOutput
	if err := json.Unmarshal([]byte(run.stdout), &output); err != nil {
		t.Fatalf("decode the command output: %v\n%s", err, run.output())
	}
	return output
}

func awaitReadLifecycleRow(t *testing.T, ledger *nodeAuditLedger, operatorID uuid.UUID, verb audit.Verb) audit.Row {
	t.Helper()
	now := clock.Now()
	filter := audit.QueryFilter{
		OrgID: audit.SystemOrgID(), Oldest: now.Add(-time.Hour), Latest: now.Add(time.Hour),
		Action: string(verb), ActorID: operatorID, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: 2,
	}
	deadline := clock.Now().Add(nodeAuditDeadline)
	for {
		rows, err := ledger.reader.Query(t.Context(), filter)
		if err != nil {
			t.Fatalf("query the audit.read rows of operator %s: %v", operatorID, err)
		}
		if len(rows) > 1 {
			t.Fatalf("operator %s has %d audit.read rows, want 1", operatorID, len(rows))
		}
		if len(rows) == 1 {
			return rows[0]
		}
		if clock.Now().After(deadline) {
			t.Fatalf("operator %s has no audit.read row after %s", operatorID, nodeAuditDeadline)
		}
		time.Sleep(nodeAuditPollInterval)
	}
}

type readRowTarget struct {
	uri       string
	reference string
}

func uriTarget(uri string) readRowTarget {
	return readRowTarget{uri: uri, reference: ""}
}

func referenceTarget(reference string) readRowTarget {
	return readRowTarget{uri: "", reference: reference}
}

func requireReadRowShape(t *testing.T, row audit.Row, outcome audit.Outcome, want readRowTarget) {
	t.Helper()
	target := row.Context.Target
	if row.Outcome != outcome || row.Context.OperationClass != audit.OperationRead || row.Context.Phase != audit.PhaseRead ||
		target == nil || target.URI != want.uri || target.Reference != want.reference {
		t.Fatalf("row %s = outcome %q class %q phase %q target %+v, want %q read read %+v",
			row.EventID, row.Outcome, row.Context.OperationClass, row.Context.Phase, row.Context.Target, outcome, want)
	}
}

func requireCompletedReadRow(t *testing.T, row audit.Row, want readRowTarget, result json.RawMessage, itemURIs []string) {
	t.Helper()
	requireReadRowShape(t, row, audit.OutcomeCompleted, want)
	requireReadEvidence(t, row, result, itemURIs)
}

func requireVerdictReadRow(t *testing.T, row audit.Row, want readRowTarget, result json.RawMessage, itemURIs []string, messagePart string) {
	t.Helper()
	requireReadRowShape(t, row, audit.OutcomeFailed, want)
	requireReadEvidence(t, row, result, itemURIs)
	if row.Error == nil || row.Error.Code != "check_failed" || !strings.Contains(row.Error.Message, messagePart) {
		t.Fatalf("row %s error %+v, want check_failed containing %q", row.EventID, row.Error, messagePart)
	}
}

func requireReadEvidence(t *testing.T, row audit.Row, result json.RawMessage, itemURIs []string) {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, result); err != nil {
		t.Fatalf("compact the command result: %v", err)
	}
	fingerprint := sha256.Sum256(compact.Bytes())
	manifestDigest, manifestParts := expectedManifestEvidence(t, itemURIs)
	read := row.Context.Read
	if read == nil || read.PayloadSHA256 != hex.EncodeToString(fingerprint[:]) || read.PayloadFormat != readLifecycleJSONFormat ||
		read.Count != uint64(len(itemURIs)) || read.ManifestSHA256 != manifestDigest || read.ManifestParts != manifestParts {
		t.Fatalf("row %s read evidence = %+v, want fingerprint %x, %d items, manifest %q with %d parts",
			row.EventID, read, fingerprint, len(itemURIs), manifestDigest, manifestParts)
	}
}

func requireFailedReadRun(t *testing.T, run builtAuditRun) {
	t.Helper()
	if run.err == nil || strings.Contains(run.stdout, `"command"`) {
		t.Fatalf("failing read = %v, want an error and no result\n%s", run.err, run.output())
	}
}

func requireFailedReadRow(t *testing.T, row audit.Row, want readRowTarget, messagePart string) {
	t.Helper()
	requireReadRowShape(t, row, audit.OutcomeFailed, want)
	if row.Context.Read != nil || row.Error == nil || row.Error.Code != "read_failed" || !strings.Contains(row.Error.Message, messagePart) {
		t.Fatalf("row %s read %+v error %+v, want no evidence and read_failed containing %q", row.EventID, row.Context.Read, row.Error, messagePart)
	}
}
