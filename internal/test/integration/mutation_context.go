package integration

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// testActorID identifies the system actor of the writes that the integration
// fixtures make.
var testActorID = uuid.MustParse("019e6b4a-0000-7000-8000-0000000000a1")

// testMutationContext returns ctx with a new audit operation of the system
// actor testActorID. Every FoundationDB writer requires an operation.
func testMutationContext(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	operation, err := audit.NewMutationOperation(audit.Actor{
		Type: audit.ActorSystem, ID: testActorID, Email: "", Name: "integration test", SessionID: "",
		IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
	}, audit.SourceSystem, "integration-test")
	if err != nil {
		t.Fatalf("create the test audit operation: %v", err)
	}
	return audit.WithMutationContext(ctx, operation)
}
