package foundationdb_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
)

func relationPageContext(t *testing.T) context.Context {
	t.Helper()
	operation, err := audit.NewMutationOperation(
		audit.Actor{
			Type:          audit.ActorSystem,
			ID:            uuid.Must(uuid.NewV7()),
			Name:          "relationship pagination fixture",
			Email:         "",
			SessionID:     "",
			IP:            "",
			UserAgent:     "",
			RequestID:     "",
			APITokenLabel: "",
		},
		audit.SourceSystem,
		"relationship-page-test",
	)
	if err != nil {
		t.Fatalf("create fixture operation: %v", err)
	}

	ctx, err := audit.WithMutationContext(t.Context(), operation)
	if err != nil {
		t.Fatalf("attach fixture operation: %v", err)
	}

	return ctx
}
