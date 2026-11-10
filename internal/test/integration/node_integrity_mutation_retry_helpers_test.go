package integration_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// retryCreatePlan returns a plan that creates a node with ID newID under
// projectID using the idempotency key and fingerprint of input.
func retryCreatePlan(t *testing.T, env *integration.TestEnv, projectID uuid.UUID, input service.CreateInput, newID uuid.UUID) node.MutationPlan {
	t.Helper()
	_, schema := mutationSnapshot(t, env, projectID)
	name := "Store retry"
	return node.MutationPlan{
		OrgID: env.OrgID, NodeID: newID, NodeType: input.NodeTypeKey, ActorID: input.ActorID,
		Source: node.SourceExpectation{Absent: true, PrimaryDigest: node.Digest{}}, MetadataEpoch: schema.MetadataEpoch,
		Graph: node.GraphExpectation{Records: parentChain(t, env, projectID), Relationships: nil, Incoming: nil},
		Name:  &name, Props: schemaProps("scope_id", quoted(projectID.String())), Parent: &node.ParentChange{TargetID: projectID},
		ReferenceKeys: nil, Relationships: node.RelationshipChanges{Add: nil, Remove: nil},
		Idempotency: &node.IdempotencyRecord{
			Key: input.IdempotencyKey, NodeID: newID, Fingerprint: input.IdempotencyFingerprint,
			Source: input.IdempotencySource, CreatedAt: clock.Now().UTC(),
		},
	}
}

// auditSlot returns a context with an MCP tool audit slot for actorID.
func auditSlot(env *integration.TestEnv, actorID uuid.UUID) context.Context {
	return auditintent.WithSlot(audit.WithScopeBuilder(env.Ctx), "tack_create_issue", actorID)
}

// outboxEventCount returns the number of outbox events for entityID.
func outboxEventCount(t *testing.T, env *integration.TestEnv, entityID uuid.UUID) int {
	t.Helper()
	count := 0
	var mark []byte
	for {
		entries, err := env.Stores.OpsOutbox.ReadOutboxFrom(env.Ctx, mark, 500)
		if err != nil {
			t.Fatalf("read the operator outbox: %v", err)
		}
		if len(entries) == 0 {
			return count
		}
		for _, entry := range entries {
			var event audit.Event
			if err := json.Unmarshal(entry.Event, &event); err != nil {
				t.Fatalf("decode an outbox entry: %v", err)
			}
			if event.Entity.ID == entityID {
				count++
			}
		}
		mark = entries[len(entries)-1].Mark
	}
}
