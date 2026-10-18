package ops

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// pass validates every node record on the first pass. On later passes, it
// removes validated entries, violations, and dependencies for records with a
// changed organization, a changed write fence bucket, or a dependency in a
// changed bucket. It then validates affected records that the scan returns.
func (s *strictScan) pass(ctx context.Context, change node.StrictEvidenceChange, full bool) error {
	affected := map[uuid.UUID]bool{}
	if !full {
		for nodeID, orgID := range s.validated {
			if s.affected(change, orgID, nodeID) {
				affected[nodeID] = true
				delete(s.validated, nodeID)
				delete(s.violations, nodeID)
				delete(s.dependencies, nodeID)
			}
		}
	}
	cursor := ""
	for {
		records, next, err := s.store.StrictScanPage(ctx, cursor, strictScanPageSize)
		if err != nil {
			return strictFailure(ctx, "read a strict validation scan page", err)
		}
		for _, record := range records {
			if !full && !affected[record.Node.ID] && !strictRecordAffected(change, record.Node.OrgID, record.Node.ID) {
				continue
			}
			if err := s.validate(ctx, record); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		cursor = next
	}
}

func (s *strictScan) validate(ctx context.Context, scanned node.StrictRecord) error {
	record := scanned.Node
	schema, cached := s.cache[record.OrgID]
	if !cached {
		var err error
		schema, err = s.schemas.Schema(ctx, record.OrgID)
		if err != nil {
			return strictFailure(ctx, "read the schema of organization "+record.OrgID.String(), err)
		}
		s.cache[record.OrgID] = schema
	}
	s.validated[record.ID] = record.OrgID
	s.dependencies[record.ID] = slices.Collect(maps.Keys(scanned.Graph.Targets))
	resultErr := schema.ValidateResult(record.NodeType, record.Props, scanned.Graph).Err()
	if resultErr == nil {
		delete(s.violations, record.ID)
		return nil
	}
	s.violations[record.ID] = StrictViolation{OrgID: record.OrgID, NodeID: record.ID, NodeType: record.NodeType, Reason: resultErr.Error()}
	return nil
}

// affected reports whether change includes orgID or the write fence bucket
// of nodeID or any of its recorded dependencies.
func (s *strictScan) affected(change node.StrictEvidenceChange, orgID, nodeID uuid.UUID) bool {
	if strictRecordAffected(change, orgID, nodeID) {
		return true
	}
	for _, dependencyID := range s.dependencies[nodeID] {
		if strictRecordAffected(change, orgID, dependencyID) {
			return true
		}
	}
	return false
}

func strictRecordAffected(change node.StrictEvidenceChange, orgID, nodeID uuid.UUID) bool {
	return slices.Contains(change.Organizations, orgID) || slices.Contains(change.Buckets, node.WriteFenceBucket(orgID, nodeID))
}

func compareStrictViolation(left, right StrictViolation) int {
	if order := bytes.Compare(left.OrgID[:], right.OrgID[:]); order != 0 {
		return order
	}
	return bytes.Compare(left.NodeID[:], right.NodeID[:])
}

// storeSchemaSource reads an organization's node types and property
// definitions through the metadata stores.
type storeSchemaSource struct {
	nodeTypes    node.TypeRepository
	propertyDefs node.PropertyDefRepository
}

func (s storeSchemaSource) Schema(ctx context.Context, orgID uuid.UUID) (node.Schema, error) {
	schema := node.Schema{OrgID: orgID, Types: map[string]*node.NodeType{}, Definitions: nil}
	types, err := s.nodeTypes.List(ctx, orgID)
	if err != nil {
		slog.ErrorContext(ctx, "strict.schema_failed", slog.String("org_id", orgID.String()), slog.String("err", err.Error()))
		return schema, fmt.Errorf("list the node types of organization %s: %w", orgID, err)
	}
	for _, kind := range types {
		schema.Types[kind.TypeKey] = kind
	}
	schema.Definitions, err = s.propertyDefs.List(ctx, orgID)
	if err != nil {
		slog.ErrorContext(ctx, "strict.schema_failed", slog.String("org_id", orgID.String()), slog.String("err", err.Error()))
		return schema, fmt.Errorf("list the property definitions of organization %s: %w", orgID, err)
	}
	return schema, nil
}
