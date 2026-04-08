package org

import (
	"context"

	"github.com/google/uuid"
)

type Repository interface {
	Create(ctx context.Context, o *Org) (*Org, error)
	GetByID(ctx context.Context, id uuid.UUID) (*Org, error)
	GetBySlug(ctx context.Context, slug string) (*Org, error)

	AddMember(ctx context.Context, m *Member) error
	RemoveMember(ctx context.Context, orgID, userID uuid.UUID) error
	ListMembers(ctx context.Context, orgID uuid.UUID) ([]*Member, error)
}
