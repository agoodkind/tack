package ops

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
)

func init() {
	Register(Operation{
		Name:        "reference.duplicates",
		Audit:       audit.Spec{Verb: string(audit.VerbOpsReferenceDuplicates), Reads: true},
		Description: "Report every case where two nodes render the same reference for one template their org declared. Read-only.",
		Run:         runReferenceDuplicates,
	})
}

// DuplicateReference identifies one rendered reference held by multiple nodes.
type DuplicateReference struct {
	// OrgID owns every node in the duplicate group.
	OrgID uuid.UUID
	// TemplateName identifies the template that rendered Encoded.
	TemplateName string
	// Encoded is the rendered human-readable reference.
	Encoded string
	// NodeIDs holds the duplicate candidates in stable order.
	NodeIDs []uuid.UUID
}

func runReferenceDuplicates(ctx context.Context, env *Env) error {
	duplicates, err := FindDuplicateReferences(ctx, env)
	if err != nil {
		return err
	}
	for _, duplicate := range duplicates {
		env.Log.WarnContext(ctx, "reference.duplicate",
			slog.String("org_id", duplicate.OrgID.String()),
			slog.String("template", duplicate.TemplateName),
			slog.String("reference", duplicate.Encoded),
			slog.Any("node_ids", duplicate.NodeIDs),
		)
	}
	env.Log.InfoContext(ctx, "reference.duplicates.completed", slog.Int("groups", len(duplicates)))
	return nil
}

// FindDuplicateReferences returns each rendered reference held by multiple
// nodes. It groups template name and rendered value across node types.
func FindDuplicateReferences(ctx context.Context, env *Env) ([]DuplicateReference, error) {
	orgIDs, err := listOrgIDs(ctx, env)
	if err != nil {
		return nil, err
	}
	duplicates := make([]DuplicateReference, 0)
	for orgID := range orgIDs {
		orgDuplicates, findErr := findOrgDuplicateReferences(ctx, env, orgID)
		if findErr != nil {
			wrapped := fmt.Errorf("find duplicate references for org %s: %w", orgID, findErr)
			env.Log.WarnContext(ctx, "reference.duplicates.org_failed",
				slog.String("org_id", orgID.String()),
				slog.String("err", wrapped.Error()),
			)
			return nil, wrapped
		}
		duplicates = append(duplicates, orgDuplicates...)
	}
	sort.Slice(duplicates, func(i, j int) bool {
		if duplicates[i].OrgID != duplicates[j].OrgID {
			return duplicates[i].OrgID.String() < duplicates[j].OrgID.String()
		}
		if duplicates[i].TemplateName != duplicates[j].TemplateName {
			return duplicates[i].TemplateName < duplicates[j].TemplateName
		}
		return duplicates[i].Encoded < duplicates[j].Encoded
	})
	return duplicates, nil
}

func findOrgDuplicateReferences(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
) ([]DuplicateReference, error) {
	nodeTypes, err := env.Stores.NodeTypes.List(ctx, orgID)
	if err != nil {
		wrapped := fmt.Errorf("list node types for org %s: %w", orgID, err)
		env.Log.WarnContext(ctx, "reference.duplicates.types_failed",
			slog.String("org_id", orgID.String()),
			slog.String("err", wrapped.Error()),
		)
		return nil, wrapped
	}
	typeIndex := node.BuildTypeIndex(nodeTypes)
	holders := make(map[string][]uuid.UUID)
	templateNames := make(map[string]string)
	encodedValues := make(map[string]string)
	scopeCache := make(map[uuid.UUID]map[string]string)
	for _, nodeType := range nodeTypes {
		if len(nodeType.ReferenceTemplates) == 0 {
			continue
		}
		views, listErr := env.Stores.Views.List(ctx, node.NodeListQuery{
			OrgID:            orgID,
			NodeType:         nodeType.TypeKey,
			ByProperty:       nil,
			BySourceRelation: nil,
			ByTargetRelation: nil,
			CreatedAfter:     nil,
			CreatedBefore:    nil,
			PropFilters:      nil,
			Limit:            0,
			Cursor:           "",
		})
		if listErr != nil {
			wrapped := fmt.Errorf("list %s nodes in org %s: %w", nodeType.TypeKey, orgID, listErr)
			env.Log.WarnContext(ctx, "reference.duplicates.views_failed",
				slog.String("org_id", orgID.String()),
				slog.String("node_type", nodeType.TypeKey),
				slog.String("err", wrapped.Error()),
			)
			return nil, wrapped
		}
		for _, view := range views {
			collectErr := collectRenderedReferences(
				ctx, env, orgID, nodeType, typeIndex, view,
				holders, templateNames, encodedValues, scopeCache,
			)
			if collectErr != nil {
				return nil, collectErr
			}
		}
	}
	return duplicateGroups(orgID, holders, templateNames, encodedValues), nil
}

func collectRenderedReferences(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	nodeType *node.NodeType,
	typeIndex map[string]*node.NodeType,
	view *node.NodeView,
	holders map[string][]uuid.UUID,
	templateNames map[string]string,
	encodedValues map[string]string,
	scopeCache map[uuid.UUID]map[string]string,
) error {
	scopeRefs, err := scopeReferencesForView(ctx, env, orgID, typeIndex, view, scopeCache)
	if err != nil {
		return err
	}
	input := node.ReferenceRenderInput{
		NodeTypeKey: nodeType.TypeKey,
		Props:       view.Props,
		ScopeRefs:   scopeRefs,
	}
	for _, template := range nodeType.ReferenceTemplates {
		encoded, renderErr := template.Render(input)
		if renderErr != nil {
			// A node whose reference cannot render is invisible to the
			// report, so the omission must be loud or the blast-radius count
			// silently understates.
			env.Log.WarnContext(ctx, "reference.duplicates.render_failed",
				slog.String("org_id", orgID.String()),
				slog.String("node_type", nodeType.TypeKey),
				slog.String("node_id", view.ID.String()),
				slog.String("template", template.Name),
				slog.String("err", renderErr.Error()),
			)
			continue
		}
		group := template.Name + "\x00" + encoded
		holders[group] = append(holders[group], view.ID)
		templateNames[group] = template.Name
		encodedValues[group] = encoded
	}
	return nil
}

func duplicateGroups(
	orgID uuid.UUID,
	holders map[string][]uuid.UUID,
	templateNames map[string]string,
	encodedValues map[string]string,
) []DuplicateReference {
	duplicates := make([]DuplicateReference, 0)
	for group, nodeIDs := range holders {
		if len(nodeIDs) < 2 {
			continue
		}
		sortedNodeIDs := append([]uuid.UUID(nil), nodeIDs...)
		sort.Slice(sortedNodeIDs, func(i, j int) bool {
			return sortedNodeIDs[i].String() < sortedNodeIDs[j].String()
		})
		duplicates = append(duplicates, DuplicateReference{
			OrgID:        orgID,
			TemplateName: templateNames[group],
			Encoded:      encodedValues[group],
			NodeIDs:      sortedNodeIDs,
		})
	}
	return duplicates
}
