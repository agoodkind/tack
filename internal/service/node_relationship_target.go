package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
)

// NodeServiceOption sets an optional dependency of a NodeService.
type NodeServiceOption func(*NodeService)

// WithMembers sets the org_members repository that AddRelationship reads to
// find relationship targets that are members of the source organization.
func WithMembers(members org.MemberRepository) NodeServiceOption {
	return func(nodeService *NodeService) {
		nodeService.members = members
	}
}

// markMemberTarget resolves rel.TargetID before checking membership. It
// returns resolution errors other than domain.ErrNotFound. If the target
// resolves to a node or the service does not have a members repository, it
// returns ctx unchanged. Otherwise, it reads org_members and returns any
// lookup error. When the target belongs to rel.OrgID, it returns a context
// that records the membership. Otherwise, it returns ctx unchanged.
func (s *NodeService) markMemberTarget(ctx context.Context, rel *node.Relationship) (context.Context, error) {
	resolved, err := s.reader.Resolve(ctx, rel.TargetID)
	if err == nil && resolved != nil {
		return ctx, nil
	}
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		wrapped := fmt.Errorf("resolve relationship target %s: %w", rel.TargetID, err)
		slog.ErrorContext(ctx, "node.relationship.target_resolve_failed", slog.String("err", wrapped.Error()))
		return ctx, wrapped
	}
	if s.members == nil {
		return ctx, nil
	}
	orgIDs, err := s.members.ListOrgIDsForUser(ctx, rel.TargetID)
	if err != nil {
		wrapped := fmt.Errorf("read the organizations of relationship target %s: %w", rel.TargetID, err)
		slog.ErrorContext(ctx, "node.relationship.target_members_failed", slog.String("err", wrapped.Error()))
		return ctx, wrapped
	}
	if !slices.Contains(orgIDs, rel.OrgID) {
		return ctx, nil
	}
	return node.WithMemberTarget(ctx, rel.OrgID, rel.TargetID), nil
}
