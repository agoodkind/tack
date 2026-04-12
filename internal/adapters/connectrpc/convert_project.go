// Package connectrpc converts between domain types and protobuf messages.
package connectrpc

import (
	"encoding/json"

	v1 "goodkind.io/tack/gen/tack/v1"
	"goodkind.io/tack/internal/domain/node"
)

func protoProjectFromView(view *node.NodeListView) *v1.Project {
	pb := &v1.Project{
		Base:        baseFromFields(view.ID, view.CreatedAt, view.UpdatedAt, nil, nil),
		WorkspaceId: view.WorkspaceID.String(),
		Name:        view.Name,
	}

	// Extract identifier from CustomProps
	if identifierBytes, ok := view.CustomProps["identifier"]; ok {
		var identifier string
		_ = json.Unmarshal(identifierBytes, &identifier)
		pb.Identifier = identifier
	}

	// Extract description from CustomProps
	if descriptionBytes, ok := view.CustomProps["description"]; ok {
		var description string
		_ = json.Unmarshal(descriptionBytes, &description)
		if description != "" {
			pb.Description = &description
		}
	}

	// Extract default_state_id from CustomProps
	if defaultStateBytes, ok := view.CustomProps["default_state_id"]; ok {
		var stateID string
		_ = json.Unmarshal(defaultStateBytes, &stateID)
		if stateID != "" {
			pb.DefaultStateId = &stateID
		}
	}

	return pb
}
