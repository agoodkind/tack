package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/user"
)

// TestRenderNodeIdentifierPreferred asserts that an issue under a project
// renders with a "<PROJECT>-<seq>" identifier and pretty-printed scope and
// parent fields, with the raw UUID at the bottom for round-tripping.
func TestRenderNodeIdentifierPreferred(t *testing.T) {
	now := time.Date(2026, 4, 28, 5, 30, 0, 0, time.UTC)
	orgID := uuid.New()
	userID := uuid.New()
	projectID := uuid.New()
	epicID := uuid.New()
	stateID := uuid.New()
	issueID := uuid.New()

	project := &node.NodeView{
		ID:       projectID,
		OrgID:    orgID,
		NodeType: "project",
		Name:     "Tack",
		Props: map[string]json.RawMessage{
			"slug":       mustRaw(t, "tack"),
			"identifier": mustRaw(t, "TACK"),
		},
	}
	epic := &node.NodeView{
		ID:       epicID,
		OrgID:    orgID,
		NodeType: "epic",
		Name:     "TUI work",
		Props: map[string]json.RawMessage{
			"sequence": mustRaw(t, 12),
			"scope_id": mustRaw(t, projectID.String()),
		},
	}
	state := &node.NodeView{
		ID:       stateID,
		OrgID:    orgID,
		NodeType: "state",
		Name:     "in-progress",
	}
	issue := &node.NodeView{
		ID:        issueID,
		OrgID:     orgID,
		NodeType:  "issue",
		Name:      "Fix the resolver bug",
		CreatedAt: now,
		UpdatedAt: now,
		CreatedBy: userID,
		UpdatedBy: userID,
		Props: map[string]json.RawMessage{
			"sequence": mustRaw(t, 65),
			"scope_id": mustRaw(t, projectID.String()),
			"state_id": mustRaw(t, stateID.String()),
			"priority": mustRaw(t, "high"),
		},
	}

	reader := &fakeReader{
		parents: map[uuid.UUID]uuid.UUID{issueID: epicID, epicID: projectID},
		views: map[uuid.UUID]*node.NodeView{
			projectID: project,
			epicID:    epic,
			stateID:   state,
			issueID:   issue,
		},
	}
	users := &fakeUsers{
		users: map[uuid.UUID]*user.User{
			userID: {ID: userID, DisplayName: "alex", Email: "alex@goodkind.io"},
		},
	}

	rc := newRenderCtxWithTypes(
		context.Background(),
		reader,
		users,
		map[string]*node.NodeType{
			"project": {
				TypeKey: "project",
				Reference: node.ReferenceConfig{
					Strategy: node.ReferenceDirectProperty,
					Property: "identifier",
				},
			},
			"epic": {
				TypeKey: "epic",
				Reference: node.ReferenceConfig{
					Strategy: node.ReferenceScopedSequence,
					Property: "sequence",
				},
			},
			"issue": {
				TypeKey: "issue",
				Reference: node.ReferenceConfig{
					Strategy: node.ReferenceScopedSequence,
					Property: "sequence",
				},
			},
		},
	)
	out := renderNode(rc, issue)

	mustContain(t, out, "TACK-65")                          // identifier in header
	mustContain(t, out, "Fix the resolver bug")             // name in header
	mustContain(t, out, "- Scope:")                         // resolved scope label
	mustContain(t, out, "`TACK` (`project`)")               // resolved scope value
	mustContain(t, out, "- Parent:")                        // resolved parent label
	mustContain(t, out, "`TACK-12` (`epic`)")               // resolved parent identifier
	mustContain(t, out, "- State:")                         // resolved state label
	mustContain(t, out, "`in-progress` (`state`)")          // resolved state value
	mustContain(t, out, "- Priority: high")                 // scalar prop
	mustContain(t, out, "- Created:")                       // audit field
	mustContain(t, out, "by alex")                          // resolved user
	mustContain(t, out, "- Raw id: `"+issueID.String()+"`") // raw id at bottom

	// No raw UUIDs for cross-references should appear in the body.
	for _, raw := range []string{projectID.String(), epicID.String(), stateID.String()} {
		if strings.Contains(out, raw) {
			t.Errorf("rendered output should not contain raw cross-ref UUID %s; got:\n%s", raw, out)
		}
	}
}
