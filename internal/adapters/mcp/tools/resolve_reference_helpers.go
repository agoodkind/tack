package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func (r *Resolver) referenceSearchRoots(ctx context.Context) ([]*node.NodeView, error) {
	userID, ok := auth.UserID(ctx)
	if !ok {
		return nil, fmt.Errorf("unauthenticated")
	}
	return r.WorkspacesForUser(ctx, userID)
}

func (r *Resolver) referenceScopeLevel(nt *node.NodeType) (ScopeLevel, bool) {
	chain := r.ScopeChainForType(nt)
	if len(chain) == 0 {
		return ScopeLevel{}, false
	}
	return chain[0], true
}

func (r *Resolver) nodeBelongsToScope(ctx context.Context, candidate *node.Node, scopeID uuid.UUID) bool {
	if candidate == nil || scopeID == uuid.Nil {
		return false
	}
	if got := rawUUID(candidate.Props, "scope_id"); got == scopeID {
		return true
	}
	parentID := rawUUID(candidate.Props, "parent_id")
	if parentID == uuid.Nil {
		return false
	}
	if parentID == scopeID {
		return true
	}
	return r.parentChainContains(ctx, parentID, scopeID)
}

func (r *Resolver) parentChainContains(ctx context.Context, startID, wantID uuid.UUID) bool {
	currentID := startID
	visited := map[uuid.UUID]struct{}{}
	for depth := 0; depth < maxParentDepth && currentID != uuid.Nil; depth++ {
		if currentID == wantID {
			return true
		}
		if _, seen := visited[currentID]; seen {
			return false
		}
		visited[currentID] = struct{}{}
		currentView, err := r.reader.Get(ctx, currentID)
		if err != nil || currentView == nil {
			return false
		}
		currentID = uuidProp(currentView, "parent_id")
	}
	return false
}

func viewBelongsToScope(ctx context.Context, resolver *Resolver, view *node.NodeView, scopeID uuid.UUID) bool {
	if view == nil || scopeID == uuid.Nil {
		return false
	}
	if view.ID == scopeID {
		return true
	}
	if uuidProp(view, "scope_id") == scopeID {
		return true
	}
	parentID := uuidProp(view, "parent_id")
	if parentID == uuid.Nil {
		return false
	}
	if parentID == scopeID {
		return true
	}
	return resolver.parentChainContains(ctx, parentID, scopeID)
}

func matchesReferenceProperty(view *node.NodeView, propName, input string) bool {
	if view == nil {
		return false
	}
	if propName == "name" && strings.EqualFold(view.Name, input) {
		return true
	}
	value := stringProp(view, propName)
	return value != "" && strings.EqualFold(value, input)
}

func uniqueMatch(matches map[uuid.UUID]struct{}, kind, input string) (uuid.UUID, error) {
	switch len(matches) {
	case 0:
		return uuid.Nil, fmt.Errorf("%s %q: %w", kind, input, domain.ErrNotFound)
	case 1:
		for id := range matches {
			return id, nil
		}
	}
	return uuid.Nil, fmt.Errorf("%s %q matched multiple nodes: %w", kind, input, domain.ErrInvalidArgument)
}

func invalidTypedNodeIDError(nt *node.NodeType, input string) error {
	switch nt.Reference.Strategy {
	case node.ReferenceScopedSequence:
		return fmt.Errorf("invalid node_id %q: use a UUID or sequence reference like TACK-65: %w", input, domain.ErrInvalidArgument)
	case node.ReferenceDirectProperty:
		return fmt.Errorf("invalid node_id %q: use a UUID or declared reference like TACK: %w", input, domain.ErrInvalidArgument)
	case node.ReferenceScopedProperty:
		return fmt.Errorf("invalid node_id %q: use a UUID or scoped reference like CLYDE::In Progress: %w", input, domain.ErrInvalidArgument)
	default:
		return fmt.Errorf("invalid node_id %q: use a UUID: %w", input, domain.ErrInvalidArgument)
	}
}

func rawUUID(props map[string]json.RawMessage, key string) uuid.UUID {
	raw, ok := props[key]
	if !ok || len(raw) == 0 {
		return uuid.Nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return uuid.Nil
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil
	}
	return id
}

func referenceLookupValues(input string) []json.RawMessage {
	values := []string{input, strings.ToUpper(input), strings.ToLower(input)}
	out := make([]json.RawMessage, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		raw, _ := json.Marshal(value)
		key := string(raw)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, raw)
	}
	return out
}
