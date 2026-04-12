package service

import (
	"time"

	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain/node"
	"github.com/google/uuid"
)

var tackPropNamespace = uuid.MustParse("7ac0face-dead-beef-cafe-000000000000")

func systemPropID(workspaceID uuid.UUID, name string) uuid.UUID {
	return uuid.NewSHA1(tackPropNamespace, []byte(workspaceID.String()+":"+name))
}

const (
	propNamePriority       = "priority"
	propNameDueDate        = "due_date"
	propNameStartDate      = "start_date"
	propNameIsDraft        = "is_draft"
	propNameStatus         = "status"
	propNameEndDate        = "end_date"
	propNameSlug           = "slug"
	propNameIdentifier     = "identifier"
	propNameDescription    = "description"
	propNameNetwork        = "network"
	propNameDefaultStateID = "default_state_id"
	propNameGroupName      = "group_name"
	propNameColor          = "color"
	propNameSortOrder      = "sort_order"
)

func nodeDocFromView(v *node.NodeListView) domainsearch.NodeDoc {
	doc := domainsearch.NodeDoc{
		ID:          v.ID.String(),
		OrgID:       v.OrgID.String(),
		WorkspaceID: v.WorkspaceID.String(),
		ProjectID:   v.ProjectID.String(),
		EntityType:  v.NodeType,
		Name:        v.Name,
		Description: v.Description,
		SequenceID:  v.SequenceID,
		Priority:    v.Priority,
		IsDraft:     v.IsDraft,
		UpdatedAt:   v.UpdatedAt.UTC().Format(time.RFC3339),
		CreatedAt:   v.CreatedAt.UTC().Format(time.RFC3339),
	}
	if v.StateID != nil {
		doc.StateID = v.StateID.String()
	}
	if v.EpicID != nil {
		doc.EpicID = v.EpicID.String()
	}
	if v.StartDate != nil {
		doc.StartDate = v.StartDate.UTC().Format(time.RFC3339)
	}
	if v.DueDate != nil {
		doc.DueDate = v.DueDate.UTC().Format(time.RFC3339)
	}
	if len(v.AssigneeIDs) > 0 {
		doc.AssigneeIDs = make([]string, len(v.AssigneeIDs))
		for i, id := range v.AssigneeIDs {
			doc.AssigneeIDs[i] = id.String()
		}
	}
	if len(v.LabelIDs) > 0 {
		doc.LabelIDs = make([]string, len(v.LabelIDs))
		for i, id := range v.LabelIDs {
			doc.LabelIDs[i] = id.String()
		}
	}
	return doc
}
