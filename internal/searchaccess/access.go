// Package searchaccess compiles policy decisions into opaque OpenSearch values.
package searchaccess

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// StableVersion is the organization-scope policy version every node starts
// with before a policy rollout records another write version.
const StableVersion = "org-scope-v1"

// RotatedVersion is the organization-scope policy under a rotated key
// namespace. An access rollout to it replaces every indexed and caller key
// without changing the access decision.
const RotatedVersion = "org-scope-v2"

// IndexAccessRequest identifies the policy version and source resource being indexed.
type IndexAccessRequest struct {
	Version                    string
	OrganizationID, ResourceID uuid.UUID
	Generation                 int64
}

// AccessRequest identifies the caller, the organizations the caller belongs
// to, and the entry point one query searches under.
type AccessRequest struct {
	Version                                string
	PrincipalID, AuthorityID, EntryPointID uuid.UUID
	MemberOrganizations                    []uuid.UUID
}

// Compiler compiles one opaque policy version. Its Dependents method reads
// one bounded page of the resources that derive their access from a resource
// under this policy. The page starts after an opaque cursor. EntryAuthority
// returns the permission authority of an entry point, and Query returns the
// opaque caller keys of one query. GrantKey encodes the key of one resolved
// organization and entry-point grant.
type Compiler interface {
	Version() string
	Index(context.Context, IndexAccessRequest) (node.SearchAccess, error)
	GrantKey(orgID, entryPointID uuid.UUID) (string, error)
	Dependents(ctx context.Context, orgID, resourceID uuid.UUID, cursor string, limit int) (node.IDPage, error)
	EntryAuthority(context.Context, uuid.UUID) (uuid.UUID, error)
	Query(context.Context, AccessRequest) ([]string, error)
}

// PolicySet dispatches policy compilation by its explicit version.
type PolicySet struct {
	compilers map[string]Compiler
}

// NewPolicySet registers the production organization-scope policy under
// both of its key namespaces.
func NewPolicySet(reader node.NodeReader, types TypeReader, relationships RelationshipReader) *PolicySet {
	compilers := make(map[string]Compiler, 2)
	for _, version := range []string{StableVersion, RotatedVersion} {
		compilers[version] = NewOrgScopeCompiler(version, reader, types, relationships)
	}
	return &PolicySet{compilers: compilers}
}

// Supports reports whether a compiler is registered for version.
func (s *PolicySet) Supports(version string) bool {
	_, registered := s.compilers[version]
	return registered
}

// Index returns sorted opaque versions and keys for one resource.
func (s *PolicySet) Index(ctx context.Context, request IndexAccessRequest) (node.SearchAccess, error) {
	compiler, err := s.compiler(ctx, request.Version, request.ResourceID)
	if err != nil {
		return node.SearchAccess{}, err
	}
	access, err := compiler.Index(ctx, request)
	if err != nil {
		var logged loggedAccessError
		if errors.As(err, &logged) {
			return node.SearchAccess{}, WithContext("compile search access for resource "+request.ResourceID.String(), err)
		}
		wrapped := fmt.Errorf("compile search access for resource %s: %w", request.ResourceID, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.access.compile_failed",
			slog.String("err", wrapped.Error()), slog.String("resource_id", request.ResourceID.String()))
		return node.SearchAccess{}, loggedAccessError{err: wrapped}
	}
	if len(access.Versions) == 0 || len(access.Keys) == 0 || access.Generation < 0 ||
		!slices.IsSorted(access.Versions) || !slices.IsSorted(access.Keys) ||
		slices.Contains(access.Versions, "") || slices.Contains(access.Keys, "") ||
		len(slices.Compact(slices.Clone(access.Versions))) != len(access.Versions) ||
		len(slices.Compact(slices.Clone(access.Keys))) != len(access.Keys) || access.Versions[0] != request.Version {
		wrapped := fmt.Errorf("validate search access for resource %s: versions and keys must be nonempty, unique, sorted, and include the policy version", request.ResourceID)
		telemetry.L(ctx).ErrorContext(ctx, "search.access.invalid",
			slog.String("err", wrapped.Error()), slog.String("resource_id", request.ResourceID.String()))
		return node.SearchAccess{}, loggedAccessError{err: wrapped}
	}
	return access, nil
}

// Dependents reads one bounded page of the resources that derive their
// access from resourceID. It reads each version of versions in order. The
// cursor records the version position and that compiler's cursor.
func (s *PolicySet) Dependents(ctx context.Context, versions []string, orgID, resourceID uuid.UUID, cursor string, limit int) (node.IDPage, error) {
	position, inner, err := parseDependentCursor(cursor)
	if err != nil {
		wrapped := fmt.Errorf("parse dependent cursor of resource %s: %w", resourceID, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.access.dependent_cursor_invalid",
			slog.String("err", wrapped.Error()), slog.String("resource_id", resourceID.String()))
		return node.IDPage{}, loggedAccessError{err: wrapped}
	}
	if position >= len(versions) {
		return node.IDPage{IDs: []uuid.UUID{}, NextCursor: "", Done: true}, nil
	}
	compiler, err := s.compiler(ctx, versions[position], resourceID)
	if err != nil {
		return node.IDPage{}, err
	}
	page, err := compiler.Dependents(ctx, orgID, resourceID, inner, limit)
	if err != nil {
		return node.IDPage{}, WithContext("list search access dependents under "+versions[position], err)
	}
	switch {
	case !page.Done:
		page.NextCursor = formatDependentCursor(position, page.NextCursor)
	case position+1 < len(versions):
		page.NextCursor = formatDependentCursor(position+1, "")
		page.Done = false
	default:
		page.NextCursor = ""
	}
	return page, nil
}

func formatDependentCursor(position int, inner string) string {
	return strconv.Itoa(position) + ":" + inner
}

func parseDependentCursor(cursor string) (int, string, error) {
	if cursor == "" {
		return 0, "", nil
	}
	positionText, inner, found := strings.Cut(cursor, ":")
	if !found {
		return 0, "", errors.New("dependent cursor has no version position")
	}
	position, err := strconv.Atoi(positionText)
	if err != nil || position < 0 {
		return 0, "", fmt.Errorf("dependent cursor version position %q is invalid", positionText)
	}
	return position, inner, nil
}

func (s *PolicySet) compiler(ctx context.Context, version string, resourceID uuid.UUID) (Compiler, error) {
	compiler := s.compilers[version]
	if compiler == nil {
		wrapped := fmt.Errorf("compile search access version %q for resource %s: unsupported policy", version, resourceID)
		telemetry.L(ctx).ErrorContext(ctx, "search.access.policy_unsupported",
			slog.String("err", wrapped.Error()), slog.String("version", version))
		return nil, loggedAccessError{err: wrapped}
	}
	return compiler, nil
}

type loggedAccessError struct{ err error }

func (e loggedAccessError) Error() string { return e.err.Error() }
func (e loggedAccessError) Unwrap() error { return e.err }

type accessContextError struct {
	operation string
	err       error
}

func (e accessContextError) Error() string { return e.operation + ": " + e.err.Error() }
func (e accessContextError) Unwrap() error { return e.err }

// WithContext prefixes err with operation and logs no event.
func WithContext(operation string, err error) error {
	return accessContextError{operation: operation, err: err}
}
