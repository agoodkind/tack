package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// Seed-only type keys. These are the ONLY place where specific NodeType names
// live as source-code constants. Runtime code reads NodeType.TypeKey from the
// FDB-loaded NodeType records.
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
func (s *Seeder) SeedOrg(ctx context.Context, orgID uuid.UUID) error {
	log := telemetry.L(ctx)

	nodeTypes, propertyDefs, err := defaultOrgDefinitions(orgID)
	if err != nil {
		log.ErrorContext(ctx, "seed.property_def_undeclared", slog.String("err", err.Error()))
		return fmt.Errorf("seed org %s: %w", orgID, err)
	}

	for _, def := range propertyDefs {
		if err := s.propertyDefs.Set(ctx, def); err != nil {
			log.ErrorContext(
				ctx,
				"seed.property_def_failed",
				slog.String("name", def.Name),
				slog.String("err", err.Error()),
			)

			return fmt.Errorf("seed property def %q: %w", def.Name, err)
		}
	}

	for _, nodeType := range nodeTypes {
		if err := s.nodeTypes.Set(ctx, nodeType); err != nil {
			log.ErrorContext(
				ctx,
				"seed.node_type_failed",
				slog.String("type_key", nodeType.TypeKey),
				slog.String("err", err.Error()),
			)

			return fmt.Errorf("seed node type %q: %w", nodeType.TypeKey, err)
		}
	}

	return nil
}

// defaultOrgDefinitions returns the sample records Seeder writes for an org.
// It is unexported because the sample data changes freely, and no other
// package may pin its contents (TACK-512).
func defaultOrgDefinitions(orgID uuid.UUID) ([]*node.NodeType, []*node.PropertyDef, error) {
	propertyDefs, err := defaultPropertyDefs(orgID)
	if err != nil {
		return nil, nil, err
	}

	return defaultNodeTypes(orgID), propertyDefs, nil
}

func defaultPropertyDefs(orgID uuid.UUID) ([]*node.PropertyDef, error) {
	jsonRaw := func(v any) json.RawMessage {
		b, _ := json.Marshal(v)
		return b
	}

	definitions := []*node.PropertyDef{
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
			ID:                     node.SystemPropID(orgID, "state_id"),
			OrgID:                  orgID,
			Name:                   "state_id",
			Type:                   node.PropertyTypeUUID,
			AppliesToFeatures:      []string{node.FeatureHasWorkflowStates},
			Indexed:                true,
			Required:               true,
			ReferenceTargetTypeKey: nodeTypeState,
			DefaultReference: &node.PropertyDefaultReference{
				Scope:     node.DefaultReferenceScopeCreateScope,
				Reference: "Todo",
			},
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
	}
	definitions = append(definitions, additionalPropertyDefs(orgID)...)
	if err := applyDefaultSearchProjections(definitions); err != nil {
		return nil, err
	}

	return definitions, nil
}

func additionalPropertyDefs(orgID uuid.UUID) []*node.PropertyDef {
	return []*node.PropertyDef{
		{
			ID:                node.SystemPropID(orgID, "description"),
			OrgID:             orgID,
			Name:              "description",
			Type:              node.PropertyTypeText,
			AppliesToFeatures: nil,
			Indexed:           false,
		},
		{
			ID:                node.SystemPropID(orgID, defaultSlugProperty),
			OrgID:             orgID,
			Name:              defaultSlugProperty,
			Type:              node.PropertyTypeText,
			AppliesToFeatures: []string{node.FeatureHasAddress},
			Indexed:           true,
		},
		{
			ID:                node.SystemPropID(orgID, defaultIdentifierProperty),
			OrgID:             orgID,
			Name:              defaultIdentifierProperty,
			Type:              node.PropertyTypeText,
			AppliesToFeatures: []string{node.FeatureIsScope},
			Indexed:           true,
		},
		{
			ID:                node.SystemPropID(orgID, defaultSequenceProperty),
			OrgID:             orgID,
			Name:              defaultSequenceProperty,
			Type:              node.PropertyTypeNumber,
			AppliesToFeatures: []string{node.FeatureHasSequenceID},
			Indexed:           true,
		},
		{
			ID:                node.SystemPropID(orgID, "parent_id"),
			OrgID:             orgID,
			Name:              "parent_id",
			Type:              node.PropertyTypeText,
			AppliesToFeatures: nil,
			Indexed:           true,
		},
		{
			ID:                node.SystemPropID(orgID, "scope_id"),
			OrgID:             orgID,
			Name:              "scope_id",
			Type:              node.PropertyTypeText,
			AppliesToFeatures: nil,
			Indexed:           true,
		},
		{
			ID:                node.SystemPropID(orgID, defaultGroupProperty),
			OrgID:             orgID,
			Name:              defaultGroupProperty,
			Type:              node.PropertyTypeText,
			AppliesToFeatures: nil,
			Indexed:           false,
		},
		{
			ID:                node.SystemPropID(orgID, defaultColorProperty),
			OrgID:             orgID,
			Name:              defaultColorProperty,
			Type:              node.PropertyTypeText,
			AppliesToFeatures: nil,
			Indexed:           false,
		},
		{
			ID:                node.SystemPropID(orgID, defaultSortOrderProperty),
			OrgID:             orgID,
			Name:              defaultSortOrderProperty,
			Type:              node.PropertyTypeNumber,
			AppliesToFeatures: nil,
			Indexed:           false,
		},
	}
}

