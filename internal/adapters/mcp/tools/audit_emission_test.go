package tools

import (
	"context"
	"testing"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
)

func TestWrappedToolRecordsInvocation(t *testing.T) {
	recorder := audit.NewMemoryRecorder()
	previousRecorder := currentAuditRecorder()
	SetAuditRecorder(recorder)
	t.Cleanup(func() {
		SetAuditRecorder(previousRecorder)
	})

	actorID := uuid.New()
	handler := wrapToolHandler("tack_list_workspaces", func(
		context.Context,
		mcpmcp.CallToolRequest,
	) (*mcpmcp.CallToolResult, error) {
		return successText("ok", ""), nil
	})
	_, err := handler(auth.WithUser(t.Context(), actorID), callToolReq("tack_list_workspaces", nil))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	assertAuditEvent(t, recorder.Events(), string(audit.VerbMCPToolInvoked), actorID, "mcp_tool", "tack_list_workspaces")
}

func assertAuditEvent(t *testing.T, events []audit.Event, verb string, actorID uuid.UUID, entityType, entityName string) {
	t.Helper()
	for _, event := range events {
		if event.Verb != verb {
			continue
		}
		if event.Actor.ID != actorID {
			t.Errorf("%s actor = %s, want %s", verb, event.Actor.ID, actorID)
		}
		if event.Entity.Type != entityType {
			t.Errorf("%s entity type = %q, want %q", verb, event.Entity.Type, entityType)
		}
		if event.Entity.Name != entityName {
			t.Errorf("%s entity name = %q, want %q", verb, event.Entity.Name, entityName)
		}
		return
	}
	t.Errorf("missing %s event", verb)
}
