package ops

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func sortReferenceCandidates(candidates []RepairReferenceCandidate, policy RepairConflictPolicy) {
	sort.SliceStable(candidates, func(i int, j int) bool {
		left := candidates[i]
		right := candidates[j]
		if policy == RepairConflictPreferHighestRank && left.Rank != right.Rank {
			return left.Rank > right.Rank
		}
		if policy == RepairConflictPreferExisting {
			return left.SourceField < right.SourceField
		}
		if left.SourceField == "__default__" && right.SourceField != "__default__" {
			return false
		}
		if right.SourceField == "__default__" && left.SourceField != "__default__" {
			return true
		}
		return left.SourceField < right.SourceField
	})
}

func distinctReferenceCandidateCount(candidates []RepairReferenceCandidate) int {
	ids := make(map[uuid.UUID]struct{}, len(candidates))
	for _, candidate := range candidates {
		ids[candidate.NodeID] = struct{}{}
	}
	return len(ids)
}

func referencePropertyValue(view *node.NodeView, propName string) string {
	if propName == "name" {
		return view.Name
	}
	return rawStringValue(view.Props[propName])
}

func rawStringValue(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var stringValue string
	if err := json.Unmarshal(raw, &stringValue); err == nil {
		return stringValue
	}
	var intValue int64
	if err := json.Unmarshal(raw, &intValue); err == nil {
		return strconv.FormatInt(intValue, 10)
	}
	var floatValue float64
	if err := json.Unmarshal(raw, &floatValue); err == nil {
		return strconv.FormatFloat(floatValue, 'f', -1, 64)
	}
	return ""
}

func int64JSONProp(props map[string]json.RawMessage, key string) int64 {
	if strings.TrimSpace(key) == "" {
		return 0
	}
	raw, ok := props[key]
	if !ok || len(raw) == 0 {
		return 0
	}
	var intValue int64
	if err := json.Unmarshal(raw, &intValue); err == nil {
		return intValue
	}
	var floatValue float64
	if err := json.Unmarshal(raw, &floatValue); err == nil {
		return int64(floatValue)
	}
	return 0
}

func nodeTypeKey(nodeType *node.NodeType) string {
	if nodeType == nil {
		return ""
	}
	return nodeType.TypeKey
}

func scopedReferenceStrategy(strategy node.ReferenceStrategy) bool {
	return strategy == node.ReferenceScopedProperty || strategy == node.ReferenceScopedSequence
}

func compactStrings(values []string) []string {
	compacted := make([]string, 0, len(values))
	for _, value := range values {
		trimmedValue := strings.TrimSpace(value)
		if trimmedValue != "" {
			compacted = append(compacted, trimmedValue)
		}
	}
	return compacted
}
