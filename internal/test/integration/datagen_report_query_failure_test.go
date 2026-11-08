package integration_test

import (
	"encoding/json"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/testenv"
)

// TestDatagenReportQueryRefusal checks a real SQL permission failure after
// admission. Both permanent error messages must exclude the database error.
func TestDatagenReportQueryRefusal(t *testing.T) {
	fixture := newDatagenReportFixture(t)
	cfg := *fixture.config
	readerAddress, err := url.Parse(cfg.AuditReaderDSN)
	if err != nil {
		t.Fatal("parse the local ledger reader address")
	}
	parameters := readerAddress.Query()
	parameters.Set("role", "audit_operator")
	readerAddress.RawQuery = parameters.Encode()
	cfg.AuditReaderDSN = readerAddress.String()
	fixture.config = &cfg
	for _, format := range []string{cli.FormatJSON, cli.FormatText} {
		t.Run(format, func(t *testing.T) {
			operatorID := uuid.Must(uuid.NewV7())
			seed := time.Now().UnixNano()
			root, output := datagenReportFailureCommand(t, fixture, fixture.pipeline.admin, operatorID, seed, format)
			err := root.ExecuteContext(testenv.CommandContext(t))
			if err == nil || errors.Is(err, audit.ErrReadNotAdmitted) || output.Len() != 0 {
				t.Fatalf("query refusal did not fail after admission with suppressed output: error=%v bytes=%d", err, output.Len())
			}
			requireDatagenPermissionCause(t, err)
			safeMessage := "generated data report ops.audit.consumer_offsets failed"
			if err.Error() != "qa datagen seed: "+safeMessage {
				t.Fatalf("query refusal returned an unsafe message: %s", err.Error())
			}
			rows := fixture.operatorRows(t, operatorID, 3)
			parent := requireDatagenMutationOutcome(t, rows, audit.OutcomeError, audit.VerbOpsDatagenSeed)
			failed := 0
			for _, row := range rows {
				if row.Action == string(audit.VerbOpsDatagenSeed) && row.Outcome == audit.OutcomeError && row.Error.Message != err.Error() {
					t.Fatalf("the enclosing command recorded an unsafe query error: %s", row.Error.Message)
				}
				if row.Action != string(audit.VerbOpsAuditConsumerOffsets) {
					continue
				}
				var extra datagenReportExtra
				if decodeErr := json.Unmarshal(row.Extra, &extra); decodeErr != nil {
					t.Fatalf("decode the failed report selection: %v", decodeErr)
				}
				if row.Outcome != audit.OutcomeFailed || row.Error == nil || row.Error.Code != "read_failed" ||
					row.Error.Message != safeMessage || row.Context.Read != nil || row.Context.ParentOperationID != parent ||
					extra.Input.Report != "ops.audit.consumer_offsets" {
					t.Fatalf("the failed query did not record its safe report evidence: %+v", row)
				}
				failed++
				t.Logf("permanent query refusal event=%s parent=%s outcome=%s message=%s", row.EventID, parent, row.Outcome, row.Error.Message)
			}
			if failed != 1 {
				t.Fatalf("permanent failed report events = %d, want 1", failed)
			}
			requireDatagenCommittedChanges(t, fixture, seed)
		})
	}
}

func requireDatagenPermissionCause(t *testing.T, err error) {
	t.Helper()
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != "42501" || !errors.Is(err, databaseError) {
		t.Fatal("the safe error did not preserve the original SQL permission cause")
	}
}