// seedSearchDecisionError is the refusal of a seeded definition without a
// search decision. SeedOrg logs it once.
type seedSearchDecisionError struct {
	name string
}

func (e seedSearchDecisionError) Error() string {
	return fmt.Sprintf(
		"seeded property definition %q has no search decision: %v",
		e.name,
		node.ErrInvalidSearchProjection,
	)
}

// Unwrap lets callers match node.ErrInvalidSearchProjection.
func (e seedSearchDecisionError) Unwrap() error { return node.ErrInvalidSearchProjection }

// applyDefaultSearchProjections gives every seeded definition its explicit
// search decision. A seeded name without an order has no decision, and the
// seed refuses it instead of storing an undeclared definition.
func applyDefaultSearchProjections(definitions []*node.PropertyDef) error {
	orders := map[string]int{
		"priority":                0,
		"due_date":                1,
		"start_date":              2,
		"state_id":                3,
		"is_draft":                4,
		"description":             10,
		defaultSlugProperty:       20,
		defaultIdentifierProperty: 30,
		defaultSequenceProperty:   40,
		"parent_id":               50,
		"scope_id":                60,
		defaultGroupProperty:      70,
		defaultColorProperty:      80,
		defaultSortOrderProperty:  90,
	}
	included := map[string]bool{
		"description":             true,
		defaultSlugProperty:       true,
		defaultIdentifierProperty: true,
		defaultSequenceProperty:   true,
		defaultGroupProperty:      true,
		"parent_id":               false,
		"scope_id":                false,
		"state_id":                false,
	}

	for _, definition := range definitions {
		order, ok := orders[definition.Name]
		if !ok {
			return seedSearchDecisionError{name: definition.Name}
		}

		definition.Search = &node.SearchProjection{
			Include: included[definition.Name],
			Order:   order,
			Rule: node.TextRule{
				Mode:   node.TextRuleScalar,
				Fields: nil,
				Items:  nil,
				Labels: nil,
			},
		}
	}

	return nil
}

// scopedSequenceTemplate is the reference template the seed writes for
// work-item types: the enclosing scope's reference, a hyphen, then a generated
// sequence. The node type is absent, so every type carrying this template in one
// scope shares a single numbering space.
//
// This is seed data, not an engine default: the engine supplies no template of
// its own, and a type with no template carries no constraint. SeedOrg reapplies
// every built-in NodeType field on reseed, this template included, so an org's
// edit to a built-in type survives only until the next reseed. A durable custom
// reference shape belongs on an org-defined type, which the seed never touches.
func scopedSequenceTemplate() []node.ReferenceTemplate {
	return []node.ReferenceTemplate{
		{
			Name:      "reference",
			IsPrimary: true,
			Generated: defaultSequenceProperty,
			Parts: []node.ReferencePart{
				{Kind: node.ReferencePartScopeRef, Value: node.FeatureIsScope},
				{Kind: node.ReferencePartLiteral, Value: "-"},
				{Kind: node.ReferencePartProperty, Value: defaultSequenceProperty},
			},
		},
	}
}

// defaultNodeTypes returns the built-in NodeType records for a given org.
// IDs are deterministic UUID v5 values derived from (orgID, NodeType.Slug) so
// repeated seeding writes the same record.
func defaultNodeTypes(orgID uuid.UUID) []*node.NodeType {
	specs := defaultContainerSpecs()
	specs = append(specs, defaultWorkItemSpecs()...)
	specs = append(specs, defaultSupportingSpecs()...)

	types := make([]*node.NodeType, 0, len(specs))
	for _, sp := range specs {
		id := uuid.NewSHA1(builtinTypeNamespace, []byte(orgID.String()+":"+sp.slug))
		var referenceTemplates []node.ReferenceTemplate
		if sp.features.Has(node.FeatureHasSequenceID) {
			referenceTemplates = scopedSequenceTemplate()
		}

		types = append(
			types,
			&node.NodeType{
				ID:                 id,
				OrgID:              orgID,
				Name:               sp.name,
				Slug:               sp.slug,
				PluralSlug:         sp.pluralSlug,
				IsBuiltin:          true,
				TypeKey:            sp.typeKey,
				AllowedOps:         node.AllOps,
				Features:           sp.features,
				CanContain:         sp.canContain,
				CanLiveUnder:       sp.canLiveUnder,
				Reference:          sp.reference,
				ReferenceTemplates: referenceTemplates,
				DefaultChildren:    sp.defaultChildren,
			},
		)
	}

	return types
}
