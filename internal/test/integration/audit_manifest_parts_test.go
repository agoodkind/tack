package integration_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/testenv"
)

const (
	// manifestUnreachableReaderDSN identifies an unreachable audit reader.
	manifestUnreachableReaderDSN = "postgres://nobody@[::1]:1/tack?connect_timeout=1"
	// manifestDSNSecret is the password text that pgx v5.11.0 does not mask in its parse error.
	manifestDSNSecret = "parts@secret"
	// manifestMalformedReaderDSN is a malformed AUDIT_READER_DSN with @ characters in its password.
	manifestMalformedReaderDSN = "postgres://nobody:manifest@" + manifestDSNSecret + "@[::1:1/tack"
	// manifestReaderFixedMessage is the fixed text "check AUDIT_READER_DSN" in the stored failure message.
	manifestReaderFixedMessage = "check AUDIT_READER_DSN"
	// manifestParseErrorText is the text "cannot parse" from the raw parse error.
	manifestParseErrorText = "cannot parse"
	// manifestMalformedFlag is a flag value that does not parse as a UUID.
	manifestMalformedFlag = "manifest-parts-raw-flag-text"
	// manifestInvalidTargetURI is the fixed audit target for a malformed event ID.
	manifestInvalidTargetURI = "tack:audit-event/invalid"
)

// manifestPartEntry represents one part in the command report.
type manifestPartEntry struct {
	Index  uint32 `json:"index"`
	Digest string `json:"digest"`
}

// manifestPartsReport represents the command report.
type manifestPartsReport struct {
	Command   string              `json:"command"`
	OrgID     uuid.UUID           `json:"org_id"`
	EventID   uuid.UUID           `json:"event_id"`
	PartCount int                 `json:"part_count"`
	Parts     []manifestPartEntry `json:"parts"`
}

// manifestReadRun contains the audit evidence required for one executed run.
type manifestReadRun struct {
	orgInput        string
	eventInput      string
	targetURI       string
	outcome         audit.Outcome
	errorCode       string
	messageContains string
	forbidden       []string
	payloadSHA256   string
	partURIs        []string
	verifyPayloads  bool `exhaustruct:"optional"`
}

// manifestRunOptions contains the reader DSN, lifecycle pool, and operator for one command run.
type manifestRunOptions struct {
	readerDSN  string
	lifecycle  *pgxpool.Pool
	operatorID uuid.UUID
}

// manifestCommandRun contains the standard output, standard error, and error from one command run.
type manifestCommandRun struct {
	stdout string
	stderr string
	err    error
}

// TestAuditManifestPartsReportsStoredParts runs the command through the rendered ops tree and checks the reported parts and audit evidence. It checks read failures and recording refusals.
func TestAuditManifestPartsReportsStoredParts(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	orgID, otherOrgID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	storedEvent, storedParts := storeManifestRead(t, ledger, orgID)
	operator := manifestRunOptions{
		readerDSN: ledger.cfg.AuditReaderDSN, lifecycle: rolePool(t, testenv.Ledger(t), "audit_operator"), operatorID: uuid.Must(uuid.NewV7()),
	}

	// Run without --execute and with an empty reader DSN.
	dryOptions := operator
	dryOptions.readerDSN = ""
	dry := runManifestParts(t, ledger, dryOptions, manifestArgs(orgID.String(), storedEvent.String())...)
	if dry.err != nil || !strings.Contains(dry.stderr, "would run: audit.read") || strings.Contains(dry.stdout, "part_count") {
		t.Fatalf("dry run = %+v, want no error, a plan on stderr, and no result", dry)
	}

	// Check stored parts, an event without parts, and another org.
	emptyEvent := uuid.Must(uuid.NewV7())
	runs := []manifestReadRun{
		requireManifestRead(t, ledger, operator, orgID, storedEvent, storedParts),
		requireManifestRead(t, ledger, operator, orgID, emptyEvent, nil),
		requireManifestRead(t, ledger, operator, otherOrgID, storedEvent, nil),
	}
	// Check the audit rows and failing reads.
	runs = append(runs, requireManifestFailures(t, ledger, operator, orgID)...)
	requireStoredAuditReads(t, ledger, operator.operatorID, runs)
	requireManifestRecordingRefusals(t, ledger, operator, orgID, storedEvent)
}

// manifestArgs returns the flags for one command run.
func manifestArgs(org, event string, extra ...string) []string {
	return append([]string{"--org", org, "--event-id", event}, extra...)
}

// runManifestParts runs the command through the rendered ops tree.
func runManifestParts(t *testing.T, ledger *nodeAuditLedger, options manifestRunOptions, args ...string) manifestCommandRun {
	t.Helper()
	return runOpsTreeCommand(t, ledger, options, []string{"ops", "audit", "manifest-parts"}, args...)
}

// runOpsTreeCommand runs command with args through the rendered ops tree as options.operatorID.
func runOpsTreeCommand(t *testing.T, ledger *nodeAuditLedger, options manifestRunOptions, command []string, args ...string) manifestCommandRun {
	t.Helper()
	cfg := *ledger.cfg
	cfg.AuditReaderDSN = options.readerDSN
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	factory := cli.System(&cfg)
	factory.Out, factory.Err = stdout, stderr
	factory.SetAuditOutbox(audit.NewPoolOutbox(ledger.admin))
	factory.SetAuditReads(audit.NewPoolReadLifecycle(options.lifecycle))
	root := nodeAuditCommandRoot(factory)
	root.SetContext(t.Context())
	root.SetArgs(slices.Concat([]string{
		"--output", "json", "--operator-id", options.operatorID.String(), "--operator-email", "manifest-parts@example.test",
	}, command, args))
	err := root.Execute()
	return manifestCommandRun{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

// requireManifestRead runs a successful command and compares each printed digest with the digest of the separately read payload. It checks that the output omits payloads and returns the expected audit evidence.
func requireManifestRead(t *testing.T, ledger *nodeAuditLedger, options manifestRunOptions, orgID, eventID uuid.UUID, parts []audit.ManifestPart) manifestReadRun {
	t.Helper()
	run := runManifestParts(t, ledger, options, manifestArgs(orgID.String(), eventID.String(), "--execute")...)
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
	partURIs := make([]string, 0, len(parts))
	for _, part := range parts {
		digest := sha256.Sum256(part.Payload)
		want = append(want, manifestPartEntry{Index: part.Index, Digest: hex.EncodeToString(digest[:])})
		partURIs = append(partURIs, "tack:audit-event/"+eventID.String()+"/part/"+strconv.FormatUint(uint64(part.Index), 10))
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
		orgInput: orgID.String(), eventInput: eventID.String(), targetURI: "tack:audit-event/" + eventID.String(),
		outcome: audit.OutcomeCompleted, errorCode: "", messageContains: "", forbidden: nil,
		payloadSHA256: hex.EncodeToString(digest[:]), partURIs: partURIs,
	}
}
