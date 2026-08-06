package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

const (
	scopedNodeRefSeparator = "::"
	maxParentDepth         = 32
)

// ResolveTypedNodeID resolves a node reference in the context of one node type.
// Typed resolution lets get/update/delete accept declared references and scoped
// references like "CLYDE::In Progress" without widening lookups across all types.
func (r *Resolver) ResolveTypedNodeID(ctx context.Context, nt *node.NodeType, input string) (uuid.UUID, error) {
	if id, err := uuid.Parse(input); err == nil {
		if nt == nil {
			return id, nil
		}
		resolve, err := r.reader.Resolve(ctx, id)
		if err != nil {
			return uuid.Nil, err
		}
		if resolve == nil || resolve.NodeType != nt.TypeKey {
			return uuid.Nil, fmt.Errorf("%s %q: %w", strings.ToLower(nt.Slug), input, domain.ErrNotFound)
		}
		stampAuditNodeResolve(ctx, resolve)
		return id, nil
	}
	if nt == nil {
		return r.ResolveNodeID(ctx, input)
	}

	switch nt.Reference.Strategy {
	case node.ReferenceDirectProperty:
		return r.resolveDirectReference(ctx, nt, input)
	case node.ReferenceScopedSequence:
		return r.resolveSequenceNodeID(ctx, input, []string{nt.TypeKey})
	case node.ReferenceScopedProperty:
		return r.resolveScopedNodeReference(ctx, nt, input)
	case node.ReferenceUUIDOnly, "":
		return uuid.Nil, invalidTypedNodeIDError(nt, input)
	default:
		return uuid.Nil, fmt.Errorf("unknown reference strategy %q for %s: %w", nt.Reference.Strategy, nt.Slug, domain.ErrInvalidArgument)
	}
}

func (r *Resolver) resolveSequenceNodeID(ctx context.Context, input string, typeKeys []string) (uuid.UUID, error) {
	userID, ok := auth.UserID(ctx)
	if !ok {
		return uuid.Nil, fmt.Errorf("unauthenticated")
	}
	workspaces, err := r.WorkspacesForUser(ctx, userID)
	if err != nil {
		return uuid.Nil, err
	}
	parsed := false
	projectReference := ""
	seqID := 0
	for _, workspace := range workspaces {
		id, found, lookupErr := r.referenceKeyHolder(ctx, workspace.OrgID, typeKeys, input)
		if lookupErr != nil {
			return uuid.Nil, lookupErr
		}
		if found {
			return id, nil
		}
		if !parsed {
			projectReference, seqID, err = r.parseSequenceReference(input, typeKeys)
			if err != nil {
				return uuid.Nil, fmt.Errorf("invalid node_id %q: must be a UUID or sequence reference like TACK-65: %w", input, domain.ErrInvalidArgument)
			}
			parsed = true
		}
		if len(r.scopeChain) == 0 {
			continue
		}
		scopeNode, scopeErr := r.ResolveScope(ctx, workspace, r.scopeChain[0], projectReference)
		if scopeErr != nil {
			continue
		}
		id, matchErr := r.scanSequenceCandidates(ctx, workspace.OrgID, typeKeys, seqID, scopeNode.ID, input)
		if matchErr != nil {
			if errors.Is(matchErr, domain.ErrInvalidArgument) {
				return uuid.Nil, matchErr
			}
			continue
		}
		return id, nil
	}
	return uuid.Nil, fmt.Errorf("reference %q: %w", input, domain.ErrNotFound)
}
