package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strconv"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func normalizeCreateInput(in CreateInput) CreateInput {
	if in.ScopeID == uuid.Nil {
		in.ScopeID = in.ParentID
	}
	if in.ParentID == uuid.Nil {
		in.ParentID = in.ScopeID
	}
	return in
}

func createTimeFromUUID(id uuid.UUID) time.Time {
	seconds, nanoseconds := id.Time().UnixTime()
	return time.Unix(seconds, nanoseconds).UTC()
}

func (s *NodeService) prepareCreateProps(
	ctx context.Context,
	log *slog.Logger,
	orgID uuid.UUID,
	nt *node.NodeType,
	in CreateInput,
) (map[string]json.RawMessage, error) {
	props, err := s.copyCreateProps(ctx, log, orgID, nt, in)
	if err != nil {
		return nil, err
	}
	if err := s.applyCreatePropertyDefaults(ctx, orgID, nt, in.ScopeID, props); err != nil {
		return nil, err
	}
	if err := s.validateCreateProps(ctx, orgID, nt, props); err != nil {
		return nil, err
	}
	return props, nil
}

func (s *NodeService) copyCreateProps(
	ctx context.Context,
	log *slog.Logger,
	orgID uuid.UUID,
	nt *node.NodeType,
	in CreateInput,
) (map[string]json.RawMessage, error) {
	props := make(map[string]json.RawMessage, len(in.Props)+2)
	maps.Copy(props, in.Props)
	stampUUIDCreateProp(props, "parent_id", in.ParentID)
	stampUUIDCreateProp(props, "scope_id", in.ScopeID)
	if err := s.allocateCreateSequence(ctx, log, orgID, nt, in.ScopeID, props); err != nil {
		return nil, err
	}
	return props, nil
}

func stampUUIDCreateProp(props map[string]json.RawMessage, name string, id uuid.UUID) {
	if id == uuid.Nil {
		return
	}
	props[name] = json.RawMessage(strconv.Quote(id.String()))
}

func (s *NodeService) allocateCreateSequence(
	ctx context.Context,
	log *slog.Logger,
	orgID uuid.UUID,
	nt *node.NodeType,
	scopeID uuid.UUID,
	props map[string]json.RawMessage,
) error {
	if !nt.Features.Has(node.FeatureHasSequenceID) || scopeID == uuid.Nil {
		return nil
	}
	sequenceID, err := s.nodes.AllocateSequence(ctx, orgID, scopeID, nt.TypeKey)
	if err != nil {
		log.ErrorContext(ctx, "node.Create: allocate sequence",
			slog.String("node_type", nt.TypeKey),
			slog.String("scope_id", scopeID.String()),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("allocate sequence: %w", err)
	}
	props["sequence"] = json.RawMessage(strconv.FormatInt(sequenceID, 10))
	return nil
}

func newCreateNode(
	orgID uuid.UUID,
	nodeID uuid.UUID,
	nodeType string,
	in CreateInput,
	props map[string]json.RawMessage,
	now time.Time,
) *node.Node {
	return &node.Node{
		ID:        nodeID,
		OrgID:     orgID,
		NodeType:  nodeType,
		Name:      in.Name,
		Props:     props,
		CreatedBy: in.ActorID,
		UpdatedBy: in.ActorID,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func buildCreateRelationships(
	orgID uuid.UUID,
	nodeID uuid.UUID,
	in CreateInput,
	now time.Time,
) []*node.Relationship {
	relationships := make([]*node.Relationship, 0, len(in.Relationships)+1)
	if in.ParentID != uuid.Nil {
		relationships = append(relationships, &node.Relationship{
			OrgID:        orgID,
			SourceID:     nodeID,
			RelationType: node.RelChildOf,
			TargetID:     in.ParentID,
			CreatedBy:    in.ActorID,
			CreatedAt:    now,
			Props:        nil,
		})
	}
	for _, relationship := range in.Relationships {
		if relationship.OrgID == uuid.Nil {
			relationship.OrgID = orgID
		}
		if relationship.SourceID == uuid.Nil {
			relationship.SourceID = nodeID
		}
		if relationship.CreatedAt.IsZero() {
			relationship.CreatedAt = now
		}
		if relationship.CreatedBy == uuid.Nil {
			relationship.CreatedBy = in.ActorID
		}
		relationships = append(relationships, relationship)
	}
	return relationships
}
