package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/user"
)

// fakeReader is a tiny in-memory NodeReader that lets render_test assert
// cross-reference resolution without standing up FDB. Only Get is used by the
// renderer; List reads parent relationships for reference formatting.
type fakeReader struct {
	views   map[uuid.UUID]*node.NodeView
	parents map[uuid.UUID]uuid.UUID
}

func (r *fakeReader) Get(_ context.Context, id uuid.UUID) (*node.NodeView, error) {
	return r.views[id], nil
}

func (r *fakeReader) Resolve(context.Context, uuid.UUID) (*node.NodeResolve, error) {
	panic("fakeReader.Resolve called")
}

func (r *fakeReader) List(_ context.Context, query node.NodeListQuery) ([]*node.NodeView, error) {
	return parentViews(r.views, r.parents, query), nil
}

func (r *fakeReader) Stream(
	context.Context,
	node.NodeListQuery,
) (<-chan node.NodeStreamResult, error) {
	panic("fakeReader.Stream called")
}

func (r *fakeReader) ListPage(context.Context, node.NodeListQuery) (node.Page, error) {
	panic("fakeReader.ListPage called")
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

func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	return b
}

func mustContain(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Errorf("expected output to contain %q, got:\n%s", want, body)
	}
}
