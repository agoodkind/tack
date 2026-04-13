package service

import (
	"context"
	"log/slog"

	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
)

// Seed-only type keys. DO NOT USE OUTSIDE OF SEED.
// Runtime code must read type keys from loaded NodeType data, never from these constants.
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
)

// builtinTypeNamespace is the UUID v5 namespace for deterministic builtin NodeType IDs.
// Builtin type IDs are derived from (orgID, slug) so the same org always gets
// the same IDs across workspace seeds, and Set is idempotent.
var builtinTypeNamespace = uuid.MustParse("bab1e5da-cafe-dead-beef-000000000001")

// WorkspaceSeeder seeds default PropertyDef and built-in NodeType records into a new workspace.
type WorkspaceSeeder struct {
	properties node.PropertyRepository
	nodeTypes  node.TypeRepository
}

func NewWorkspaceSeeder(properties node.PropertyRepository, nodeTypes node.TypeRepository) *WorkspaceSeeder {
	return &WorkspaceSeeder{properties: properties, nodeTypes: nodeTypes}
}

// SeedOrg seeds the org-level NodeType definition. Called once on org creation.
func (s *WorkspaceSeeder) SeedOrg(ctx context.Context, orgID uuid.UUID) {
	orgTypeID := uuid.NewSHA1(builtinTypeNamespace, []byte(orgID.String()+":org"))
	if err := s.nodeTypes.Set(ctx, &node.NodeType{
		ID:         orgTypeID,
		OrgID:      orgID,
		Name:       "Org",
		Slug:       "org",
		PluralSlug: "orgs",
		TypeKey:    nodeTypeOrg,
		IsBuiltin:  true,
		Features:   node.Features{node.FeatureHasSlug, node.FeatureIsContainer, node.FeatureIsScope, node.FeatureExcludeFromGenericTools},
		CanContain: []string{nodeTypeWorkspace},
	}); err != nil {
		telemetry.L(ctx).Warn("seed org type failed", slog.String("error", err.Error()))
	}
}

// SeedWorkspace writes default PropertyDefs and built-in NodeTypes for the given workspace.
// Errors are non-fatal; the workspace is usable even if seeding partially fails.
func (s *WorkspaceSeeder) SeedWorkspace(ctx context.Context, orgID, workspaceID uuid.UUID) {
	for _, def := range defaultPropertyDefs(orgID, workspaceID) {
		if err := s.properties.SetDef(ctx, def); err != nil {
			telemetry.L(ctx).Warn("seed property def failed",
				slog.String("name", def.Name),
				slog.String("workspace_id", workspaceID.String()),
				slog.String("error", err.Error()),
			)
		}
	}
	for _, nt := range defaultNodeTypes(orgID) {
		if err := s.nodeTypes.Set(ctx, nt); err != nil {
			telemetry.L(ctx).Warn("seed node type failed",
				slog.String("slug", nt.Slug),
				slog.String("org_id", orgID.String()),
				slog.String("error", err.Error()),
			)
		}
	}
}

