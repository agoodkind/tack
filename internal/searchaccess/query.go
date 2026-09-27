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

// activeVersion returns the policy version that serves queries for an
// authority. The organization-scope policy is the only registered version
// until a policy rollout records a candidate.
func (s *PolicySet) activeVersion() string { return StableVersion }

// EntryAuthority returns the permission authority of one entry point from
// FoundationDB.
func (s *PolicySet) EntryAuthority(ctx context.Context, entryPointID uuid.UUID) (uuid.UUID, error) {
	compiler, err := s.compiler(ctx, s.activeVersion(), entryPointID)
	if err != nil {
		return uuid.Nil, err
	}
	authority, err := compiler.EntryAuthority(ctx, entryPointID)
	if err != nil {
		return uuid.Nil, WithContext("resolve search entry point "+entryPointID.String()+" authority", err)
	}
	return authority, nil
}

// Query returns the active version and sorted opaque caller keys for one
// query. The keys use the same [EncodeKey] contract as indexed pages.
func (s *PolicySet) Query(ctx context.Context, request AccessRequest) (search.AccessFilter, error) {
	request.Version = s.activeVersion()
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

// ResourceKeys compiles the current opaque keys of one resource under every
// registered policy version from current FoundationDB data.
func (s *PolicySet) ResourceKeys(ctx context.Context, orgID, resourceID uuid.UUID) ([]string, error) {
	keys := make([]string, 0, len(s.compilers))
	for _, version := range slices.Sorted(maps.Keys(s.compilers)) {
		access, err := s.Index(ctx, IndexAccessRequest{Version: version, OrganizationID: orgID, ResourceID: resourceID, Generation: 0})
		if err != nil {
			return nil, err
		}
		keys = append(keys, access.Keys...)
	}
	slices.Sort(keys)
	return slices.Compact(keys), nil
}
