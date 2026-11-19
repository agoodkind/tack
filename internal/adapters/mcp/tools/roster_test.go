package tools

import (
	"context"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/org"
)

type fakeMembers struct {
	orgIDs []uuid.UUID
}

func (m *fakeMembers) AddMember(context.Context, *org.Member) error {
	panic("fakeMembers.AddMember called")
}

func (m *fakeMembers) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error {
	panic("fakeMembers.RemoveMember called")
}

func (m *fakeMembers) ListMembers(context.Context, uuid.UUID) ([]*org.Member, error) {
	panic("fakeMembers.ListMembers called")
}

func (m *fakeMembers) ListOrgIDsForUser(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return m.orgIDs, nil
}
