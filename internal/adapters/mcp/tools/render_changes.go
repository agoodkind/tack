package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	// maxChangeValueBytes limits the text retained for each value in a change line
	// before shortenText adds an ellipsis.
	maxChangeValueBytes = 40
	nameFieldKey        = "name"
)

// builtinReferenceKeys lists the built-in property keys that identify reference targets.
var builtinReferenceKeys = []string{"parent_id", "scope_id"}

// writeChanges stores the requested fields and the node before and after a write.
// Before is nil for a create.
type writeChanges struct {
	Before        *node.NodeView
	After         *node.NodeView
	Name          *string
	Props         map[string]json.RawMessage
	ReferenceKeys map[string]struct{}
}

// writeReferenceKeys returns the built-in reference keys and the property keys
// that the organization's property definitions declare as references.
func writeReferenceKeys(ctx context.Context, b NodeTypeBinding, orgID uuid.UUID) (map[string]struct{}, error) {
	defs, err := b.PropertyDefs.List(ctx, orgID)
	if err != nil {
		slog.ErrorContext(ctx, "mcp.write_confirmation_defs_failed", slog.String("org_id", orgID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("list property defs of org %s for the write confirmation: %w", orgID, err)
	}
	keys := make(map[string]struct{}, len(defs)+len(builtinReferenceKeys))
	for _, key := range builtinReferenceKeys {
		keys[key] = struct{}{}
	}
	for _, def := range defs {
		if def.ReferenceTargetTypeKey != "" {
			keys[def.Name] = struct{}{}
		}
	}
	return keys, nil
}

// describeFieldChanges returns one field per requested key, sorted by key.
// It includes the requested name only on update because create already prints the name.
func describeFieldChanges(rc *renderCtx, changes writeChanges) []markdownField {
	fieldKeys := make([]string, 0, len(changes.Props)+1)
	if changes.Name != nil && changes.Before != nil {
		fieldKeys = append(fieldKeys, nameFieldKey)
	}
	for key := range changes.Props {
		fieldKeys = append(fieldKeys, key)
	}
	sort.Strings(fieldKeys)
	fields := make([]markdownField, 0, len(fieldKeys))
	for _, key := range fieldKeys {
		fields = append(fields, describeFieldChange(rc, changes, key))
	}
	return fields
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

// storedValue reads the name or property value from view and reports its presence.
// It treats a nil view, a missing property, empty JSON, and JSON null as absent.
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

// display formats a shortened value as Markdown code.
// For reference values, it prints the target reference when the caller may see
// the target; otherwise, it prints the raw id.
func (changes writeChanges) display(rc *renderCtx, key string, raw json.RawMessage) string {
	text := scalarOrCompactJSON(raw)
	if _, isReference := changes.ReferenceKeys[key]; isReference {
		text = changes.referenceText(rc, text)
	}
	text = strings.ReplaceAll(text, "`", "'")
	return markdownCodeValue(shortenText(text, maxChangeValueBytes))
}

func (changes writeChanges) referenceText(rc *renderCtx, text string) string {
	targetID, err := uuid.Parse(text)
	if err != nil {
		return text
	}
	identifier, _ := rc.nodeSummaryInOrg(targetID, changes.After.OrgID)
	if identifier == "" {
		return text
	}
	return identifier
}
