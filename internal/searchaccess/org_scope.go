package searchaccess

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// maxDependentPage bounds one page of hierarchy dependents.
const maxDependentPage = 100

// RelationshipReader reads bounded pages of the nodes at the other end of a
// node's relationships of one type. The policy reads only child_of edges.
// NodeType metadata then decides which child_of edges form the hierarchy.
type RelationshipReader interface {
	EdgesFrom(ctx context.Context, orgID, sourceID uuid.UUID, relationType, cursor string, limit int) (node.IDPage, error)
	EdgesTo(ctx context.Context, orgID, targetID uuid.UUID, relationType, cursor string, limit int) (node.IDPage, error)
}

// TypeReader reads one node type by its type key with bounded reads.
type TypeReader interface {
	TypeByKey(ctx context.Context, orgID uuid.UUID, typeKey string) (*node.NodeType, error)
}

// OrgScopeCompiler compiles an organization and entry-point grant into one
// opaque key. Each registered version encodes the same decision under its
// own key namespace. A rollout to another version replaces every key.
type OrgScopeCompiler struct {
	version       string
	reader        node.NodeReader
	types         TypeReader
	relationships RelationshipReader
}

// NewOrgScopeCompiler builds the production organization-scope compiler of
// one policy version.
func NewOrgScopeCompiler(version string, reader node.NodeReader, types TypeReader, relationships RelationshipReader) *OrgScopeCompiler {
	return &OrgScopeCompiler{version: version, reader: reader, types: types, relationships: relationships}
}

// Version returns this compiler's policy version.
func (c *OrgScopeCompiler) Version() string { return c.version }

// Dependents reads one bounded page of the hierarchy children of resourceID
// and returns their node IDs. A child is the source node of a child_of edge
// to resourceID, and node.LivesUnder accepts the pair of node types.
func (c *OrgScopeCompiler) Dependents(ctx context.Context, orgID, resourceID uuid.UUID, cursor string, limit int) (node.IDPage, error) {
	if c.reader == nil || c.types == nil || c.relationships == nil || limit < 1 || limit > maxDependentPage {
		wrapped := fmt.Errorf("list hierarchy children of resource %s: policy dependencies and a limit between 1 and %d are required", resourceID, maxDependentPage)
		telemetry.L(ctx).ErrorContext(ctx, "search.access.dependents_invalid",
			slog.String("err", wrapped.Error()), slog.String("resource_id", resourceID.String()))
		return node.IDPage{}, loggedAccessError{err: wrapped}
	}
	return c.children(ctx, orgID, resourceID, cursor, limit)
}

// ResourceAuthority reads the resource's organization from FoundationDB.
func (c *OrgScopeCompiler) ResourceAuthority(ctx context.Context, resourceID uuid.UUID) (uuid.UUID, error) {
	if c.reader == nil {
		wrapped := fmt.Errorf("read search resource %s: node reader is unavailable", resourceID)
		telemetry.L(ctx).ErrorContext(ctx, "search.access.authority_failed",
			slog.String("err", wrapped.Error()), slog.String("resource_id", resourceID.String()))
		return uuid.Nil, loggedAccessError{err: wrapped}
	}
	resolved, err := c.reader.Resolve(ctx, resourceID)
	if err != nil {
		wrapped := fmt.Errorf("resolve search resource %s authority: %w", resourceID, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.access.authority_failed",
			slog.String("err", wrapped.Error()), slog.String("resource_id", resourceID.String()))
		return uuid.Nil, loggedAccessError{err: wrapped}
	}
	if resolved == nil || resolved.OrgID == uuid.Nil {
		wrapped := fmt.Errorf("resolve search resource %s authority: organization authority is missing", resourceID)
		telemetry.L(ctx).ErrorContext(ctx, "search.access.authority_failed",
			slog.String("err", wrapped.Error()), slog.String("resource_id", resourceID.String()))
		return uuid.Nil, loggedAccessError{err: wrapped}
	}
	return resolved.OrgID, nil
}

// Index compiles one organization and entry-point pair without interpreting its key.
func (c *OrgScopeCompiler) Index(ctx context.Context, request IndexAccessRequest) (node.SearchAccess, error) {
	if request.Version != c.version || request.OrganizationID == uuid.Nil ||
		request.ResourceID == uuid.Nil || request.Generation < 0 {
		wrapped := fmt.Errorf("index search access: invalid version, identity, or generation")
		telemetry.L(ctx).ErrorContext(ctx, "search.access.index_invalid",
			slog.String("err", wrapped.Error()), slog.String("resource_id", request.ResourceID.String()))
		return node.SearchAccess{}, loggedAccessError{err: wrapped}
	}
	authority, err := c.ResourceAuthority(ctx, request.ResourceID)
	if err != nil {
		return node.SearchAccess{}, err
	}
	if authority != request.OrganizationID {
		wrapped := fmt.Errorf("index search resource %s: organization authority mismatch", request.ResourceID)
		telemetry.L(ctx).ErrorContext(ctx, "search.access.authority_mismatch",
			slog.String("err", wrapped.Error()), slog.String("resource_id", request.ResourceID.String()))
		return node.SearchAccess{}, loggedAccessError{err: wrapped}
	}
	entryPointID, err := c.entryPoint(ctx, request.OrganizationID, request.ResourceID)
	if err != nil {
		return node.SearchAccess{}, WithContext("resolve search resource "+request.ResourceID.String()+" entry point", err)
	}
	key, err := EncodeKey(request.Version, request.OrganizationID[:], entryPointID[:])
	if err != nil {
		wrapped := fmt.Errorf("encode search access for resource %s: %w", request.ResourceID, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.access.key_encode_failed",
			slog.String("err", wrapped.Error()), slog.String("resource_id", request.ResourceID.String()))
		return node.SearchAccess{}, loggedAccessError{err: wrapped}
	}
	access := node.SearchAccess{Versions: []string{request.Version}, Keys: []string{key}, Generation: request.Generation}
	return access, nil
}
