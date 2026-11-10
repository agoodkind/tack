//go:build runtimeaudit

package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/test/integration"
)

// TestRuntimeAuditMixedVersionRollout verifies the release B fleet with ops
// deploy verify and requires a refusal while a stale audit-consumer replica
// runs. It then rolls the app back to a release A image and keeps the release B
// audit-consumer, which reads format 2 events, and requires deploy verify to
// accept that fleet. The three verify events wait in the operator outbox until
// the test starts the relay after the rollback. The consumer must store each
// event as a format 2 read with outcome completed, failed, and completed in
// that order.
func TestRuntimeAuditMixedVersionRollout(t *testing.T) {
	stack := startRuntimeAuditStack(t)
	started := clock.Now().UTC().Add(-time.Minute)
	fixture := integration.StartContainerdDeployFixture(t)
	serverDigest, consumerDigest := integration.PushDeployIndexes(t, fixture)
	integration.CreateDeployContainers(t, fixture)
	operatorID := uuid.Must(uuid.NewV7())

	output, err := integration.VerifyDeployAs(t, fixture, operatorID.String(), serverDigest, consumerDigest)
	if err != nil || !strings.Contains(output, "tack-audit-consumer-1") {
		t.Fatalf("Deploy verify failed or omitted tack-audit-consumer-1 from its output; the release B fleet must pass: %v\n%s", err, output)
	}

	integration.AddConsumerReplica(t, fixture, "tack-audit-consumer-2", integration.DeployTag(fixture)+"-stale")
	output, err = integration.VerifyDeployAs(t, fixture, operatorID.String(), serverDigest, consumerDigest)
	if err == nil || !strings.Contains(err.Error(), "tack-audit-consumer-2") || !strings.Contains(err.Error(), "descriptor digest differs") {
		t.Fatalf("Deploy verify did not report a descriptor digest mismatch for tack-audit-consumer-2; the stale replica must be refused: %v\n%s", err, output)
	}

	integration.RemoveDeployReplica(t, fixture, "tack-audit-consumer-2")
	releaseADigest := integration.ReplaceAppReplica(t, fixture, integration.DeployTag(fixture)+"-release-a")
	output, err = integration.VerifyDeployAs(t, fixture, operatorID.String(), releaseADigest, consumerDigest)
	if err != nil || !strings.Contains(output, "tack-app-1") {
		t.Fatalf("Deploy verify refused the rollback to the release A app with the release B audit-consumer: %v\n%s", err, output)
	}

	startOutboxRelay(t, stack)
	rows := waitForActorRows(t, stack, audit.VerbOpsDeployVerify, operatorID, started, 3)
	expected := []audit.Outcome{audit.OutcomeCompleted, audit.OutcomeFailed, audit.OutcomeCompleted}
	for index, row := range rows {
		if row.Outcome != expected[index] || row.Context.AuditFormatVersion != audit.AuditFormatComplete ||
			row.Context.OperationClass != audit.OperationRead {
			t.Fatalf("Verify event %d is %s, format %d, class %s; it must be a format %d read with outcome %s",
				index+1, row.Outcome, row.Context.AuditFormatVersion, row.Context.OperationClass,
				audit.AuditFormatComplete, expected[index])
		}
	}
	if rows[1].Error == nil || !strings.Contains(rows[1].Error.Message, "tack-audit-consumer-2") {
		t.Fatalf("The refused verify event does not name tack-audit-consumer-2 in its error: %+v", rows[1].Error)
	}
}

// waitForActorRows waits until the ledger returns count rows for verb by
// actorID since started and returns them in the order the events occurred.
func waitForActorRows(t *testing.T, stack runtimeAuditStack, verb audit.Verb, actorID uuid.UUID, started time.Time, count int) []audit.Row {
	t.Helper()
	deadline := clock.Now().Add(runtimeAuditDeadline)
	for {
		rows, err := stack.reader.Query(t.Context(), audit.QueryFilter{
			OrgID: audit.SystemOrgID(), Oldest: started, Latest: clock.Now().UTC().Add(time.Minute),
			Action: string(verb), ActorID: actorID, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: count + 1,
		})
		if err != nil {
			t.Fatalf("The ledger query for %s by %s failed: %v", verb, actorID, err)
		}
		if len(rows) > count {
			t.Fatalf("The ledger has %d rows for %s by %s instead of %d", len(rows), verb, actorID, count)
		}
		if len(rows) == count {
			ordered := make([]audit.Row, 0, count)
			for index := len(rows) - 1; index >= 0; index-- {
				ordered = append(ordered, rows[index])
			}
			return ordered
		}
		if clock.Now().After(deadline) {
			t.Fatalf("The ledger returned %d of %d rows for %s by %s within %s", len(rows), count, verb, actorID, runtimeAuditDeadline)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
