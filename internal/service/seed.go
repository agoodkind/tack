package service

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// Seed-only type keys. These are the ONLY place where specific NodeType names
// live as source-code constants. Runtime code reads NodeType.TypeKey (and
// Slug) from the FDB-loaded NodeType records.
const (
	nodeTypeIssue     = "issue"
	nodeTypeEpic      = "epic"
	nodeTypeCycle     = "cycle"
	nodeTypeModule    = "module"
	nodeTypeOrg       = "org"
	nodeTypeWorkspace = "workspace"
	nodeTypeProject   = "project"
	nodeTypeState     = "state"
	nodeTypeLabel     = "label"
	nodeTypeComment   = "comment"
	nodeTypeActivity  = "activity"
)

// builtinTypeNamespace is the UUID v5 namespace for deterministic builtin NodeType IDs.
var builtinTypeNamespace = uuid.MustParse("bab1e5da-cafe-dead-beef-000000000001")

// Seeder seeds default PropertyDef and built-in NodeType records into an org.
// Seeding is idempotent: every builtin record has a deterministic UUID derived
// from the org ID, so repeated calls rewrite the same records.
type Seeder struct {
	propertyDefs node.PropertyDefRepository
	nodeTypes    node.TypeRepository
}

func NewSeeder(propertyDefs node.PropertyDefRepository, nodeTypes node.TypeRepository) *Seeder {
	return &Seeder{propertyDefs: propertyDefs, nodeTypes: nodeTypes}
}

// SeedOrg writes the default NodeTypes and PropertyDefs for a newly created org.
// Called once on org creation and again after any NodeType / PropertyDef change
// to propagate updated defaults.
func (s *Seeder) SeedOrg(ctx context.Context, orgID uuid.UUID) {
	log := telemetry.L(ctx)

	for _, def := range defaultPropertyDefs(orgID) {
		if err := s.propertyDefs.Set(ctx, def); err != nil {
			log.Warn("seed property def", slog.String("name", def.Name), slog.String("err", err.Error()))
		}
	}
	for _, nt := range defaultNodeTypes(orgID) {
		if err := s.nodeTypes.Set(ctx, nt); err != nil {
			log.Warn("seed node type", slog.String("slug", nt.Slug), slog.String("err", err.Error()))
		}
	}
}

func defaultPropertyDefs(orgID uuid.UUID) []*node.PropertyDef {
	jsonRaw := func(v any) json.RawMessage {
		b, _ := json.Marshal(v)
		return b
	}

	return []*node.PropertyDef{
		{
			ID:                node.SystemPropID(orgID, "priority"),
			OrgID:             orgID,
			Name:              "priority",
			Type:              node.PropertyTypeSelect,
			AppliesToFeatures: []string{node.FeatureHasWorkflowStates},
			Indexed:           true,
			Options: []node.EnumOption{
				{Key: "urgent", Label: "Urgent", Color: "#EF4444", SortRank: 0},
				{Key: "high", Label: "High", Color: "#F97316", SortRank: 1},
				{Key: "medium", Label: "Medium", Color: "#EAB308", SortRank: 2},
				{Key: "low", Label: "Low", Color: "#22C55E", SortRank: 3},
				{Key: "none", Label: "No Priority", Color: "#9B9B9B", SortRank: 4},
			},
			DefaultValue: jsonRaw("none"),
		},
		{
			ID:                node.SystemPropID(orgID, "due_date"),
			OrgID:             orgID,
			Name:              "due_date",
			Type:              node.PropertyTypeTimestamp,
			AppliesToFeatures: []string{node.FeatureHasDueDates},
			Indexed:           true,
		},
		{
			ID:                node.SystemPropID(orgID, "start_date"),
			OrgID:             orgID,
			Name:              "start_date",
			Type:              node.PropertyTypeTimestamp,
			AppliesToFeatures: []string{node.FeatureHasDueDates},
			Indexed:           true,
		},
		{
			ID:                node.SystemPropID(orgID, "state_id"),
			OrgID:             orgID,
			Name:              "state_id",
			Type:              node.PropertyTypeUUID,
			AppliesToFeatures: []string{node.FeatureHasWorkflowStates},
			Indexed:           true,
		},
		{
			ID:                node.SystemPropID(orgID, "is_draft"),
			OrgID:             orgID,
			Name:              "is_draft",
			Type:              node.PropertyTypeCheckbox,
			AppliesToFeatures: []string{node.FeatureHasWorkflowStates},
			Indexed:           false,
			DefaultValue:      jsonRaw(false),
		},
		{
			ID:                node.SystemPropID(orgID, "description"),
			OrgID:             orgID,
			Name:              "description",
			Type:              node.PropertyTypeText,
			AppliesToFeatures: nil, // applies to every type
			Indexed:           false,
		},
		{
			ID:                node.SystemPropID(orgID, "slug"),
			OrgID:             orgID,
			Name:              "slug",
			Type:              node.PropertyTypeText,
			AppliesToFeatures: []string{node.FeatureHasSlug},
			Indexed:           true,
		},
		{
			ID:                node.SystemPropID(orgID, "identifier"),
			OrgID:             orgID,
			Name:              "identifier",
			Type:              node.PropertyTypeText,
			AppliesToFeatures: []string{node.FeatureIsScope},
			Indexed:           true,
		},
		{
			ID:                node.SystemPropID(orgID, "color"),
			OrgID:             orgID,
			Name:              "color",
			Type:              node.PropertyTypeText,
			AppliesToFeatures: nil, // any type can have a color
			Indexed:           false,
		},
		{
			ID:                node.SystemPropID(orgID, "sort_order"),
			OrgID:             orgID,
			Name:              "sort_order",
			Type:              node.PropertyTypeNumber,
			AppliesToFeatures: nil,
			Indexed:           false,
		},
	}
}

