package integration

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// TestActorID identifies the system actor of the writes that the integration
// fixtures make.
var TestActorID = uuid.MustParse("019e6b4a-0000-7000-8000-0000000000a1")

// WithTestOperation returns ctx with a new audit operation of the system
// actor TestActorID. Every FoundationDB writer requires an operation.
func WithTestOperation(t testing.TB, ctx context.Context) context.Context {
	t.Helper()
	operation, err := audit.NewMutationOperation(audit.Actor{
		Type: audit.ActorSystem, ID: TestActorID, Email: "", Name: "integration test", SessionID: "",
		IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
	}, audit.SourceSystem, "integration-test")
	if err != nil {
		t.Fatalf("create the test audit operation: %v", err)
	}
	attached, err := audit.WithMutationContext(ctx, operation)
	if err != nil {
		t.Fatalf("attach the test audit operation: %v", err)
	}
	return attached
}
