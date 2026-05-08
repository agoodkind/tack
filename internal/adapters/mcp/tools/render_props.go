package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func numberProp(view *node.NodeView, key string) int64 {
	raw, ok := view.Props[key]
	if !ok {
		return 0
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return int64(f)
	}
	return 0
}

func uuidProp(view *node.NodeView, key string) uuid.UUID {
	raw, ok := view.Props[key]
	if !ok {
		return uuid.Nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return uuid.Nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil
	}
	return id
}

func uuidArrayProp(view *node.NodeView, key string) []uuid.UUID {
	raw, ok := view.Props[key]
	if !ok {
		return nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	out := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		id, err := uuid.Parse(value)
		if err == nil {
			out = append(out, id)
		}
	}
	return out
}

func stringProp(view *node.NodeView, key string) string {
	raw, ok := view.Props[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

func propAsString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", n), "0"), ".")
	}
	var bv bool
	if err := json.Unmarshal(raw, &bv); err == nil {
		return fmt.Sprintf("%t", bv)
	}
	return string(raw)
}

func sortedScalarPropKeys(view *node.NodeView) []string {
	skip := map[string]struct{}{
		"parent_id":    {},
		"scope_id":     {},
		"state_id":     {},
		"assignee_ids": {},
		"identifier":   {},
		"sequence":     {},
	}
	keys := make([]string, 0, len(view.Props))
	for key := range view.Props {
		if _, hide := skip[key]; hide {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func referenceSummary(reference node.ReferenceConfig) string {
	if reference.Strategy == "" {
		return ""
	}
	strategy := string(reference.Strategy)
	if reference.Strategy == node.ReferenceDirectProperty {
		strategy = "direct_property"
	}
	if reference.Property == "" {
		return strategy
	}
	return fmt.Sprintf("%s:%s", strategy, reference.Property)
}
