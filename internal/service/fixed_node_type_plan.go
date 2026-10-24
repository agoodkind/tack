package service

import (
	"fmt"
	"slices"

	"goodkind.io/tack/internal/domain/node"
)

// earlierSeededCanLiveUnder returns, by built-in slug, the CanLiveUnder list
// that an earlier seed stored for a built-in node type that the correction
// guards. The correction writes the seeded list to a guarded type only when
// the stored list equals this earlier list.
func earlierSeededCanLiveUnder() map[string][]string {
	return map[string][]string{nodeTypeEpic: {nodeTypeProject}}
}

// nodeTypeCorrection is the planned correction of one stored node type.
// Corrected is the replacement record, or nil when the stored record needs no
// write. Conflict is the reason that the stored record blocks the correction,
// and Proposed is the record the correction would otherwise store.
type nodeTypeCorrection struct {
	Corrected *node.NodeType
	Proposed  *node.NodeType
	Conflict  string
}

// planNodeTypeCorrection compares stored with its seeded record. For a type
// that earlierSeededCanLiveUnder guards, it plans the seeded CanLiveUnder list
// only when the stored list equals the earlier seeded list, and it reports a
// conflict for any other stored list that differs from the seeded list. For
// every other type, correctedNodeType decides the correction.
func planNodeTypeCorrection(stored, seed *node.NodeType) nodeTypeCorrection {
	plan := nodeTypeCorrection{Corrected: nil, Proposed: nil, Conflict: ""}
	if seed == nil {
		return plan
	}
	earlier, guarded := earlierSeededCanLiveUnder()[seed.Slug]
	if !guarded {
		plan.Corrected = correctedNodeType(stored, seed)
		return plan
	}
	if sameFeatures(stored.CanLiveUnder, seed.CanLiveUnder) {
		return plan
	}
	proposed := withSeededCanLiveUnder(stored, seed)
	if slices.Equal(stored.CanLiveUnder, earlier) {
		plan.Corrected = proposed
		return plan
	}
	plan.Proposed = proposed
	plan.Conflict = fmt.Sprintf("the correction did not write this node type: the stored can_live_under %q equals neither the earlier seeded value %q nor the seeded value %q",
		stored.CanLiveUnder, earlier, seed.CanLiveUnder)
	return plan
}

// correctedNodeType returns a copy of stored with the seeded CanLiveUnder
// list. It returns nil if the stored list is empty, the stored list includes
// a type outside the seeded list, or both lists contain the same types.
func correctedNodeType(stored, seed *node.NodeType) *node.NodeType {
	if len(stored.CanLiveUnder) == 0 {
		return nil
	}
	for _, parentType := range stored.CanLiveUnder {
		if !slices.Contains(seed.CanLiveUnder, parentType) {
			return nil
		}
	}
	if sameFeatures(stored.CanLiveUnder, seed.CanLiveUnder) {
		return nil
	}
	return withSeededCanLiveUnder(stored, seed)
}

// withSeededCanLiveUnder returns a copy of stored that differs only in the
// CanLiveUnder list, which it copies from seed.
func withSeededCanLiveUnder(stored, seed *node.NodeType) *node.NodeType {
	next := *stored
	next.CanLiveUnder = slices.Clone(seed.CanLiveUnder)
	return &next
}
