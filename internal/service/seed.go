package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// Seed-only type keys. These are the ONLY place where specific NodeType
// names
// live as source-code constants. Runtime code reads NodeType.TypeKey from
// the
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

// builtinTypeNamespace is the UUID v5 namespace for deterministic builtin
// NodeType IDs.
var builtinTypeNamespace = uuid.MustParse("bab1e5da-cafe-dead-beef-000000000001")

// Seeder seeds default PropertyDef and built-in NodeType records into an
// org.
// Seeding is idempotent: every builtin record has a deterministic UUID
// derived
// from the org ID, so repeated calls rewrite the same records.
type Seeder struct {
	propertyDefs node.PropertyDefRepository
	nodeTypes    node.TypeRepository
}

func NewSeeder(propertyDefs node.PropertyDefRepository, nodeTypes node.TypeRepository) *Seeder {
	return &Seeder{propertyDefs: propertyDefs, nodeTypes: nodeTypes}
}

// SeedOrg writes the default NodeTypes and PropertyDefs for a newly created
// org.
// Called once on org creation and again after any NodeType / PropertyDef
// change
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

	for _, nt := range nodeTypes {
		if err := s.nodeTypes.Set(ctx, nt); err != nil {
			log.ErrorContext(
				ctx,
				"seed.node_type_failed",
				slog.String("type_key", nt.TypeKey),
				slog.String("err", err.Error()),
			)

			return fmt.Errorf("seed node type %q: %w", nt.TypeKey, err)
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
