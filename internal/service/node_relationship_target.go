package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// isMemberTarget reads org_members through s.members, and the runtime passes
// the cached membership repository. A removed member stays a valid target
// until AuthMembershipCacheLifetime expires.
func (s *NodeService) isMemberTarget(ctx context.Context, rel *node.Relationship) (bool, error) {
	resolved, err := s.reader.Resolve(ctx, rel.TargetID)
	if err == nil && resolved != nil {
		return false, nil
	}
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		wrapped := fmt.Errorf("resolve relationship target %s: %w", rel.TargetID, err)
		slog.ErrorContext(ctx, "node.relationship.target_resolve_failed", slog.String("err", wrapped.Error()))
		return false, wrapped
	}
	orgIDs, err := s.members.ListOrgIDsForUser(ctx, rel.TargetID)
	if err != nil {
		wrapped := fmt.Errorf("read the organizations of relationship target %s: %w", rel.TargetID, err)
		slog.ErrorContext(ctx, "node.relationship.target_members_failed", slog.String("err", wrapped.Error()))
		return false, wrapped
	}
	return slices.Contains(orgIDs, rel.OrgID), nil
}
