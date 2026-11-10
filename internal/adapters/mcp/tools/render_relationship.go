package tools

import (
	"fmt"

	"github.com/google/uuid"
)

func renderRelationshipMutation(rc *renderCtx, heading string, sourceID uuid.UUID, relType string, targetID uuid.UUID) string {
	fields := []markdownField{
		relationshipEndpointField(rc, "Source", sourceID),
		markdownCodeFieldValue("Relation type", relType),
		relationshipEndpointField(rc, "Target", targetID),
	}
	return executeMarkdownTemplate("node.md.tmpl", nodeTemplateData{Heading: heading, Fields: fields})
}

func relationshipEndpointField(rc *renderCtx, label string, id uuid.UUID) markdownField {
	ident, name, nodeType := rc.nodeSummary(id)
	if ident == "" {
		return markdownCodeFieldValue(label, id.String())
	}
	value := fmt.Sprintf("`%s`", ident)
	if nodeType != "" {
		value = fmt.Sprintf("%s (`%s`)", value, nodeType)
	}
	if name != "" && name != ident {
		value = fmt.Sprintf("%s, %s", value, name)
	}
	return markdownFieldValue(label, value)
}
