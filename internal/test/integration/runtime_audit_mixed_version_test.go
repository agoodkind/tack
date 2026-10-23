package integration_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
)

// TestRuntimeAuditMixedVersionRollout verifies that ops deploy verify accepts
// matching image indexes, rejects a stale audit-consumer replica, and records
// both verification attempts as operator events.
func TestRuntimeAuditMixedVersionRollout(t *testing.T) {
	fixture := integration.StartContainerdDeployFixture(t)
	serverDigest, consumerDigest := integration.PushDeployIndexes(t, fixture)
	integration.CreateDeployContainers(t, fixture)
	operatorID := uuid.Must(uuid.NewV7())

	output, err := integration.VerifyDeployAs(t, fixture, operatorID.String(), serverDigest, consumerDigest)
	if err != nil || !strings.Contains(output, "tack-audit-consumer-1") {
		t.Fatalf("deploy verify failed or omitted tack-audit-consumer-1 from its output; expected success with that replica: %v\n%s", err, output)
	}

	integration.AddConsumerReplica(t, fixture, "tack-audit-consumer-2", integration.DeployTag(fixture)+"-stale")
	output, err = integration.VerifyDeployAs(t, fixture, operatorID.String(), serverDigest, consumerDigest)
	if err == nil || !strings.Contains(err.Error(), "tack-audit-consumer-2") || !strings.Contains(err.Error(), "descriptor digest differs") {
		t.Fatalf("deploy verify did not report a descriptor digest mismatch for tack-audit-consumer-2; expected refusal of the stale replica: %v\n%s", err, output)
	}

	pool := ledgerAdminPool(t)
	events := outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql", string(audit.VerbOpsDeployVerify), operatorID.String())
	if len(events) != 2 {
		t.Fatalf("expected 2 ops.deploy.verify outbox events; actual count for operator %s: %d", operatorID, len(events))
	}
	for _, event := range events {
		if event.Actor.Type != audit.ActorOperator {
			t.Fatalf("expected actor type operator; actual actor type for event %s: %s", event.EventID, event.Actor.Type)
		}
	}
}
