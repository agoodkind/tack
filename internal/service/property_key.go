package service

import (
	"time"

	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain/issue"
	"goodkind.io/tack/internal/domain/node"
	"github.com/google/uuid"
)

var tackPropNamespace = uuid.MustParse("7ac0face-dead-beef-cafe-000000000000")

// systemPropID returns a deterministic UUID for a built-in workspace-scoped
// property definition. The ID is derived from the workspace ID and property
// name so seeded PropertyDefs always match what the service layer reads.
func systemPropID(workspaceID uuid.UUID, name string) uuid.UUID {
	return uuid.NewSHA1(tackPropNamespace, []byte(workspaceID.String()+":"+name))
}

const (
	propNamePriority  = "priority"
	propNameDueDate   = "due_date"
	propNameStartDate = "start_date"
	propNameIsDraft   = "is_draft"
	propNameStatus    = "status"
	propNameEndDate   = "end_date"
)

// priorityRank maps a Priority to its sort rank for the enum secondary index.
// Lower rank sorts first (urgent = 0, none = 4).
func priorityRank(p issue.Priority) *int32 {
	ranks := map[issue.Priority]int32{
		issue.PriorityUrgent: 0,
		issue.PriorityHigh:   1,
		issue.PriorityMedium: 2,
		issue.PriorityLow:    3,
		issue.PriorityNone:   4,
	}
	r, ok := ranks[p]
	if !ok {
		r = 4
	}
	return &r
}

// tsPV returns a PropertyValue of kind timestamp, or nil if t is nil.
func tsPV(t interface{ UnixNano() int64 }) *node.PropertyValue {
	return nil // unused sentinel; callers use tsPVTime instead
}

// boolPVTrue returns a PropertyValue of kind bool set to true.
func boolPVTrue() *node.PropertyValue {
	b := true
	return &node.PropertyValue{Kind: node.PropertyValueBool, Bool: &b}
}

// boolPVFalse returns a PropertyValue of kind bool set to false.
func boolPVFalse() *node.PropertyValue {
	b := false
	return &node.PropertyValue{Kind: node.PropertyValueBool, Bool: &b}
}

// textPV returns a PropertyValue of kind text.
func textPV(s string) *node.PropertyValue {
	return &node.PropertyValue{Kind: node.PropertyValueText, Text: &s}
}

// _ suppress unused warning for tsPV
var _ = tsPV

// nodeDocFromView builds a full NodeDoc from a materialized NodeListView.
// All filterable and display fields are populated so Meilisearch hits require
// zero follow-up FDB reads to render list rows.
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
