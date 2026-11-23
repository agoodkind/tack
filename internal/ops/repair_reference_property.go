package ops

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// RepairCleanupPolicy controls whether the repair removes source fields.
type RepairCleanupPolicy string

// RepairConflictPolicy controls candidate selection when inputs disagree.
type RepairConflictPolicy string

const (
	// RepairCleanupNone leaves source fields unchanged after setting the target property.
	RepairCleanupNone RepairCleanupPolicy = "none"
	// RepairCleanupSourceFields removes declared source fields after setting the target property.
	RepairCleanupSourceFields RepairCleanupPolicy = "source_fields"
	// RepairConflictPreferSource chooses a resolved source-field candidate before existing target values.
	RepairConflictPreferSource RepairConflictPolicy = "prefer_source"
	// RepairConflictPreferExisting chooses the existing target value before source-field candidates.
	RepairConflictPreferExisting RepairConflictPolicy = "prefer_existing"
	// RepairConflictPreferHighestRank chooses the candidate with the largest numeric rank property.
	RepairConflictPreferHighestRank RepairConflictPolicy = "prefer_highest_rank"
	// RepairConflictFail blocks the repair when resolved candidates point at different nodes.
	RepairConflictFail RepairConflictPolicy = "fail"
)

// RepairReferenceProfile specifies a generic UUID reference-property repair.
type RepairReferenceProfile struct {
	Name                string                  `json:"name,omitempty"`
	TargetProperty      string                  `json:"target_property"`
	TargetTypeKey       string                  `json:"target_type_key,omitempty"`
	TargetMatchProperty string                  `json:"target_match_property,omitempty"`
	SourceFields        []string                `json:"source_fields"`
	ScopeFields         []string                `json:"scope_fields,omitempty"`
	RemoveFields        []string                `json:"remove_fields,omitempty"`
	RenameFields        []RepairRenameField     `json:"rename_fields,omitempty"`
	AppendTextFields    []RepairAppendTextField `json:"append_text_fields,omitempty"`
	Normalization       RepairNormalization     `json:"normalization"`
	UseDefaultFallback  bool                    `json:"use_default_fallback,omitempty"`
	CleanupBehavior     RepairCleanupPolicy     `json:"cleanup_behavior,omitempty"`
	ConflictPolicy      RepairConflictPolicy    `json:"conflict_policy,omitempty"`
	RankProperty        string                  `json:"rank_property,omitempty"`
}

// RepairNormalization controls operator-supplied source normalization.
type RepairNormalization struct {
	Trim     bool              `json:"trim,omitempty"`
	CaseFold bool              `json:"case_fold,omitempty"`
	ValueMap map[string]string `json:"value_map,omitempty"`
}

// RepairPreview records the current repair decision for one node.
type RepairPreview struct {
	Class                RepairClass                `json:"class"`
	ProfileName          string                     `json:"profile_name,omitempty"`
	NodeID               uuid.UUID                  `json:"node_id"`
	NodeType             string                     `json:"node_type"`
	CurrentUpdatedAt     time.Time                  `json:"current_updated_at"`
	TargetProperty       string                     `json:"target_property"`
	TargetType           string                     `json:"target_type"`
	ObservedSources      []RepairObservedSource     `json:"observed_sources,omitempty"`
	Candidates           []RepairReferenceCandidate `json:"candidates,omitempty"`
	ChosenCandidate      *RepairReferenceCandidate  `json:"chosen_candidate,omitempty"`
	PlannedProps         map[string]json.RawMessage `json:"planned_props,omitempty"`
	PlannedRelationships []RepairRelationshipChange `json:"planned_relationships,omitempty"`
	Summary              string                     `json:"summary"`
	ConfirmationToken    string                     `json:"confirmation_token,omitempty"`
	NeedsRepair          bool                       `json:"needs_repair"`
	CanApply             bool                       `json:"can_apply"`
}

// RepairObservedSource records one source field read from the node under repair.
type RepairObservedSource struct {
	Field      string          `json:"field"`
	Raw        json.RawMessage `json:"raw,omitempty"`
	Normalized string          `json:"normalized,omitempty"`
	Present    bool            `json:"present"`
}

// RepairReferenceCandidate records one attempted target-node match.
type RepairReferenceCandidate struct {
	SourceField string    `json:"source_field"`
	Reference   string    `json:"reference"`
	NodeID      uuid.UUID `json:"node_id,omitempty"`
	NodeName    string    `json:"node_name,omitempty"`
	Rank        int64     `json:"rank,omitempty"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
}
