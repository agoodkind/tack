package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

type carriedReadOutcome struct {
	details audit.OutcomeDetails
}

func (e carriedReadOutcome) Error() string { return "carried read outcome" }

func (e carriedReadOutcome) ReadOutcome() audit.OutcomeDetails { return e.details }

func TestOperatorReadRecordsOnlyRefusedCarriedOutcomes(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	store := audit.NewPoolReadLifecycle(rolePool(t, testenv.Ledger(t), "audit_operator"))

	cases := []struct {
		name     string
		details  audit.OutcomeDetails
		outcome  audit.Outcome
		wantCode string
	}{
		{
			name:    "refused",
			details: audit.OutcomeDetails{Outcome: audit.OutcomeRefused, FailureCode: "input_invalid", FailureMessage: "refused", CommittedSteps: nil},
			outcome: audit.OutcomeRefused, wantCode: "input_invalid",
		},
		{
			name:    "failed",
			details: audit.OutcomeDetails{Outcome: audit.OutcomeFailed, FailureCode: "step_failed", FailureMessage: "failed", CommittedSteps: nil},
			outcome: audit.OutcomeFailed, wantCode: "read_failed",
		},
		{
			name:    "completed",
			details: audit.OutcomeDetails{Outcome: audit.OutcomeCompleted, FailureCode: "", FailureMessage: "", CommittedSteps: nil},
			outcome: audit.OutcomeFailed, wantCode: "read_failed",
		},
	}
	for _, current := range cases {
		t.Run(current.name, func(t *testing.T) {
			target := audit.Target{
				Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: "", URI: "tack:command/carried-read-outcome",
				RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
			}
			template := completeEvent(audit.SystemOrgID(), userActor(), target, audit.OperationRead, audit.PhaseRead, "")
			template.Context.CompletedAt = time.Time{}

			_, err := audit.RecordOperatorRead(t.Context(), store, template, func(context.Context) (audit.OperatorReadOutput, error) {
				return audit.OperatorReadOutput{}, carriedReadOutcome{details: current.details}
			})
			if err == nil {
				t.Fatal("RecordOperatorRead returned no error, want the carried read error")
			}

			row := waitForReadRow(t, ledger, template.EventID)
			if row.Outcome != current.outcome || row.Error == nil || row.Error.Code != current.wantCode {
				t.Fatalf("recorded outcome %q, error %+v, want %q with code %q", row.Outcome, row.Error, current.outcome, current.wantCode)
			}
		})
	}
}

func waitForReadRow(t *testing.T, ledger *nodeAuditLedger, eventID uuid.UUID) *audit.Row {
	t.Helper()
	deadline := clock.Now().Add(nodeAuditDeadline)

	for {
		row, err := ledger.reader.GetByID(t.Context(), eventID)
		if err == nil {
			return row
		}

		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("read event %s: %v", eventID, err)
		}
		if clock.Now().After(deadline) {
			t.Fatalf("event %s was not stored after %s", eventID, nodeAuditDeadline)
		}

		time.Sleep(nodeAuditPollInterval)
	}
}