// defaultNodeTypes returns the built-in NodeType records for a given org.
// IDs are deterministic UUID v5 values derived from (orgID, slug) so repeated
// seeding writes the same record.
func defaultNodeTypes(orgID uuid.UUID) []*node.NodeType {
	type spec struct {
		slug         string
		pluralSlug   string
		name         string
		typeKey      string
		features     node.Features
		canContain   []string
		canLiveUnder []string
	}
	issueFeatures := node.Features{
		node.FeatureHasSequenceID, node.FeatureHasWorkflowStates,
		node.FeatureHasAssignees, node.FeatureHasActivity,
		node.FeatureHasComments, node.FeatureHasDueDates,
	}
	epicFeatures := append(node.Features{node.FeatureIsContainer}, issueFeatures...)
	cycleFeatures := node.Features{node.FeatureHasDueDates, node.FeatureHasActivity, node.FeatureIsContainer}
	moduleFeatures := node.Features{node.FeatureHasActivity, node.FeatureIsContainer}
	workspaceFeatures := node.Features{
		node.FeatureHasSlug, node.FeatureIsContainer,
		node.FeatureIsEntryPoint, node.FeatureIsScope,
		node.FeatureExcludeFromGenericTools,
	}
	projectFeatures := node.Features{
		node.FeatureHasSlug, node.FeatureIsContainer, node.FeatureIsScope,
	}
	orgFeatures := node.Features{
		node.FeatureHasSlug, node.FeatureIsContainer, node.FeatureIsScope,
		node.FeatureExcludeFromGenericTools,
	}

	specs := []spec{
		{"org", "orgs", "Org", nodeTypeOrg, orgFeatures, []string{nodeTypeWorkspace}, nil},
		{"workspace", "workspaces", "Workspace", nodeTypeWorkspace, workspaceFeatures, []string{nodeTypeProject, nodeTypeLabel, nodeTypeWorkspace}, []string{nodeTypeOrg}},
		{"project", "projects", "Project", nodeTypeProject, projectFeatures, []string{nodeTypeIssue, nodeTypeEpic, nodeTypeCycle, nodeTypeModule, nodeTypeState}, []string{nodeTypeWorkspace}},
		{"issue", "issues", "Issue", nodeTypeIssue, issueFeatures, []string{nodeTypeComment, nodeTypeActivity}, []string{nodeTypeProject}},
		{"epic", "epics", "Epic", nodeTypeEpic, epicFeatures, []string{nodeTypeIssue, nodeTypeComment, nodeTypeActivity}, []string{nodeTypeProject}},
		{"cycle", "cycles", "Cycle", nodeTypeCycle, cycleFeatures, []string{nodeTypeIssue}, []string{nodeTypeProject}},
		{"module", "modules", "Module", nodeTypeModule, moduleFeatures, []string{nodeTypeIssue}, []string{nodeTypeProject}},
		{"state", "states", "State", nodeTypeState, nil, nil, []string{nodeTypeProject}},
		{"label", "labels", "Label", nodeTypeLabel, nil, nil, []string{nodeTypeWorkspace}},
		{"comment", "comments", "Comment", nodeTypeComment, nil, nil, []string{nodeTypeIssue, nodeTypeEpic}},
		{"activity", "activities", "Activity", nodeTypeActivity, nil, nil, []string{nodeTypeIssue, nodeTypeEpic, nodeTypeCycle, nodeTypeModule}},
	}

	types := make([]*node.NodeType, 0, len(specs))
	for _, sp := range specs {
		id := uuid.NewSHA1(builtinTypeNamespace, []byte(orgID.String()+":"+sp.slug))
		types = append(types, &node.NodeType{
			ID:           id,
			OrgID:        orgID,
			Name:         sp.name,
			Slug:         sp.slug,
			PluralSlug:   sp.pluralSlug,
			IsBuiltin:    true,
			TypeKey:      sp.typeKey,
			AllowedOps:   node.AllOps,
			Features:     sp.features,
			CanContain:   sp.canContain,
			CanLiveUnder: sp.canLiveUnder,
		})
	}
	return types
}
