package ops

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

// pass validates every node record on the first pass. On later passes, it
// first removes earlier violations for records in changed buckets or
// organizations, then validates only those records.
func (s *strictScan) pass(ctx context.Context, change node.StrictEvidenceChange, full bool) error {
	if !full {
		for nodeID, violation := range s.violations {
			if strictRecordAffected(change, violation.OrgID, nodeID) {
				delete(s.violations, nodeID)
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
			if !full && !strictRecordAffected(change, record.OrgID, record.ID) {
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

// validate checks one record against its organization's schema and records a
// violation if the schema rejects it.
func (s *strictScan) validate(ctx context.Context, record *node.Node) error {
	schema, cached := s.cache[record.OrgID]
	if !cached {
		var err error
		schema, err = s.schemas.Schema(ctx, record.OrgID)
		if err != nil {
			return strictFailure(ctx, "read the schema of organization "+record.OrgID.String(), err)
		}
		s.cache[record.OrgID] = schema
	}
	s.scanned++
	resultErr := schema.Interpret(record.NodeType, record.Props).Err()
	if resultErr == nil {
		delete(s.violations, record.ID)
		return nil
	}
	s.violations[record.ID] = StrictViolation{OrgID: record.OrgID, NodeID: record.ID, NodeType: record.NodeType, Reason: resultErr.Error()}
	return nil
}

func strictRecordAffected(change node.StrictEvidenceChange, orgID, nodeID uuid.UUID) bool {
	return slices.Contains(change.Organizations, orgID) || slices.Contains(change.Buckets, fdbadapter.WriteFenceBucket(orgID, nodeID))
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
