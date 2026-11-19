package ops

import (
	"encoding/json"
	"strings"

	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

func planRenameField(
	view *node.NodeView,
	props map[string]json.RawMessage,
	rename RepairRenameField,
) {
	if rename.From == "" || rename.To == "" {
		return
	}

	raw, provided := view.Props[rename.From]
	if !provided {
		return
	}

	if _, exists := view.Props[rename.To]; exists && !rename.Overwrite {
		return
	}

	props[rename.To] = append(json.RawMessage(nil), raw...)
	props[rename.From] = json.RawMessage(transformedNullProperty)
}

func planAppendTextField(
	view *node.NodeView,
	props map[string]json.RawMessage,
	appendText RepairAppendTextField,
) {
	if appendText.From == "" || appendText.To == "" {
		return
	}

	sourceText := strings.TrimSpace(rawStringValue(view.Props[appendText.From]))
	if sourceText == "" {
		return
	}

	targetText := strings.TrimSpace(rawStringValue(transformTargetRaw(view, props, appendText.To)))
	separator := appendText.Separator
	if separator == "" {
		separator = "\n\n"
	}

	section := sourceText
	if strings.TrimSpace(appendText.Heading) != "" {
		section = strings.TrimSpace(appendText.Heading) + "\n" + sourceText
	}

	mergedText := section
	if targetText != "" {
		mergedText = targetText + separator + section
	}

	props[appendText.To] = service.MustRawString(mergedText)
	props[appendText.From] = json.RawMessage(transformedNullProperty)
}

func transformTargetRaw(
	view *node.NodeView,
	props map[string]json.RawMessage,
	field string,
) json.RawMessage {
	if raw, provided := props[field]; provided && !isRepairDeletedProp(raw) {
		return raw
	}

	return view.Props[field]
}

func isRepairDeletedProp(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == transformedNullProperty
}

const transformedNullProperty = "null"
