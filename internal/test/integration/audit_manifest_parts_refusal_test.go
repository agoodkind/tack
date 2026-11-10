package integration_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// requireManifestFailures runs each failing read and returns the required audit evidence for each run.
func requireManifestFailures(t *testing.T, ledger *nodeAuditLedger, operator manifestRunOptions, orgID uuid.UUID) []manifestReadRun {
	t.Helper()
	cases := []struct {
		name            string
		readerDSN       string
		org             string
		event           string
		orgInput        string
		eventInput      string
		targetURI       string
		messageContains string
		forbidden       []string
	}{
		{name: "unreachable reader", readerDSN: manifestUnreachableReaderDSN, org: orgID.String(), orgInput: orgID.String()},
		{name: "malformed org", readerDSN: operator.readerDSN, org: manifestMalformedFlag, orgInput: "", messageContains: "--org", forbidden: []string{manifestMalformedFlag}},
		{name: "malformed event", readerDSN: operator.readerDSN, org: orgID.String(), orgInput: orgID.String(), event: manifestMalformedFlag, eventInput: "", targetURI: manifestInvalidTargetURI, messageContains: "--event-id", forbidden: []string{manifestMalformedFlag}},
		{name: "application role reader", readerDSN: ledgerLoginDSN(t, ledger, lastChangeAppRole), org: orgID.String(), orgInput: orgID.String(), messageContains: "permission denied"},
		{name: "malformed reader DSN", readerDSN: manifestMalformedReaderDSN, org: orgID.String(), orgInput: orgID.String(), messageContains: manifestReaderFixedMessage, forbidden: []string{manifestDSNSecret, manifestParseErrorText}},
	}
	runs := make([]manifestReadRun, 0, len(cases))
	for _, current := range cases {
		event, eventInput, targetURI := current.event, current.eventInput, current.targetURI
		if targetURI == "" {
			eventID := uuid.Must(uuid.NewV7())
			event, eventInput, targetURI = eventID.String(), eventID.String(), "tack:audit-event/"+eventID.String()
		}
		options := operator
		options.readerDSN = current.readerDSN
		logs := &bytes.Buffer{}
		previousLogger := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
		run := runManifestParts(t, ledger, options, manifestArgs(current.org, event, "--execute")...)
		slog.SetDefault(previousLogger)
		if run.err == nil || strings.Contains(run.stdout, "part_count") {
			t.Fatalf("%s = %+v, want an error and no result", current.name, run)
		}
		for _, forbidden := range current.forbidden {
			if strings.Contains(run.stdout+run.stderr+run.err.Error()+logs.String(), forbidden) {
				t.Fatalf("%s printed or logged the text %q: %+v\nlogs:\n%s", current.name, forbidden, run, logs.String())
			}
		}
		runs = append(runs, manifestReadRun{
			orgInput: current.orgInput, eventInput: eventInput, targetURI: targetURI, outcome: audit.OutcomeFailed, errorCode: "read_failed",
			messageContains: current.messageContains, forbidden: current.forbidden, payloadSHA256: "", partURIs: nil,
		})
	}
	return runs
}

// requireManifestRecordingRefusals checks refused admission and finalization. Neither run prints a result. Refused admission does not create a row. Recovery saves an incomplete row for refused finalization.
func requireManifestRecordingRefusals(t *testing.T, ledger *nodeAuditLedger, operator manifestRunOptions, orgID, eventID uuid.UUID, extra ...string) {
	t.Helper()
	admission := operator
	admission.operatorID = uuid.Must(uuid.NewV7())
	allowAdmission := readTestSQL(t, "search_read_allow_admission.sql")
	if _, err := ledger.admin.Exec(t.Context(), readTestSQL(t, "search_read_deny_admission.sql")); err != nil {
		t.Fatalf("deny the operator read admission: %v", err)
	}
	t.Cleanup(func() {
		if _, err := ledger.admin.Exec(context.WithoutCancel(t.Context()), allowAdmission); err != nil {
			t.Errorf("restore the operator read admission: %v", err)
		}
	})
	admission.lifecycle = rolePool(t, testenv.Ledger(t), "audit_operator")
	refused := runManifestParts(t, ledger, admission, manifestArgs(orgID.String(), eventID.String(), append([]string{"--execute"}, extra...)...)...)
	if _, err := ledger.admin.Exec(t.Context(), allowAdmission); err != nil {
		t.Fatalf("restore the operator read admission: %v", err)
	}
	if !errors.Is(refused.err, audit.ErrReadNotAdmitted) || !strings.Contains(refused.err.Error(), "ops_read_begin") || refused.stdout != "" {
		t.Fatalf("admission refusal = %+v, want a refused admission and no result", refused)
	}

	if _, err := ledger.admin.Exec(t.Context(), readTestSQL(t, "runtime_audit_public_finalize_denied.sql")); err != nil {
		t.Fatalf("configure the finalization-denied role: %v", err)
	}
	finalization := operator
	finalization.operatorID = uuid.Must(uuid.NewV7())
	finalization.lifecycle = rolePool(t, testenv.Ledger(t), finalizeDeniedRole)
	withheld := runManifestParts(t, ledger, finalization, manifestArgs(orgID.String(), eventID.String(), append([]string{"--execute"}, extra...)...)...)
	if withheld.err == nil || !strings.Contains(withheld.err.Error(), "finalize audit read") || withheld.stdout != "" {
		t.Fatalf("finalization refusal = %+v, want a finalization error and no result", withheld)
	}
	recoverSearchRead(t, ledger.admin, finalization.operatorID)
	requireStoredAuditReads(t, ledger, finalization.operatorID, []manifestReadRun{{
		orgInput: orgID.String(), eventInput: eventID.String(), targetURI: "tack:audit-event/" + eventID.String(),
		outcome: audit.OutcomeIncomplete, errorCode: "", messageContains: "", forbidden: nil, payloadSHA256: "", partURIs: nil, verifyPayloads: len(extra) > 0,
	}})
	lagLedger := *ledger
	lagLedger.admin = ledgerAdminPool(t)
	lagLedger.waitForNoLag(t)
	requireStoredAuditReads(t, ledger, admission.operatorID, nil)
}
