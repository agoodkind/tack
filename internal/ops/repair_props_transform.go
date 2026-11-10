package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

func (c *RepairConsole) planPropsTransform(
	ctx context.Context,
	nodeID uuid.UUID,
	profile *RepairReferenceProfile,
) (*repairPlan, error) {
	preparedProfile, err := preparePropsTransformProfile(ctx, profile)
	if err != nil {
		return nil, err
	}
	view, err := c.reader.Get(ctx, nodeID)
	if err != nil {
		return nil, loggedRepairError(ctx, fmt.Sprintf("get repair node %s", nodeID), err)
	}
	if view == nil {
		return nil, domain.ErrNotFound
	}
	preview := &RepairPreview{
		Class:                RepairClassPropsTransform,
		ProfileName:          preparedProfile.Name,
		NodeID:               view.ID,
		NodeType:             view.NodeType,
		CurrentUpdatedAt:     view.UpdatedAt,
		TargetProperty:       "props",
		TargetType:           "",
		ObservedSources:      nil,
		Candidates:           nil,
		ChosenCandidate:      nil,
		PlannedProps:         plannedPropsTransform(view, preparedProfile),
		PlannedRelationships: nil,
		Summary:              "",
		ConfirmationToken:    "",
		NeedsRepair:          false,
		CanApply:             false,
	}
	preview.NeedsRepair = len(preview.PlannedProps) > 0
	preview.CanApply = preview.NeedsRepair
	preview.Summary = summarizePropsTransform(preview)
	if preview.CanApply {
		preview.ConfirmationToken = repairConfirmationToken(preview)
	}
	return &repairPlan{
		preview:             preview,
		props:               preview.PlannedProps,
		relationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil},
	}, nil
}

func preparePropsTransformProfile(
	ctx context.Context,
	profile *RepairReferenceProfile,
) (RepairReferenceProfile, error) {
	if profile == nil {
		return RepairReferenceProfile{}, loggedRepairError(ctx, "props transform profile required", domain.ErrInvalidArgument)
	}
	prepared := *profile
	prepared.RemoveFields = compactStrings(prepared.RemoveFields)
	for index := range prepared.RenameFields {
		prepared.RenameFields[index].From = strings.TrimSpace(prepared.RenameFields[index].From)
		prepared.RenameFields[index].To = strings.TrimSpace(prepared.RenameFields[index].To)
	}
	for index := range prepared.AppendTextFields {
		prepared.AppendTextFields[index].From = strings.TrimSpace(prepared.AppendTextFields[index].From)
		prepared.AppendTextFields[index].To = strings.TrimSpace(prepared.AppendTextFields[index].To)
	}
	if len(prepared.RemoveFields) == 0 && len(prepared.RenameFields) == 0 && len(prepared.AppendTextFields) == 0 {
		return RepairReferenceProfile{}, loggedRepairError(ctx, "props transform operation required", domain.ErrInvalidArgument)
	}
	return prepared, nil
}

func plannedPropsTransform(
	view *node.NodeView,
	profile RepairReferenceProfile,
) map[string]json.RawMessage {
	props := make(map[string]json.RawMessage)
	for _, rename := range profile.RenameFields {
		planRenameField(view, props, rename)
	}
	for _, appendText := range profile.AppendTextFields {
		planAppendTextField(view, props, appendText)
	}
	for _, field := range profile.RemoveFields {
		if _, ok := view.Props[field]; ok {
			props[field] = json.RawMessage("null")
		}
	}
	return props
}

func planRenameField(
	view *node.NodeView,
	props map[string]json.RawMessage,
	rename RepairRenameField,
) {
	if rename.From == "" || rename.To == "" {
		return
	}
	raw, ok := view.Props[rename.From]
	if !ok {
		return
	}
	if _, exists := view.Props[rename.To]; exists && !rename.Overwrite {
		return
	}
	props[rename.To] = append(json.RawMessage(nil), raw...)
	props[rename.From] = json.RawMessage("null")
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
	props[appendText.From] = json.RawMessage("null")
}

func transformTargetRaw(
	view *node.NodeView,
	props map[string]json.RawMessage,
	field string,
) json.RawMessage {
	if raw, ok := props[field]; ok && !isRepairDeletedProp(raw) {
		return raw
	}
	return view.Props[field]
}

func isRepairDeletedProp(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

func summarizePropsTransform(preview *RepairPreview) string {
	if preview == nil || !preview.NeedsRepair {
		return "props already match transform profile"
	}
	return fmt.Sprintf("apply %d prop changes", len(preview.PlannedProps))
}
