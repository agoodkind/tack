package integration_test

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// requireFenceAdvances checks that each node write advances its bucket's epoch
// and that buckets without writes keep their previous epochs.
func requireFenceAdvances(
	t *testing.T,
	env *integration.TestEnv,
	operation string,
	write func() []uuid.UUID,
) {
	t.Helper()
	before, err := env.Stores.Nodes.CaptureStrictEvidence(env.Ctx)
	if err != nil {
		t.Fatalf("capture the evidence before the %s: %v", operation, err)
	}

	written := write()
	after, err := env.Stores.Nodes.CaptureStrictEvidence(env.Ctx)
	if err != nil {
		t.Fatalf("capture the evidence after the %s: %v", operation, err)
	}

	expected := make(map[int]int64, len(written))
	for _, nodeID := range written {
		expected[node.WriteFenceBucket(env.OrgID, nodeID)]++
	}

	for bucket := range node.WriteFenceBuckets {
		advance := after.Buckets[bucket] - before.Buckets[bucket]
		if advance < expected[bucket] || (expected[bucket] == 0 && advance != 0) {
			t.Fatalf(
				"the %s advanced bucket %d by %d, want at least %d and no advance without a written node %v",
				operation,
				bucket,
				advance,
				expected[bucket],
				written,
			)
		}
	}
}

// deleteStrictNode resumes subtree deletes because NodeService.Delete can
// return before its request budget finishes the job.
func deleteStrictNode(t *testing.T, env *integration.TestEnv, nodeID, actorID uuid.UUID) {
	t.Helper()
	if _, err := env.NodeSvc.Delete(env.Ctx, nodeID, actorID); err != nil {
		t.Fatalf("delete node %s: %v", nodeID, err)
	}

	if _, err := env.NodeSvc.ResumeSubtreeDeletes(env.Ctx, 0); err != nil {
		t.Fatalf("finish the delete of node %s: %v", nodeID, err)
	}
}
