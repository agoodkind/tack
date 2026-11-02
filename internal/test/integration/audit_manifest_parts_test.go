package integration_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

// manifestUnreachableReaderDSN is a reader DSN with a port that refuses connections.
const manifestUnreachableReaderDSN = "postgres://nobody@[::1]:1/tack?connect_timeout=1"

// manifestPartEntry represents one decoded part entry.
type manifestPartEntry struct {
	Index  uint32 `json:"index"`
	Digest string `json:"digest"`
}

// manifestPartsReport represents the decoded command result.
type manifestPartsReport struct {
	Command   string              `json:"command"`
	OrgID     uuid.UUID           `json:"org_id"`
	EventID   uuid.UUID           `json:"event_id"`
	PartCount int                 `json:"part_count"`
	Parts     []manifestPartEntry `json:"parts"`
}

// manifestReadRun represents the audit evidence required for one executed run.
type manifestReadRun struct {
	orgID         uuid.UUID
	eventID       uuid.UUID
	outcome       audit.Outcome
	payloadSHA256 string
	targetCount   uint64
}

// manifestCommandRun records stdout, stderr, and the error from one command run.
type manifestCommandRun struct {
	stdout string
	stderr string
	err    error
}

// TestAuditManifestPartsReportsStoredParts checks command results and audit records through the rendered ops command tree. It uses the audit_reader role and the production read lifecycle with a running relay and audit consumer.
func TestAuditManifestPartsReportsStoredParts(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	readerDSN := ledger.cfg.AuditReaderDSN
	operatorID, orgID, otherOrgID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	storedEvent, storedParts := storeManifestRead(t, ledger, orgID)

	// Run without --execute and with an empty reader DSN.
	dry := runManifestParts(t, ledger, "", operatorID, "--org", orgID.String(), "--event-id", storedEvent.String())
	if dry.err != nil || !strings.Contains(dry.stderr, "would run: audit.read") || strings.Contains(dry.stdout, "part_count") {
		t.Fatalf("dry run = %+v, want no error, a plan on stderr, and no result", dry)
	}

	// Check stored parts, an event without parts, and the stored event with another org.
	storedRun := requireManifestRead(t, ledger, readerDSN, operatorID, orgID, storedEvent, storedParts)
	emptyEvent := uuid.Must(uuid.NewV7())
	emptyRun := requireManifestRead(t, ledger, readerDSN, operatorID, orgID, emptyEvent, nil)
	otherOrgRun := requireManifestRead(t, ledger, readerDSN, operatorID, otherOrgID, storedEvent, nil)

	// Run with an unreachable reader.
	failedEvent := uuid.Must(uuid.NewV7())
	failed := runManifestParts(t, ledger, manifestUnreachableReaderDSN, operatorID, "--org", orgID.String(), "--event-id", failedEvent.String(), "--execute")
	if failed.err == nil || strings.Contains(failed.stdout, "part_count") {
		t.Fatalf("run with an unreachable reader = %+v, want an error and no result", failed)
	}
	failedRun := manifestReadRun{orgID: orgID, eventID: failedEvent, outcome: audit.OutcomeFailed, payloadSHA256: "", targetCount: 0}

	// Check the audit.read rows for the executed runs.
	requireStoredAuditReads(t, ledger, operatorID, []manifestReadRun{storedRun, emptyRun, otherOrgRun, failedRun})
}

// runManifestParts runs the command through the rendered ops tree as operatorID with readerDSN and args.
func runManifestParts(t *testing.T, ledger *nodeAuditLedger, readerDSN string, operatorID uuid.UUID, args ...string) manifestCommandRun {
	t.Helper()
	cfg := *ledger.cfg
	cfg.AuditReaderDSN = readerDSN
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	factory := cli.System(&cfg)
	factory.Out, factory.Err = stdout, stderr
	factory.SetAuditOutbox(audit.NewPoolOutbox(ledger.admin))
	factory.SetAuditReads(audit.NewPoolReadLifecycle(ledger.admin))
	root := nodeAuditCommandRoot(factory)
	root.SetContext(t.Context())
	root.SetArgs(slices.Concat([]string{
		"--output", "json", "--operator-id", operatorID.String(), "--operator-email", "manifest-parts@example.test",
		"ops", "audit", "manifest-parts",
	}, args))
	err := root.Execute()
	return manifestCommandRun{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

// requireManifestRead runs the command successfully and checks the result and payload omission. It returns the expected audit evidence.
func requireManifestRead(t *testing.T, ledger *nodeAuditLedger, readerDSN string, operatorID, orgID, eventID uuid.UUID, parts []audit.ManifestPart) manifestReadRun {
	t.Helper()
	run := runManifestParts(t, ledger, readerDSN, operatorID, "--org", orgID.String(), "--event-id", eventID.String(), "--execute")
	if run.err != nil {
		t.Fatalf("ops audit manifest-parts for event %s = %+v, want no error", eventID, run)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(run.stdout), &envelope); err != nil {
		t.Fatalf("decode the command output: %v\n%s", err, run.stdout)
	}
	var report manifestPartsReport
	if err := json.Unmarshal(envelope.Result, &report); err != nil {
		t.Fatalf("decode the command result: %v\n%s", err, run.stdout)
	}
	want := make([]manifestPartEntry, 0, len(parts))
	for _, part := range parts {
		digest := sha256.Sum256(part.Payload)
		want = append(want, manifestPartEntry{Index: part.Index, Digest: hex.EncodeToString(digest[:])})
		if strings.Contains(run.stdout, string(part.Payload)) || strings.Contains(run.stdout, base64.StdEncoding.EncodeToString(part.Payload)) {
			t.Fatalf("stdout contains the payload bytes of part %d:\n%s", part.Index, run.stdout)
		}
	}
	if strings.Contains(run.stdout, "payload") {
		t.Fatalf("stdout contains a payload field:\n%s", run.stdout)
	}
	if report.Command != "ops.audit.manifest_parts" || report.OrgID != orgID || report.EventID != eventID ||
		report.PartCount != len(want) || report.Parts == nil || !slices.Equal(report.Parts, want) {
		t.Fatalf("result = %+v, want event %s of org %s with parts %+v", report, eventID, orgID, want)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, envelope.Result); err != nil {
		t.Fatalf("compact the command result: %v\n%s", err, run.stdout)
	}
	digest := sha256.Sum256(compact.Bytes())
	return manifestReadRun{
		orgID: orgID, eventID: eventID, outcome: audit.OutcomeCompleted,
		payloadSHA256: hex.EncodeToString(digest[:]), targetCount: uint64(len(want)),
	}
}
