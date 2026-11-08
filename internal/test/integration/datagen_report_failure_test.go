package integration_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// TestDatagenReportRecordingFailure withholds the seed response after real
// SQL roles refuse admission or finalization. Committed node evidence must
// retain completed outcomes despite the enclosing command error.
func TestDatagenReportRecordingFailure(t *testing.T) {
	fixture := newDatagenReportFixture(t)
	for _, finalization := range []bool{false, true} {
		name, role, count := "admission", "audit_reader", 2
		if finalization {
			name, role, count = "finalization", finalizeDeniedRole, 3
			if _, err := fixture.pipeline.admin.Exec(t.Context(), readTestSQL(t, "runtime_audit_public_finalize_denied.sql")); err != nil {
				t.Fatalf("configure the finalization-denied role: %v", err)
			}
		}
		t.Run(name, func(t *testing.T) {
			operatorID := uuid.Must(uuid.NewV7())
			seed := time.Now().UnixNano()
			denied := rolePool(t, fixture.pipeline.ledger, role)
			root, output := datagenReportFailureCommand(t, fixture, denied, operatorID, seed)
			err := root.ExecuteContext(testenv.CommandContext(t))
			if err == nil || output.Len() != 0 {
				t.Fatalf("%s error = %v, response = %q; want withheld reports", name, err, output.String())
			}
			if !finalization && !errors.Is(err, audit.ErrReadNotAdmitted) {
				t.Fatalf("admission error = %v, want audit.ErrReadNotAdmitted", err)
			}
			if finalization {
				if !strings.Contains(err.Error(), "finalize audit read") {
					t.Fatalf("finalization error = %v, want a recording failure", err)
				}
				recoverSearchRead(t, fixture.pipeline.admin, serviceDatagenActor(operatorID))
			}
			rows := fixture.operatorRows(t, operatorID, count)
			parent := requireDatagenMutationOutcome(t, rows, audit.OutcomeError, audit.VerbOpsDatagenSeed)
			for _, row := range rows {
				if row.Action != string(audit.VerbOpsAuditConsumerOffsets) {
					continue
				}
				var extra datagenReportExtra
				if err := json.Unmarshal(row.Extra, &extra); err != nil {
					t.Fatalf("decode the recovered report selection: %v", err)
				}
				if row.Outcome != audit.OutcomeIncomplete || row.Context.ParentOperationID != parent ||
					row.Context.Read != nil || row.Context.Target == nil || row.Context.Target.Reference != "audit/consumer-offsets" ||
					extra.Input.Report != "ops.audit.consumer_offsets" || extra.Input.Topic != fixture.config.AuditKafkaTopic {
					t.Fatalf("recording failure did not preserve the pending read evidence: %+v", row)
				}
				t.Logf("permanent refused report event=%s parent=%s operation=%s outcome=%s",
					row.EventID, parent, row.Context.OperationID, row.Outcome)
			}
			requireDatagenCommittedChanges(t, fixture, seed)
			t.Logf("report %s refused; response bytes=%d; enclosing intent and command_failed outcome remain permanent", name, output.Len())
		})
	}
}

func datagenReportFailureCommand(t *testing.T, fixture datagenReportFixture, reads *pgxpool.Pool, operatorID uuid.UUID, seed int64) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	output := &bytes.Buffer{}
	factory := cli.System(fixture.config)
	factory.Out, factory.Err = output, &bytes.Buffer{}
	factory.SetAuditOutbox(audit.NewPoolOutbox(fixture.pipeline.admin))
	factory.SetAuditReads(audit.NewPoolReadLifecycle(reads))
	root := integration.SearchCommandRoot(factory)
	root.SetArgs([]string{
		"--execute", "--output", "json", "--operator-id", operatorID.String(),
		"--operator-email", "datagen@example.test", "--operator-name", "Report Recording Test",
		"--operator-service", searchReadService(operatorID), "--operator-session", searchReadSession,
		"--deploy-commit", searchReadDeployCommit,
		"ops", "qa", "datagen", "seed", "--commit", "--seed", strconv.FormatInt(seed, 10),
	})
	return root, output
}
