package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func (s *NodeService) indexCreateSearch(
	ctx context.Context,
	log *slog.Logger,
	nodeID uuid.UUID,
	view *node.NodeView,
) {
	if err := s.searcher.Index(ctx, "nodes", nodeID.String(), searchDocFromView(view)); err != nil {
		log.WarnContext(ctx, "node.Create: search index", slog.String("err", err.Error()))
	}
}

func (s *NodeService) createDefaultChildren(
	ctx context.Context,
	log *slog.Logger,
	nt *node.NodeType,
	parentID uuid.UUID,
	actorID uuid.UUID,
) {
	for _, defaultChild := range nt.DefaultChildren {
		childProps := make(map[string]json.RawMessage, len(defaultChild.Props))
		maps.Copy(childProps, defaultChild.Props)
		if _, err := s.Create(ctx, CreateInput{
			ParentID:               parentID,
			ScopeID:                parentID,
			NodeTypeKey:            defaultChild.TypeKey,
			Name:                   defaultChild.Name,
			Props:                  childProps,
			Relationships:          nil,
			ActorID:                actorID,
			IdempotencyKey:         "",
			IdempotencyFingerprint: "",
			IdempotencySource:      "",
		}); err != nil {
			log.WarnContext(ctx, "node.Create: default child",
				slog.String("parent_type", nt.TypeKey),
				slog.String("child_type", defaultChild.TypeKey),
				slog.String("child_name", defaultChild.Name),
				slog.String("err", err.Error()),
			)
		}
	}
}
