package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

const (
	changeLabelChanged   = "Changed"
	changeLabelSet       = "Set"
	changeLabelRemoved   = "Removed"
	changeLabelUnchanged = "Unchanged"
	maxChangeValueBytes  = 40
	nameFieldKey         = "name"
	// emptyStringText represents an empty string as two double quotes
	// in a Markdown code span.
	emptyStringText = "`\"\"`"
)

var builtinReferenceKeys = []string{"parent_id", "scope_id"}

type writeChanges struct {
	Before        *node.NodeView
	After         *node.NodeView
	Name          *string
	Props         map[string]json.RawMessage
	ReferenceKeys map[string]struct{}
	targets       map[uuid.UUID]string `exhaustruct:"optional"`
}

// fieldChanges stores up to maxChangeLines fields to print and the total count
// of requested fields.
type fieldChanges struct {
	Shown []markdownField
	Total int
}

// referenceKeysOf returns the built-in reference keys and the property keys
// that defs declare as references.
func referenceKeysOf(defs []*node.PropertyDef) map[string]struct{} {
	keys := make(map[string]struct{}, len(defs)+len(builtinReferenceKeys))
	for _, key := range builtinReferenceKeys {
		keys[key] = struct{}{}
	}
	for _, def := range defs {
		if def.ReferenceTargetTypeKey != "" {
			keys[def.Name] = struct{}{}
		}
	}
	return keys
}

// describeFieldChanges sorts requested keys, formats only the first maxChangeLines,
// and counts all requested keys. It reads each reference target it formats.
// It includes the name key only on update because create already prints the name.
func describeFieldChanges(rc *renderCtx, changes writeChanges) fieldChanges {
	fieldKeys := make([]string, 0, len(changes.Props)+1)
	if changes.Name != nil && changes.Before != nil {
		fieldKeys = append(fieldKeys, nameFieldKey)
	}
	for key := range changes.Props {
		fieldKeys = append(fieldKeys, key)
	}
	sort.Strings(fieldKeys)
	changes.targets = map[uuid.UUID]string{}
	shown := make([]markdownField, 0, maxChangeLines)
	for _, key := range fieldKeys[:min(len(fieldKeys), maxChangeLines)] {
		shown = append(shown, describeFieldChange(rc, changes, key))
	}
	return fieldChanges{Shown: shown, Total: len(fieldKeys)}
}

func describeFieldChange(rc *renderCtx, changes writeChanges, key string) markdownField {
	before, hadBefore := changes.storedValue(changes.Before, key)
	after, hasAfter := changes.storedValue(changes.After, key)
	field := markdownCodeValue(key)
	switch {
	case !hadBefore && !hasAfter:
		return markdownField{Label: changeLabelUnchanged, Value: field + " is not set"}
	case !hadBefore:
		return markdownField{Label: changeLabelSet, Value: fmt.Sprintf("%s to %s", field, changes.display(rc, key, after))}
	case !hasAfter:
		return markdownField{Label: changeLabelRemoved, Value: fmt.Sprintf("%s (was %s)", field, changes.display(rc, key, before))}
	case sameJSON(before, after):
		return markdownField{Label: changeLabelUnchanged, Value: fmt.Sprintf("%s is %s", field, changes.display(rc, key, after))}
	default:
		return markdownField{
			Label: changeLabelChanged,
			Value: fmt.Sprintf("%s from %s to %s", field, changes.display(rc, key, before), changes.display(rc, key, after)),
		}
	}
}

func (changes writeChanges) storedValue(view *node.NodeView, key string) (json.RawMessage, bool) {
	if view == nil {
		return nil, false
	}
	if key == nameFieldKey && changes.Name != nil {
		return mustRawString(view.Name), true
	}
	raw, ok := view.Props[key]
	if !ok || isAbsentJSON(raw) {
		return nil, false
	}
	return raw, true
}

// display prints a reference value as the target reference when the caller may
// see the target; otherwise, it prints the raw id. It shortens long references
// from the start to keep the target name visible.
// It prints an empty string as two double quotes in a Markdown code span.
func (changes writeChanges) display(rc *renderCtx, key string, raw json.RawMessage) string {
	text := scalarOrCompactJSON(raw)
	_, isReference := changes.ReferenceKeys[key]
	if isReference {
		text = changes.referenceText(rc, text)
	}
	text = strings.ReplaceAll(text, "`", "'")
	if isReference {
		text = shortenTextStart(text, maxChangeValueBytes)
	} else {
		text = shortenText(text, maxChangeValueBytes)
	}
	if text == "" {
		return emptyStringText
	}
	return markdownCodeValue(text)
}

// referenceText reads each target at most once per confirmation.
// It uses its own map instead of rc.cache because nodeSummaryInOrg checks
// organization access, while rc.cache entries from other callers skip that check.
func (changes writeChanges) referenceText(rc *renderCtx, text string) string {
	targetID, err := uuid.Parse(text)
	if err != nil {
		return text
	}
	if identifier, read := changes.targets[targetID]; read {
		return identifier
	}
	identifier, _ := rc.nodeSummaryInOrg(targetID, changes.After.OrgID)
	if identifier == "" {
		identifier = text
	}
	changes.targets[targetID] = identifier
	return identifier
}
