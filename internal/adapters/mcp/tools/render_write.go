package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"goodkind.io/tack/internal/domain/node"
)

// maxConfirmationNameBytes limits the displayed name when a node uses its
// name as its only human-readable identity, such as a comment body.
const maxConfirmationNameBytes = 120

const (
	truncatedNameSuffix = "…"
	// maxConfirmationBytes sets the TACK-508 byte limit. maxChangeLines limits
	// the number of change lines. The renderer drops change lines while the
	// response exceeds the byte limit and still includes change lines.
	maxConfirmationBytes = 1024
	maxChangeLines       = 4
)

// renderWriteConfirmation prints the heading, Reference, Name, and Type,
// then up to maxChangeLines lines from describeFieldChanges, with Raw id
// last. If it omits change lines, it adds one More line with their count and
// tack_get_<type>. When a node lacks a distinct reference, such as a
// comment, the heading prints its shortened name once and the fields omit
// Reference and Name.
func renderWriteConfirmation(rc *renderCtx, verb string, view *node.NodeView, changes fieldChanges) string {
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
			markdownFieldValue("Name", truncateName(view.Name)),
		)
	}
	fields = append(fields, markdownCodeFieldValue("Type", view.NodeType))
	rawID := markdownCodeFieldValue("Raw id", view.ID.String())
	shown := len(changes.Shown)
	for {
		hidden := moreChangesField(rc, view, changes.Total-shown)
		text := renderConfirmationFields(heading, fields, changes.Shown[:shown], hidden, rawID)
		if len(text) <= maxConfirmationBytes || shown == 0 {
			return text
		}
		shown--
	}
}

func renderConfirmationFields(heading string, fields, shown, more []markdownField, rawID markdownField) string {
	all := make([]markdownField, 0, len(fields)+len(shown)+len(more)+1)
	all = append(all, fields...)
	all = append(all, shown...)
	all = append(all, more...)
	all = append(all, rawID)
	return executeMarkdownTemplate("node.md.tmpl", nodeTemplateData{Heading: heading, Fields: all})
}

func moreChangesField(rc *renderCtx, view *node.NodeView, hidden int) []markdownField {
	if hidden <= 0 {
		return nil
	}
	slug := strings.ToLower(view.NodeType)
	if nodeType := rc.typeIndex[view.NodeType]; nodeType != nil {
		slug = strings.ToLower(nodeType.Slug)
	}
	noun := "fields"
	if hidden == 1 {
		noun = "field"
	}
	value := fmt.Sprintf("the request covered %d more %s, and `tack_get_%s` returns the full node", hidden, noun, slug)
	return []markdownField{{Label: "More", Value: value}}
}

// truncateName combines whitespace into one heading line and retains at most
// maxConfirmationNameBytes bytes of the name at a UTF-8 character boundary
// before adding an ellipsis.
func truncateName(name string) string {
	return shortenText(name, maxConfirmationNameBytes)
}

// shortenText combines whitespace into single spaces and retains at most
// limit bytes at a UTF-8 character boundary. It adds an ellipsis when it
// shortens the text.
func shortenText(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}
	keep := limit
	for keep > 0 && !utf8.RuneStart(text[keep]) {
		keep--
	}
	return text[:keep] + truncatedNameSuffix
}

// shortenTextStart combines whitespace into single spaces and retains at
// most the last limit bytes at a UTF-8 character boundary. It adds an
// ellipsis before shortened text.
func shortenTextStart(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}
	keep := len(text) - limit
	for keep < len(text) && !utf8.RuneStart(text[keep]) {
		keep++
	}
	return truncatedNameSuffix + text[keep:]
}
