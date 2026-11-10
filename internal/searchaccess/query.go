package searchaccess

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// ErrAccessDenied means the caller may not search under the entry point.
var ErrAccessDenied = errors.New("search access is denied")

// EntryAuthority returns the permission authority of one entry point from
// FoundationDB. Every registered version shares the organization authority.
func (s *PolicySet) EntryAuthority(ctx context.Context, entryPointID uuid.UUID) (uuid.UUID, error) {
	compiler, err := s.compiler(ctx, StableVersion, entryPointID)
	if err != nil {
		return uuid.Nil, err
	}
	authority, err := compiler.EntryAuthority(ctx, entryPointID)
	if err != nil {
		return uuid.Nil, WithContext("resolve search entry point "+entryPointID.String()+" authority", err)
	}
	return authority, nil
}

// Query returns the requested version and sorted opaque caller keys for one
// query. A new session requests the authority's active version, and a
// continuation requests the version its session stored. The keys use the
// same [EncodeKey] contract as indexed pages.
func (s *PolicySet) Query(ctx context.Context, request AccessRequest) (search.AccessFilter, error) {
	compiler, err := s.compiler(ctx, request.Version, request.EntryPointID)
	if err != nil {
		return search.AccessFilter{}, err
	}
	keys, err := compiler.Query(ctx, request)
	if err != nil {
		return search.AccessFilter{}, WithContext("compile caller access for entry point "+request.EntryPointID.String(), err)
	}
	slices.Sort(keys)
	filter := search.AccessFilter{Version: request.Version, Keys: slices.Compact(keys)}
	if err := search.ValidateAccessFilter(filter); err != nil {
		wrapped := fmt.Errorf("validate caller access for entry point %s: %w", request.EntryPointID, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.access.query_invalid",
			slog.String("err", wrapped.Error()), slog.String("entry_point_id", request.EntryPointID.String()))
		return search.AccessFilter{}, loggedAccessError{err: wrapped}
	}
	return filter, nil
}

// ResourceAccess is the current access of one resource. Keys lists its
// sorted opaque keys under every registered policy version. A resource with
// a hierarchy defect has no keys, and Defect wraps [ErrHierarchyDefect].
type ResourceAccess struct {
	Keys   []string
	Defect error
}

// BatchResourceKeys compiles the current opaque keys of every resource under
// every registered policy version. It resolves every entry point once through
// reader with the [EntryPoints] rule that indexing uses. A failed read
// returns an error for the whole batch.
func (s *PolicySet) BatchResourceKeys(ctx context.Context, reader HierarchyReader, resources []OrgNode) (map[OrgNode]ResourceAccess, error) {
	entries, defects, err := EntryPoints(ctx, reader, resources)
	if err != nil {
		return nil, WithContext("resolve search entry points", err)
	}
	versions := slices.Sorted(maps.Keys(s.compilers))
	accesses := make(map[OrgNode]ResourceAccess, len(resources))
	for _, resource := range resources {
		if defect := defects[resource]; defect != nil {
			accesses[resource] = ResourceAccess{Keys: nil, Defect: defect}
			continue
		}
		keys := make([]string, 0, len(versions))
		for _, version := range versions {
			key, err := s.compilers[version].GrantKey(resource.OrgID, entries[resource])
			if err != nil {
				return nil, entryPointFailure(ctx, resource.NodeID, "encode search access under "+version, err)
			}
			keys = append(keys, key)
		}
		slices.Sort(keys)
		accesses[resource] = ResourceAccess{Keys: slices.Compact(keys), Defect: nil}
	}
	return accesses, nil
}
