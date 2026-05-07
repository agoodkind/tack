package service

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func referenceAddressProperty(nt *node.NodeType) string {
	if nt == nil {
		return ""
	}
	return nt.Reference.DirectAddressProperty()
}

func referenceAddressValue(nt *node.NodeType, props map[string]json.RawMessage) string {
	propertyName := referenceAddressProperty(nt)
	if propertyName == "" {
		return ""
	}
	return firstStringProp(props, propertyName)
}

func (s *NodeService) writeReferenceAddress(ctx context.Context, nt *node.NodeType, props map[string]json.RawMessage, nodeID uuid.UUID, log *slog.Logger, operation string) {
	address := referenceAddressValue(nt, props)
	if address == "" {
		return
	}
	if err := s.nodes.WriteAddress(ctx, nt.TypeKey, node.AddressKindPrimary, address, nodeID); err != nil {
		log.WarnContext(ctx, operation+": write address",
			slog.String("address", address),
			slog.String("property", referenceAddressProperty(nt)),
			slog.String("err", err.Error()),
		)
	}
}

func (s *NodeService) reconcileReferenceAddress(ctx context.Context, nt *node.NodeType, oldProps, newProps map[string]json.RawMessage, nodeID uuid.UUID, log *slog.Logger) {
	oldAddress := referenceAddressValue(nt, oldProps)
	newAddress := referenceAddressValue(nt, newProps)
	if oldAddress == newAddress {
		return
	}
	if oldAddress != "" {
		if err := s.nodes.DeleteAddress(ctx, nt.TypeKey, node.AddressKindPrimary, oldAddress); err != nil {
			log.WarnContext(ctx, "node.Update: delete old address",
				slog.String("address", oldAddress),
				slog.String("property", referenceAddressProperty(nt)),
				slog.String("err", err.Error()),
			)
		}
	}
	if newAddress != "" {
		if err := s.nodes.WriteAddress(ctx, nt.TypeKey, node.AddressKindPrimary, newAddress, nodeID); err != nil {
			log.WarnContext(ctx, "node.Update: write new address",
				slog.String("address", newAddress),
				slog.String("property", referenceAddressProperty(nt)),
				slog.String("err", err.Error()),
			)
		}
	}
}
