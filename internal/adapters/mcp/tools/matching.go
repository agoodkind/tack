package tools

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func matchesReferenceProperty(view *node.NodeView, propName, input string) bool {
	if view == nil {
		return false
	}

	if propName == nameFieldKey && strings.EqualFold(view.Name, input) {
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
		for nodeID := range matches {
			return nodeID, nil
		}
	}

	candidates := make([]string, 0, len(matches))
	for nodeID := range matches {
		candidates = append(candidates, nodeID.String())
	}

	sort.Strings(candidates)
	err := fmt.Errorf(
		"%s %q matched multiple nodes (%s): %w",
		kind,
		input,
		strings.Join(candidates, ", "),
		domain.ErrInvalidArgument,
	)
	slog.Error(
		"reference.match_ambiguous",
		slog.String("kind", kind),
		slog.String("input", input),
		slog.String("candidates", strings.Join(candidates, ", ")),
		slog.Any("err", err),
	)

	return uuid.Nil, err
}

func invalidTypedNodeIDError(nodeType *node.NodeType, input string) error {
	switch nodeType.Reference.Strategy {
	case node.ReferenceUUIDOnly:
		return invalidReferenceError{
			err: fmt.Errorf("invalid node_id %q: use a UUID: %w", input, domain.ErrInvalidArgument),
		}
	case node.ReferenceScopedSequence:
		return invalidReferenceError{
			err: fmt.Errorf(
				"invalid node_id %q: use a UUID or sequence reference like TACK-65: %w",
				input,
				domain.ErrInvalidArgument,
			),
		}
	case node.ReferenceDirectProperty:
		return invalidReferenceError{
			err: fmt.Errorf(
				"invalid node_id %q: use a UUID or declared reference like TACK: %w",
				input,
				domain.ErrInvalidArgument,
			),
		}
	case node.ReferenceScopedProperty:
		return invalidReferenceError{
			err: fmt.Errorf(
				"invalid node_id %q: use a UUID or scoped reference like CLYDE::In Progress: %w",
				input,
				domain.ErrInvalidArgument,
			),
		}
	default:
		return invalidReferenceError{
			err: fmt.Errorf("invalid node_id %q: use a UUID: %w", input, domain.ErrInvalidArgument),
		}
	}
}

func rawUUID(props map[string]json.RawMessage, key string) uuid.UUID {
	raw, provided := props[key]
	if !provided || len(raw) == 0 {
		return uuid.Nil
	}

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return uuid.Nil
	}

	nodeID, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil
	}

	return nodeID
}

func referenceLookupValues(input string) []json.RawMessage {
	values := []string{input, strings.ToUpper(input), strings.ToLower(input)}
	out := make([]json.RawMessage, 0, len(values))
	seen := map[string]struct{}{}

	for _, value := range values {
		raw := mustRawString(value)
		key := string(raw)
		if _, provided := seen[key]; provided {
			continue
		}

		seen[key] = struct{}{}
		out = append(out, raw)
	}

	return out
}