func defaultPropertyDefs(orgID, workspaceID uuid.UUID) []*node.PropertyDef {
	boolPtr := func(b bool) *bool { return &b }
	ws := &workspaceID

	return []*node.PropertyDef{
		{
			ID:          systemPropID(workspaceID, propNamePriority),
			OrgID:       orgID,
			WorkspaceID: ws,
			Name:        "Priority",
			Type:        node.PropertyTypeSelect,
			NodeTypes:   []string{nodeTypeIssue, nodeTypeEpic},
			Indexed:     true,
			IsSystem:    true,
			Options: []node.EnumOption{
				{Key: "urgent", Label: "Urgent", Color: "#EF4444", SortRank: 0},
				{Key: "high", Label: "High", Color: "#F97316", SortRank: 1},
				{Key: "medium", Label: "Medium", Color: "#EAB308", SortRank: 2},
				{Key: "low", Label: "Low", Color: "#22C55E", SortRank: 3},
				{Key: "none", Label: "No Priority", Color: "#9B9B9B", SortRank: 4},
			},
			DefaultValue: enumPV("none", 4),
		},
		{
			ID:          systemPropID(workspaceID, propNameDueDate),
			OrgID:       orgID,
			WorkspaceID: ws,
			Name:        "Due Date",
			Type:        node.PropertyTypeTimestamp,
			NodeTypes:   []string{nodeTypeIssue, nodeTypeEpic, nodeTypeCycle, nodeTypeModule},
			Indexed:     true,
			IsSystem:    true,
		},
		{
			ID:          systemPropID(workspaceID, propNameStartDate),
			OrgID:       orgID,
			WorkspaceID: ws,
			Name:        "Start Date",
			Type:        node.PropertyTypeTimestamp,
			NodeTypes:   []string{nodeTypeIssue, nodeTypeEpic, nodeTypeCycle, nodeTypeModule},
			Indexed:     true,
			IsSystem:    true,
		},
		{
			ID:           systemPropID(workspaceID, propNameIsDraft),
			OrgID:        orgID,
			WorkspaceID:  ws,
			Name:         "Is Draft",
			Type:         node.PropertyTypeCheckbox,
			NodeTypes:    []string{nodeTypeIssue},
			Indexed:      false,
			IsSystem:     true,
			DefaultValue: &node.PropertyValue{Kind: node.PropertyValueBool, Bool: boolPtr(false)},
		},
		// Workspace properties
		{
			ID:          systemPropID(workspaceID, propNameSlug),
			OrgID:       orgID,
			WorkspaceID: ws,
			Name:        "Slug",
			Type:        node.PropertyTypeText,
			NodeTypes:   []string{nodeTypeWorkspace},
			Indexed:     true,
			IsSystem:    true,
		},
		// Project properties
		{
			ID:          systemPropID(workspaceID, propNameIdentifier),
			OrgID:       orgID,
			WorkspaceID: ws,
			Name:        "Identifier",
			Type:        node.PropertyTypeText,
			NodeTypes:   []string{nodeTypeProject},
			Indexed:     true,
			IsSystem:    true,
		},
		{
			ID:          systemPropID(workspaceID, propNameDescription),
			OrgID:       orgID,
			WorkspaceID: ws,
			Name:        "Description",
			Type:        node.PropertyTypeText,
			NodeTypes:   []string{nodeTypeProject},
			Indexed:     false,
			IsSystem:    true,
		},
		{
			ID:          systemPropID(workspaceID, propNameNetwork),
			OrgID:       orgID,
			WorkspaceID: ws,
			Name:        "Network",
			Type:        node.PropertyTypeNumber,
			NodeTypes:   []string{nodeTypeProject},
			Indexed:     false,
			IsSystem:    true,
		},
		{
			ID:          systemPropID(workspaceID, propNameDefaultStateID),
			OrgID:       orgID,
			WorkspaceID: ws,
			Name:        "Default State ID",
			Type:        node.PropertyTypeText,
			NodeTypes:   []string{nodeTypeProject},
			Indexed:     false,
			IsSystem:    true,
		},
		// State properties
		{
			ID:          systemPropID(workspaceID, propNameGroupName),
			OrgID:       orgID,
			WorkspaceID: ws,
			Name:        "Group Name",
			Type:        node.PropertyTypeSelect,
			NodeTypes:   []string{nodeTypeState},
			Indexed:     true,
			IsSystem:    true,
			Options: []node.EnumOption{
				{Key: "backlog", Label: "Backlog", SortRank: 0},
				{Key: "todo", Label: "Todo", SortRank: 1},
				{Key: "started", Label: "Started", SortRank: 2},
				{Key: "completed", Label: "Completed", SortRank: 3},
				{Key: "cancelled", Label: "Cancelled", SortRank: 4},
			},
		},
		{
			ID:          systemPropID(workspaceID, propNameColor),
			OrgID:       orgID,
			WorkspaceID: ws,
			Name:        "Color",
			Type:        node.PropertyTypeText,
			NodeTypes:   []string{nodeTypeState, nodeTypeLabel},
			Indexed:     false,
			IsSystem:    true,
		},
		{
			ID:          systemPropID(workspaceID, propNameSortOrder),
			OrgID:       orgID,
			WorkspaceID: ws,
			Name:        "Sort Order",
			Type:        node.PropertyTypeNumber,
			NodeTypes:   []string{nodeTypeState, nodeTypeLabel},
			Indexed:     false,
			IsSystem:    true,
		},
	}
}

// enumPV constructs a PropertyValue for an enum property with its sort rank pre-populated.
func enumPV(key string, rank int32) *node.PropertyValue {
	return &node.PropertyValue{
		Kind:     node.PropertyValueEnum,
		Enum:     &key,
		EnumRank: &rank,
	}
}

// defaultNodeTypes returns the built-in NodeType records for a given org.
// IDs are deterministic: UUID v5 derived from (builtinTypeNamespace, orgID+":"+slug).
// This ensures the same org always gets the same builtin type IDs across workspace seeds,
// and Set is idempotent.
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

	specs := []spec{
		{"issue", "issues", "Issue", "issue", issueFeatures, nil, []string{nodeTypeProject}},
		{"epic", "epics", "Epic", "epic", epicFeatures, []string{nodeTypeIssue}, []string{nodeTypeProject}},
		{"cycle", "cycles", "Cycle", "cycle", cycleFeatures, []string{nodeTypeIssue}, []string{nodeTypeProject}},
		{"module", "modules", "Module", "module", moduleFeatures, []string{nodeTypeIssue}, []string{nodeTypeProject}},
		{"workspace", "workspaces", "Workspace", "workspace", node.Features{node.FeatureHasSlug, node.FeatureIsContainer, node.FeatureIsEntryPoint, node.FeatureIsScope, node.FeatureExcludeFromGenericTools}, []string{nodeTypeProject, nodeTypeWorkspace}, []string{nodeTypeOrg}},
		{"project", "projects", "Project", "project", node.Features{node.FeatureHasSlug, node.FeatureIsContainer, node.FeatureIsScope}, []string{nodeTypeIssue, nodeTypeEpic, nodeTypeCycle, nodeTypeModule}, []string{nodeTypeWorkspace}},
		{"state", "states", "State", "state", nil, nil, []string{nodeTypeProject}},
		{"label", "labels", "Label", "label", nil, nil, []string{nodeTypeWorkspace}},
	}

	types := make([]*node.NodeType, 0, len(specs))
	for _, s := range specs {
		id := uuid.NewSHA1(builtinTypeNamespace, []byte(orgID.String()+":"+s.slug))
		types = append(types, &node.NodeType{
			ID:         id,
			OrgID:      orgID,
			Name:       s.name,
			Slug:       s.slug,
			PluralSlug: s.pluralSlug,
			IsBuiltin:  true,
			TypeKey:    s.typeKey,
			AllowedOps: node.AllOps,
			Features:   s.features,
			CanContain:   s.canContain,
			CanLiveUnder: s.canLiveUnder,
		})
	}
	return types
}

