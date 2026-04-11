package service

import (
	"context"
	"log/slog"

	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
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

// SeedWorkspace writes default PropertyDefs and built-in NodeTypes for the given workspace.
// Errors are non-fatal — the workspace is usable even if seeding partially fails.
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
			NodeTypes:   []string{node.NodeTypeIssue, node.NodeTypeEpic},
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
			NodeTypes:   []string{node.NodeTypeIssue, node.NodeTypeEpic, node.NodeTypeCycle, node.NodeTypeModule},
			Indexed:     true,
			IsSystem:    true,
		},
		{
			ID:          systemPropID(workspaceID, propNameStartDate),
			OrgID:       orgID,
			WorkspaceID: ws,
			Name:        "Start Date",
			Type:        node.PropertyTypeTimestamp,
			NodeTypes:   []string{node.NodeTypeIssue, node.NodeTypeEpic, node.NodeTypeCycle, node.NodeTypeModule},
			Indexed:     true,
			IsSystem:    true,
		},
		{
			ID:           systemPropID(workspaceID, propNameIsDraft),
			OrgID:        orgID,
			WorkspaceID:  ws,
			Name:         "Is Draft",
			Type:         node.PropertyTypeCheckbox,
			NodeTypes:    []string{node.NodeTypeIssue},
			Indexed:      false,
			IsSystem:     true,
			DefaultValue: &node.PropertyValue{Kind: node.PropertyValueBool, Bool: boolPtr(false)},
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

// defaultNodeTypes returns the 4 built-in NodeType records for a given org.
// IDs are deterministic: UUID v5 derived from (builtinTypeNamespace, orgID+":"+slug).
// This ensures the same org always gets the same builtin type IDs across workspace seeds,
// and Set is idempotent.
func defaultNodeTypes(orgID uuid.UUID) []*node.NodeType {
	type spec struct {
		slug       string
		pluralSlug string
		name       string
		typeKey    string
	}
	specs := []spec{
		{"issue", "issues", "Issue", "issue"},
		{"epic", "epics", "Epic", "epic"},
		{"cycle", "cycles", "Cycle", "cycle"},
		{"module", "modules", "Module", "module"},
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
		})
	}
	return types
}

