package integration_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// requireManifestFailures runs each failing read and returns the required audit evidence for each run.
func requireManifestFailures(
	t *testing.T,
	ledger *nodeAuditLedger,
	operator manifestRunOptions,
	orgID uuid.UUID,
) []manifestReadRun {
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
		{
			name:      "unreachable reader",
			readerDSN: manifestUnreachableReaderDSN,
			org:       orgID.String(),
			orgInput:  orgID.String(),
		},
		{
			name:            "malformed org",
			readerDSN:       operator.readerDSN,
			org:             manifestMalformedFlag,
			orgInput:        "",
			messageContains: "--org",
			forbidden:       []string{manifestMalformedFlag},
		},
		{
			name:            "malformed event",
			readerDSN:       operator.readerDSN,
			org:             orgID.String(),
			orgInput:        orgID.String(),
			event:           manifestMalformedFlag,
			eventInput:      "",
			targetURI:       manifestInvalidTargetURI,
			messageContains: "--event-id",
			forbidden:       []string{manifestMalformedFlag},
		},
		{
			name:            "application role reader",
			readerDSN:       ledgerLoginDSN(t, ledger, lastChangeAppRole),
			org:             orgID.String(),
			orgInput:        orgID.String(),
			messageContains: "permission denied",
		},
		{
			name:            "malformed reader DSN",
			readerDSN:       manifestMalformedReaderDSN,
			org:             orgID.String(),
			orgInput:        orgID.String(),
			messageContains: manifestReaderFixedMessage,
			forbidden:       []string{manifestDSNSecret, manifestParseErrorText},
		},
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
		run := runManifestParts(
			t,
			ledger,
			options,
			manifestArgs(current.org, event, "--execute")...,
		)
		slog.SetDefault(previousLogger)
		if run.err == nil || strings.Contains(run.stdout, "part_count") {
			t.Fatalf("%s = %+v, want an error and no result", current.name, run)
		}

		if current.messageContains != "" &&
			!strings.Contains(run.err.Error(), current.messageContains) {
			t.Fatalf("%s error = %v, want %q", current.name, run.err, current.messageContains)
		}

		for _, forbidden := range current.forbidden {
			if strings.Contains(run.stdout+run.stderr+run.err.Error()+logs.String(), forbidden) {
				t.Fatalf(
					"%s printed or logged the text %q: %+v\nlogs:\n%s",
					current.name,
					forbidden,
					run,
					logs.String(),
				)
			}
		}

		runs = append(
			runs,
			manifestReadRun{
				orgInput:        current.orgInput,
				eventInput:      eventInput,
				targetURI:       targetURI,
				outcome:         audit.OutcomeFailed,
				errorCode:       "read_failed",
				messageContains: current.messageContains,
				forbidden:       current.forbidden,
				payloadSHA256:   "",
				partURIs:        nil,
			},
		)
	}

	return runs
}

// requireManifestRecordingRefusals checks that an outbox refusal prevents the read.
func requireManifestRecordingRefusals(
	t *testing.T,
	ledger *nodeAuditLedger,
	operator manifestRunOptions,
	orgID uuid.UUID,
	eventID uuid.UUID,
	extra ...string,
) {
	t.Helper()
	admission := operator
	admission.operatorID = uuid.Must(uuid.NewV7())
	admission.outbox = rolePool(t, testenv.Ledger(t), lastChangeAppRole)
	refused := runManifestParts(
		t,
		ledger,
		admission,
		manifestArgs(
			orgID.String(),
			eventID.String(),
			append([]string{"--execute"}, extra...)...,
		)...,
	)
	if !errors.Is(refused.err, audit.ErrReadNotAdmitted) ||
		refused.stdout != "" {
		t.Fatalf("admission refusal = %+v, want a refused admission and no result", refused)
	}

	lagLedger := *ledger
	lagLedger.admin = ledgerAdminPool(t)
	lagLedger.waitForNoLag(t)
	requireStoredAuditReads(t, ledger, admission.operatorID, nil)
}
