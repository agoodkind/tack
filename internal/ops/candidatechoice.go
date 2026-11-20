package ops

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

func chooseReferenceCandidate(
	candidates []RepairReferenceCandidate,
	policy RepairConflictPolicy,
) (*RepairReferenceCandidate, string) {
	resolved := resolvedReferenceCandidates(candidates)
	if len(resolved) == 0 {
		return nil, "no reference candidate resolved"
	}

	if policy == RepairConflictFail && distinctReferenceCandidateCount(resolved) > 1 {
		return nil, "reference candidates conflict"
	}

	sortReferenceCandidates(resolved, policy)

	return &resolved[0], ""
}

func plannedReferenceProps(
	view *node.NodeView,
	profile RepairReferenceProfile,
	chosenID uuid.UUID,
) map[string]json.RawMessage {
	props := make(map[string]json.RawMessage)
	if rawUUIDProp(view.Props, profile.TargetProperty) != chosenID {
		props[profile.TargetProperty] = service.MustRawString(chosenID.String())
	}

	if profile.CleanupBehavior == RepairCleanupSourceFields {
		for _, field := range profile.SourceFields {
			if _, provided := view.Props[field]; provided {
				props[field] = json.RawMessage(transformedNullProperty)
			}
		}
	}

	return props
}

func summarizeReferencePreview(preview *RepairPreview) string {
	if preview == nil {
		return "preview is unavailable"
	}

	if preview.ChosenCandidate == nil {
		return "no reference candidate was selected"
	}

	if !preview.NeedsRepair {
		return "reference property already matches selected candidate"
	}

	return fmt.Sprintf(
		"set %s=%s from %s",
		preview.TargetProperty,
		preview.ChosenCandidate.NodeID,
		preview.ChosenCandidate.SourceField,
	)
}

func resolvedReferenceCandidates(candidates []RepairReferenceCandidate) []RepairReferenceCandidate {
	resolved := make([]RepairReferenceCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Status == "resolved" && candidate.NodeID != uuid.Nil {
			resolved = append(resolved, candidate)
		}
	}

	return resolved
}
