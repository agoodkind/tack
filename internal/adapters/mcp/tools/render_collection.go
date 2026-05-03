package tools

import (
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/user"
)

func renderList(rc *renderCtx, kind string, views []*node.NodeView) string {
	items := make([]markdownItem, 0, len(views))
	for _, view := range views {
		items = append(items, nodeListItem(rc, view))
	}
	data := collectionTemplateData{Heading: titleText(kind), Count: len(views), Noun: kind, Items: items}
	return executeMarkdownTemplate("collection.md.tmpl", data)
}

func renderWorkspaceDescribe(rc *renderCtx, ws *node.NodeView, types []nodeTypeSummary, children []*node.NodeView) string {
	items := make([]markdownItem, 0, len(types))
	for _, nodeType := range types {
		fields := []markdownField{
			markdownFieldValue("Name", nodeType.Name),
			markdownCodeFieldValue("Plural", nodeType.PluralSlug),
		}
		if len(nodeType.Features) > 0 {
			fields = append(fields, markdownFieldValue("Features", codeList(nodeType.Features)))
		}
		if ref := referenceSummary(nodeType.Reference); ref != "" {
			fields = append(fields, markdownCodeFieldValue("Reference", ref))
		}
		items = append(items, markdownItem{Title: markdownCodeValue(nodeType.Slug), Fields: fields})
	}
	childrenText := ""
	if len(children) > 0 {
		childrenText = renderList(rc, "children", children)
	}
	data := workspaceDescribeTemplateData{Node: renderNode(rc, ws), NodeTypes: items, Children: childrenText}
	return executeMarkdownTemplate("workspace_describe.md.tmpl", data)
}

func renderMembers(users []*user.User) string {
	items := make([]markdownItem, 0, len(users))
	for _, user := range users {
		name := user.DisplayName
		if name == "" {
			name = user.Email
		}
		fields := []markdownField{
			markdownCodeFieldValue("Operational user id", user.ID.String()),
			markdownFieldValue("Email", user.Email),
		}
		items = append(items, markdownItem{Title: name, Fields: fields})
	}
	data := collectionTemplateData{Heading: "Members", Count: len(users), Noun: "members", Items: items}
	return executeMarkdownTemplate("collection.md.tmpl", data)
}

func renderRelationships(rc *renderCtx, direction string, rels []*node.Relationship, otherIsTarget bool) string {
	items := make([]markdownItem, 0, len(rels))
	label := "Source"
	if otherIsTarget {
		label = "Target"
	}
	for _, rel := range rels {
		items = append(items, relationshipListItem(rc, rel, label, otherIsTarget))
	}
	data := collectionTemplateData{Heading: fmt.Sprintf("%s relationships", titleText(direction)), Count: len(rels), Noun: "relationships", Items: items}
	return executeMarkdownTemplate("collection.md.tmpl", data)
}

func renderSearchViews(rc *renderCtx, views []*node.NodeView) string {
	items := make([]markdownItem, 0, len(views))
	for _, view := range views {
		fields := []markdownField{
			markdownFieldValue("Name", view.Name),
			markdownCodeFieldValue("Type", view.NodeType),
		}
		if state := rc.nodeIdentifier(uuidProp(view, "state_id")); state != "" {
			fields = append(fields, markdownCodeFieldValue("State", state))
		}
		items = append(items, markdownItem{Title: markdownCodeValue(identifierFor(view, rc)), Fields: fields})
	}
	data := collectionTemplateData{Heading: "Search results", Count: len(views), Noun: "results", Items: items}
	return executeMarkdownTemplate("collection.md.tmpl", data)
}

func nodeListItem(rc *renderCtx, view *node.NodeView) markdownItem {
	ident := identifierFor(view, rc)
	if ident == "" {
		ident = view.Name
	}
	fields := []markdownField{
		markdownFieldValue("Name", view.Name),
		markdownCodeFieldValue("Type", view.NodeType),
	}
	if state := rc.nodeIdentifier(uuidProp(view, "state_id")); state != "" {
		fields = append(fields, markdownCodeFieldValue("State", state))
	}
	if priority := stringProp(view, "priority"); priority != "" {
		fields = append(fields, markdownCodeFieldValue("Priority", priority))
	}
	fields = appendNestedAssigned(fields, rc, uuidArrayProp(view, "assignee_ids"))
	return markdownItem{Title: markdownCodeValue(ident), Fields: fields}
}

func relationshipListItem(rc *renderCtx, rel *node.Relationship, label string, otherIsTarget bool) markdownItem {
	otherID := rel.SourceID
	if otherIsTarget {
		otherID = rel.TargetID
	}
	ident, name, _ := rc.nodeSummary(otherID)
	if ident == "" {
		ident = otherID.String()
	}
	fields := []markdownField{markdownCodeFieldValue(label, ident)}
	if name != "" {
		fields = append(fields, markdownFieldValue(label+" name", name))
	}
	return markdownItem{Title: markdownCodeValue(rel.RelationType), Fields: fields}
}

func appendNestedAssigned(fields []markdownField, rc *renderCtx, assignees []uuid.UUID) []markdownField {
	if len(assignees) == 0 || rc == nil || rc.users == nil {
		return append(fields, markdownFieldValue("Assigned", "unassigned"))
	}
	if len(assignees) == 1 {
		return append(fields, markdownFieldValue("Assigned", rc.userDisplay(assignees[0])))
	}
	return append(fields, markdownFieldValue("Assigned", fmt.Sprintf("%d users", len(assignees))))
}
