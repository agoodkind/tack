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

// fakeReader is a tiny in-memory NodeReader that lets render_test assert
// cross-reference resolution without standing up FDB. Only Get is used by the
// renderer; the other methods panic to make accidental calls obvious.
type fakeReader struct {
	views map[uuid.UUID]*node.NodeView
}

func (r *fakeReader) Get(_ context.Context, id uuid.UUID) (*node.NodeView, error) {
	return r.views[id], nil
}
func (r *fakeReader) Resolve(context.Context, uuid.UUID) (*node.NodeResolve, error) {
	panic("fakeReader.Resolve called")
}
func (r *fakeReader) List(context.Context, node.NodeListQuery) ([]*node.NodeView, error) {
	panic("fakeReader.List called")
}
func (r *fakeReader) Stream(context.Context, node.NodeListQuery) (<-chan node.NodeStreamResult, error) {
	panic("fakeReader.Stream called")
}

type fakeUsers struct {
	users map[uuid.UUID]*user.User
}

func (u *fakeUsers) GetByID(_ context.Context, id uuid.UUID) (*user.User, error) {
	return u.users[id], nil
}
func (u *fakeUsers) GetByEmail(context.Context, string) (*user.User, error) {
	panic("fakeUsers.GetByEmail called")
}
func (u *fakeUsers) Create(context.Context, *user.User) (*user.User, error) {
	panic("fakeUsers.Create called")
}

func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

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
			"sequence":  mustRaw(t, 65),
			"parent_id": mustRaw(t, epicID.String()),
			"scope_id":  mustRaw(t, projectID.String()),
			"state_id":  mustRaw(t, stateID.String()),
			"priority":  mustRaw(t, "high"),
		},
	}

	reader := &fakeReader{views: map[uuid.UUID]*node.NodeView{
		projectID: project,
		epicID:    epic,
		stateID:   state,
		issueID:   issue,
	}}
	users := &fakeUsers{users: map[uuid.UUID]*user.User{
		userID: {ID: userID, DisplayName: "alex", Email: "alex@goodkind.io"},
	}}

	rc := newRenderCtxWithTypes(context.Background(), reader, users, map[string]*node.NodeType{
		"project": {TypeKey: "project", Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectSlug, Property: "identifier"}},
		"epic":    {TypeKey: "epic", Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedSequence, Property: "sequence"}},
		"issue":   {TypeKey: "issue", Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedSequence, Property: "sequence"}},
	})
	out := renderNode(rc, issue)

	mustContain(t, out, "TACK-65")                        // identifier in header
	mustContain(t, out, "Fix the resolver bug")           // name in header
	mustContain(t, out, "scope:")                         // resolved scope label
	mustContain(t, out, "TACK (project)")                 // resolved scope value
	mustContain(t, out, "parent:")                        // resolved parent label
	mustContain(t, out, "TACK-12 (epic)")                 // resolved parent identifier (epic carries its own seq)
	mustContain(t, out, "state:")                         // resolved state label
	mustContain(t, out, "in-progress (state)")            // resolved state value
	mustContain(t, out, "priority:    high")              // scalar prop
	mustContain(t, out, "created:")                       // audit field
	mustContain(t, out, "by alex")                        // resolved user
	mustContain(t, out, "id:          "+issueID.String()) // raw id at bottom

	// No raw UUIDs for cross-references should appear in the body.
	for _, raw := range []string{projectID.String(), epicID.String(), stateID.String()} {
		if strings.Contains(out, raw) {
			t.Errorf("rendered output should not contain raw cross-ref UUID %s; got:\n%s", raw, out)
		}
	}
}

// TestRenderListUsesIdentifiers asserts that a list of issues prints
// identifiers and resolved state names rather than raw UUIDs.
func TestRenderListUsesIdentifiers(t *testing.T) {
	orgID := uuid.New()
	projectID := uuid.New()
	stateID := uuid.New()
	project := &node.NodeView{
		ID: projectID, OrgID: orgID, NodeType: "project", Name: "Tack",
		Props: map[string]json.RawMessage{"identifier": mustRaw(t, "TACK")},
	}
	stateView := &node.NodeView{
		ID: stateID, OrgID: orgID, NodeType: "state", Name: "in-progress",
	}
	issues := make([]*node.NodeView, 0, 3)
	for i := 1; i <= 3; i++ {
		issues = append(issues, &node.NodeView{
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
		})
	}
	reader := &fakeReader{views: map[uuid.UUID]*node.NodeView{
		projectID: project,
		stateID:   stateView,
	}}
	rc := newRenderCtxWithTypes(context.Background(), reader, nil, map[string]*node.NodeType{
		"project": {TypeKey: "project", Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectSlug, Property: "identifier"}},
		"issue":   {TypeKey: "issue", Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedSequence, Property: "sequence"}},
	})
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

func TestRenderSearchViewsUsesIdentifiers(t *testing.T) {
	orgID := uuid.New()
	projectID := uuid.New()
	issueID := uuid.New()
	project := &node.NodeView{
		ID:       projectID,
		OrgID:    orgID,
		NodeType: "project",
		Name:     "Clyde",
		Props:    map[string]json.RawMessage{"identifier": mustRaw(t, "CLYDE")},
	}
	issue := &node.NodeView{
		ID:       issueID,
		OrgID:    orgID,
		NodeType: "issue",
		Name:     "Search result",
		Props: map[string]json.RawMessage{
			"sequence": mustRaw(t, 140),
			"scope_id": mustRaw(t, projectID.String()),
		},
	}
	reader := &fakeReader{views: map[uuid.UUID]*node.NodeView{projectID: project}}
	rc := newRenderCtxWithTypes(context.Background(), reader, nil, map[string]*node.NodeType{
		"project": {TypeKey: "project", Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectSlug, Property: "identifier"}},
		"issue":   {TypeKey: "issue", Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedSequence, Property: "sequence"}},
	})
	out := renderSearchViews(rc, []*node.NodeView{issue})

	mustContain(t, out, "CLYDE-140")
	if strings.Contains(out, issueID.String()) {
		t.Fatalf("search output should not expose UUID as primary row identity:\n%s", out)
	}
}

func mustContain(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Errorf("expected output to contain %q, got:\n%s", want, body)
	}
}
