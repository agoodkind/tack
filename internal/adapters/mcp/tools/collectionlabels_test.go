package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// TestRenderListUsesIdentifiers asserts that a list of issues prints
// identifiers and resolved state names rather than raw UUIDs.
func TestRenderListUsesIdentifiers(t *testing.T) {
	orgID := uuid.New()
	projectID := uuid.New()
	stateID := uuid.New()
	project := &node.NodeView{
		ID:       projectID,
		OrgID:    orgID,
		NodeType: "project",
		Name:     "Tack",
		Props:    map[string]json.RawMessage{"identifier": mustRaw(t, "TACK")},
	}
	stateView := &node.NodeView{
		ID:       stateID,
		OrgID:    orgID,
		NodeType: "state",
		Name:     "in-progress",
	}
	issues := make([]*node.NodeView, 0, 3)

	for i := 1; i <= 3; i++ {
		issues = append(
			issues,
			&node.NodeView{
				ID:       uuid.New(),
				OrgID:    orgID,
				NodeType: "issue",
				Name:     "issue " + string(rune('A'+i-1)),
				Props: map[string]json.RawMessage{
					"sequence": mustRaw(t, i),
					"scope_id": mustRaw(t, projectID.String()),
					"state_id": mustRaw(t, stateID.String()),
					"priority": mustRaw(t, "medium"),
				},
			},
		)
	}

	reader := &fakeReader{
		views: map[uuid.UUID]*node.NodeView{
			projectID: project,
			stateID:   stateView,
		},
	}
	rc := newRenderCtxWithTypes(
		context.Background(),
		reader,
		nil,
		map[string]*node.NodeType{
			"project": {
				TypeKey: "project",
				Reference: node.ReferenceConfig{
					Strategy: node.ReferenceDirectProperty,
					Property: "identifier",
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
	out := renderList(rc, "issues", issues)

	mustContain(t, out, "3 issues")
	mustContain(t, out, "TACK-1")
	mustContain(t, out, "TACK-2")
	mustContain(t, out, "TACK-3")
	mustContain(t, out, "in-progress")
	if strings.Contains(out, projectID.String()) || strings.Contains(out, stateID.String()) {
		t.Errorf("list output should not contain raw cross-ref UUIDs:\n%s", out)
	}
}
