package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func renderNode(rc *renderCtx, view *node.NodeView) string {
	if view == nil {
		return executeMarkdownTemplate("node.md.tmpl", nodeTemplateData{Heading: "Node not found"})
	}
	ident := identifierFor(view, rc)
	heading := view.Name
	if ident != "" && ident != view.Name {
		heading = fmt.Sprintf("`%s`: %s", ident, view.Name)
	}
	fields := nodeFields(rc, view)
	return executeMarkdownTemplate("node.md.tmpl", nodeTemplateData{Heading: heading, Fields: fields})
}

func renderDeletedNode(rc *renderCtx, view *node.NodeView, deletedAt time.Time) string {
	ident := identifierFor(view, rc)
	nodeType := strings.ToLower(view.NodeType)
	if ident == "" {
		ident = view.Name
	}
	fields := []markdownField{
		markdownCodeFieldValue("Identifier", ident),
		markdownCodeFieldValue("Type", view.NodeType),
		markdownFieldValue("Deleted at", formatDisplayTime(deletedAt)),
	}
	data := nodeTemplateData{Heading: fmt.Sprintf("Deleted %s `%s`", nodeType, ident), Fields: fields}
	return executeMarkdownTemplate("node.md.tmpl", data)
}

func nodeFields(rc *renderCtx, view *node.NodeView) []markdownField {
	fields := make([]markdownField, 0, len(view.Props)+6)
	fields = appendRefField(fields, rc, "Scope", uuidProp(view, "scope_id"))
	fields = appendRefField(fields, rc, "Parent", uuidProp(view, "parent_id"))
	fields = appendRefField(fields, rc, "State", uuidProp(view, "state_id"))
	for _, key := range sortedScalarPropKeys(view) {
		value := propAsString(view.Props[key])
		if value != "" {
			fields = append(fields, markdownFieldValue(humanLabel(key), value))
		}
	}
	fields = appendAssignees(fields, rc, uuidArrayProp(view, "assignee_ids"))
	fields = appendUserField(fields, rc, "Created", view.CreatedAt, view.CreatedBy)
	fields = appendUserField(fields, rc, "Updated", view.UpdatedAt, view.UpdatedBy)
	fields = append(fields, markdownCodeFieldValue("Raw id", view.ID.String()))
	return fields
}

func appendRefField(fields []markdownField, rc *renderCtx, label string, id uuid.UUID) []markdownField {
	if id == uuid.Nil || rc == nil || rc.reader == nil {
		return fields
	}
	ident, _, nodeType := rc.nodeSummary(id)
	if ident == "" {
		return append(fields, markdownFieldValue(label, fmt.Sprintf("missing `%s`", id)))
	}
	return append(fields, markdownFieldValue(label, fmt.Sprintf("`%s` (`%s`)", ident, nodeType)))
}

func appendAssignees(fields []markdownField, rc *renderCtx, assignees []uuid.UUID) []markdownField {
	if len(assignees) == 0 || rc == nil {
		return fields
	}
	names := make([]string, 0, len(assignees))
	for _, userID := range assignees {
		name := rc.userDisplay(userID)
		if name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return fields
	}
	return append(fields, markdownFieldValue("Assignees", strings.Join(names, ", ")))
}

func appendUserField(fields []markdownField, rc *renderCtx, label string, t time.Time, by uuid.UUID) []markdownField {
	stamp := formatDisplayTime(t)
	if stamp == "" {
		return fields
	}
	name := ""
	if rc != nil {
		name = rc.userDisplay(by)
	}
	if name != "" {
		return append(fields, markdownFieldValue(label, fmt.Sprintf("%s by %s", stamp, name)))
	}
	return append(fields, markdownFieldValue(label, stamp))
}
