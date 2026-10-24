package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
)

// stagedIntents returns the FoundationDB outbox payloads that a node update
// staged through StageStateChange and a cascading delete descendant produce.
func stagedIntents(ctx context.Context, orgID, userID uuid.UUID) ([]json.RawMessage, error) {
	staging := audit.WithScopeBuilder(auditintent.WithSlot(ctx, "tack_update_issue", userID))
	audit.SetScopeFields(staging, audit.Scope{OrgID: orgID, WorkspaceID: uuid.Must(uuid.NewV7()), ScopeID: uuid.Nil, ParentID: uuid.Nil})
	entity := audit.Entity{Type: "node", NodeType: "issue", ID: uuid.Must(uuid.NewV7()), Identifier: "LEG-2", Name: "Staged issue"}
	if err := audit.StageStateChange(staging, audit.VerbNodeUpdate, entity); err != nil {
		return nil, err
	}
	payload, ok := auditintent.Pending(staging)
	if !ok {
		return nil, errors.New("StageStateChange staged nothing")
	}
	child := audit.Entity{Type: "node", NodeType: "issue", ID: uuid.Must(uuid.NewV7()), Identifier: "LEG-3", Name: "Deleted child"}
	descendant, err := audit.DescendantDeleteEvent(payload, child)
	if err != nil {
		return nil, err
	}
	return []json.RawMessage{append(json.RawMessage(nil), payload...), descendant}, nil
}
