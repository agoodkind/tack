package searchaccess

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// EntryAuthority reads the entry point from FoundationDB and returns its
// organization. The node's type must declare the entry-point feature.
func (c *OrgScopeCompiler) EntryAuthority(ctx context.Context, entryPointID uuid.UUID) (uuid.UUID, error) {
	if c.reader == nil || c.types == nil {
		return uuid.Nil, entryAuthorityFailure(ctx, entryPointID, "read entry point", errors.New("search policy dependencies are unavailable"))
	}
	view, err := c.reader.Get(ctx, entryPointID)
	if err != nil {
		return uuid.Nil, entryAuthorityFailure(ctx, entryPointID, "read entry point", err)
	}
	if view == nil || view.OrgID == uuid.Nil {
		return uuid.Nil, entryAuthorityFailure(ctx, entryPointID, "read entry point", ErrAccessDenied)
	}
	nodeType, err := c.types.TypeByKey(ctx, view.OrgID, view.NodeType)
	if err != nil {
		return uuid.Nil, entryAuthorityFailure(ctx, entryPointID, "read node type "+view.NodeType, err)
	}
	if nodeType == nil || !nodeType.Features.Has(node.FeatureIsEntryPoint) {
		return uuid.Nil, entryAuthorityFailure(ctx, entryPointID, "verify entry point type", ErrAccessDenied)
	}
	return view.OrgID, nil
}

// Query returns the one opaque key that grants the caller the entry point.
// The caller must currently belong to the entry point's organization.
func (c *OrgScopeCompiler) Query(ctx context.Context, request AccessRequest) ([]string, error) {
	if request.Version != StableVersion || request.PrincipalID == uuid.Nil || request.EntryPointID == uuid.Nil {
		return nil, entryAuthorityFailure(ctx, request.EntryPointID, "compile caller access", errors.New("invalid version or identity"))
	}
	authority, err := c.EntryAuthority(ctx, request.EntryPointID)
	if err != nil {
		return nil, err
	}
	if authority != request.AuthorityID || !slices.Contains(request.MemberOrganizations, authority) {
		telemetry.L(ctx).InfoContext(ctx, "search.access.query_denied",
			slog.String("entry_point_id", request.EntryPointID.String()), slog.String("principal_id", request.PrincipalID.String()))
		return nil, ErrAccessDenied
	}
	key, err := EncodeKey(request.Version, authority[:], request.EntryPointID[:])
	if err != nil {
		return nil, entryAuthorityFailure(ctx, request.EntryPointID, "encode caller access", err)
	}
	return []string{key}, nil
}

func entryAuthorityFailure(ctx context.Context, entryPointID uuid.UUID, operation string, err error) error {
	wrapped := fmt.Errorf("%s for entry point %s: %w", operation, entryPointID, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.access.entry_authority_failed",
		slog.String("err", wrapped.Error()), slog.String("entry_point_id", entryPointID.String()))
	return loggedAccessError{err: wrapped}
}
