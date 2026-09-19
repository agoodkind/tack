package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"goodkind.io/tack/internal/domain/node"
)

// maxConfirmationNameBytes bounds the name a confirmation prints for a node
// whose name is its only human-readable identity, such as a comment body.
const maxConfirmationNameBytes = 120

const truncatedNameSuffix = "…"

// renderWriteConfirmation reports a successful write without repeating
// property values. Callers read the full node with tack_get_*. The raw id
// stays so a caller can address the node it just wrote without a lookup.
//
// identifierFor falls back to the name, so a node without a declared
// reference, such as a comment, has no reference distinct from its name. Its
// confirmation names it by raw id and prints the name once, shortened, in the
// heading.
func renderWriteConfirmation(rc *renderCtx, verb string, view *node.NodeView, changedFields []string) string {
	nodeType := strings.ToLower(view.NodeType)
	reference := identifierFor(view, rc)
	fields := make([]markdownField, 0, 5)
	var heading string
	if reference == "" || reference == view.Name {
		heading = fmt.Sprintf("%s %s: %s", verb, nodeType, truncateName(view.Name))
	} else {
		heading = fmt.Sprintf("%s %s `%s`", verb, nodeType, reference)
		fields = append(fields,
			markdownCodeFieldValue("Reference", reference),
			markdownFieldValue("Name", view.Name),
		)
	}
	fields = append(fields, markdownCodeFieldValue("Type", view.NodeType))
	if len(changedFields) > 0 {
		sorted := append([]string(nil), changedFields...)
		sort.Strings(sorted)
		fields = append(fields, markdownFieldValue("Changed", strings.Join(sorted, ", ")))
	}
	fields = append(fields, markdownCodeFieldValue("Raw id", view.ID.String()))
	return executeMarkdownTemplate("node.md.tmpl", nodeTemplateData{Heading: heading, Fields: fields})
}

// truncateName folds name onto one heading line and shortens it to
// maxConfirmationNameBytes on a rune boundary.
func truncateName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	if len(name) <= maxConfirmationNameBytes {
		return name
	}
	keep := maxConfirmationNameBytes
	for keep > 0 && !utf8.RuneStart(name[keep]) {
		keep--
	}
	return name[:keep] + truncatedNameSuffix
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
