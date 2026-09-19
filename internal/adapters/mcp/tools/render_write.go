package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"goodkind.io/tack/internal/domain/node"
)

// renderWriteConfirmation reports a successful write without repeating
// property values. Callers read the full node with tack_get_*. The raw id
// stays so a caller can address the node it just wrote without a lookup.
func renderWriteConfirmation(rc *renderCtx, verb string, view *node.NodeView, changedFields []string) string {
	reference := identifierFor(view, rc)
	if reference == "" {
		reference = view.ID.String()
	}
	fields := []markdownField{
		markdownCodeFieldValue("Reference", reference),
		markdownFieldValue("Name", view.Name),
		markdownCodeFieldValue("Type", view.NodeType),
	}
	if len(changedFields) > 0 {
		sorted := append([]string(nil), changedFields...)
		sort.Strings(sorted)
		fields = append(fields, markdownFieldValue("Changed", strings.Join(sorted, ", ")))
	}
	fields = append(fields, markdownCodeFieldValue("Raw id", view.ID.String()))
	heading := fmt.Sprintf("%s %s `%s`", verb, strings.ToLower(view.NodeType), reference)
	return executeMarkdownTemplate("node.md.tmpl", nodeTemplateData{Heading: heading, Fields: fields})
}

func changedFieldNames(name *string, props map[string]json.RawMessage) []string {
	names := make([]string, 0, len(props)+1)
	if name != nil {
		names = append(names, "name")
	}
	for key := range props {
		names = append(names, key)
	}
	return names
}
