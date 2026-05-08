package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

type referenceMatchContext struct {
	reader        node.NodeReader
	typeIndex     map[string]*node.NodeType
	orgID         uuid.UUID
	scopeID       uuid.UUID
	targetType    *node.NodeType
	rankProperty  string
	matchProperty string
}

func collectReferenceCandidates(ctx context.Context, view *node.NodeView, profile RepairReferenceProfile, def *node.PropertyDef, matchContext referenceMatchContext, observed *[]RepairObservedSource) []RepairReferenceCandidate {
	candidates := make([]RepairReferenceCandidate, 0, len(profile.SourceFields)+2)
	for _, field := range profile.SourceFields {
		source := observedReferenceSource(view, field, profile.Normalization)
		*observed = append(*observed, source)
		if !source.Present || source.Normalized == "" {
			continue
		}
		candidates = append(candidates, matchContext.candidate(ctx, field, source.Normalized))
	}
	if targetRef := stringProp(view.Props, profile.TargetProperty); strings.TrimSpace(targetRef) != "" {
		candidates = append(candidates, matchContext.candidate(ctx, profile.TargetProperty, targetRef))
	}
	if len(resolvedReferenceCandidates(candidates)) == 0 && profile.UseDefaultFallback && def != nil && def.DefaultReference != nil {
		ref := normalizeReferenceInput(def.DefaultReference.Reference, profile.Normalization)
		candidates = append(candidates, matchContext.candidate(ctx, "__default__", ref))
	}
	return candidates
}

func (c referenceMatchContext) candidate(ctx context.Context, sourceField string, ref string) RepairReferenceCandidate {
	candidate := RepairReferenceCandidate{SourceField: sourceField, Reference: ref, NodeID: uuid.Nil, NodeName: "", Rank: 0, Status: "blocked", Error: ""}
	view, err := c.resolveReference(ctx, ref)
	if err != nil {
		candidate.Error = err.Error()
		return candidate
	}
	candidate.NodeID = view.ID
	candidate.NodeName = view.Name
	candidate.Rank = int64JSONProp(view.Props, c.rankProperty)
	candidate.Status = "resolved"
	return candidate
}

func (c referenceMatchContext) resolveReference(ctx context.Context, ref string) (*node.NodeView, error) {
	trimmedRef := strings.TrimSpace(ref)
	if trimmedRef == "" {
		return nil, loggedRepairError(ctx, "reference is empty", domain.ErrInvalidArgument)
	}
	if parsedID, err := uuid.Parse(trimmedRef); err == nil {
		return c.resolveUUID(ctx, parsedID)
	}
	localRef := trimmedRef
	if scopeRef, scopedRef, ok := strings.Cut(trimmedRef, "::"); ok {
		if err := c.matchScopeReference(ctx, strings.TrimSpace(scopeRef)); err != nil {
			return nil, err
		}
		localRef = strings.TrimSpace(scopedRef)
	}
	return c.resolveHumanReference(ctx, localRef)
}

func (c referenceMatchContext) resolveUUID(ctx context.Context, id uuid.UUID) (*node.NodeView, error) {
	view, err := c.reader.Get(ctx, id)
	if err != nil {
		return nil, loggedRepairError(ctx, fmt.Sprintf("get reference node %s", id), err)
	}
	if view == nil || view.OrgID != c.orgID || view.NodeType != nodeTypeKey(c.targetType) {
		return nil, loggedRepairError(ctx, fmt.Sprintf("reference node %s", id), domain.ErrNotFound)
	}
	if c.scopeID != uuid.Nil && scopedReferenceStrategy(c.targetType.Reference.Strategy) && rawUUIDProp(view.Props, "parent_id") != c.scopeID {
		return nil, loggedRepairError(ctx, fmt.Sprintf("reference node %s is outside scope %s", id, c.scopeID), domain.ErrInvalidArgument)
	}
	return view, nil
}

func (c referenceMatchContext) matchScopeReference(ctx context.Context, scopeRef string) error {
	if c.scopeID == uuid.Nil {
		return loggedRepairError(ctx, fmt.Sprintf("scoped reference %q has no repair scope", scopeRef), domain.ErrInvalidArgument)
	}
	scopeView, err := c.reader.Get(ctx, c.scopeID)
	if err != nil {
		return loggedRepairError(ctx, fmt.Sprintf("get repair scope %s", c.scopeID), err)
	}
	if scopeView == nil {
		return loggedRepairError(ctx, fmt.Sprintf("repair scope %s", c.scopeID), domain.ErrNotFound)
	}
	scopeType := c.typeIndex[scopeView.NodeType]
	if scopeType == nil {
		return loggedRepairError(ctx, fmt.Sprintf("repair scope type %q", scopeView.NodeType), domain.ErrNotFound)
	}
	if !viewMatchesReference(scopeView, scopeType, scopeRef) {
		return loggedRepairError(ctx, fmt.Sprintf("scope reference %q does not match scope %s", scopeRef, c.scopeID), domain.ErrInvalidArgument)
	}
	return nil
}

func (c referenceMatchContext) resolveHumanReference(ctx context.Context, ref string) (*node.NodeView, error) {
	strategy := c.targetType.Reference.Strategy
	if strategy == node.ReferenceUUIDOnly {
		return nil, loggedRepairError(ctx, fmt.Sprintf("target type %q only accepts UUID references", nodeTypeKey(c.targetType)), domain.ErrInvalidArgument)
	}
	views, err := c.candidateViews(ctx, ref)
	if err != nil {
		return nil, err
	}
	matched := make([]*node.NodeView, 0, len(views))
	for _, view := range views {
		if viewMatchesProfileReference(view, c.targetType, c.matchProperty, ref) {
			matched = append(matched, view)
		}
	}
	sort.Slice(matched, func(i int, j int) bool { return matched[i].ID.String() < matched[j].ID.String() })
	if len(matched) == 0 {
		return nil, loggedRepairError(ctx, fmt.Sprintf("reference %q matched no %s nodes", ref, nodeTypeKey(c.targetType)), domain.ErrNotFound)
	}
	if len(matched) > 1 {
		return nil, loggedRepairError(ctx, fmt.Sprintf("reference %q matched multiple %s nodes", ref, nodeTypeKey(c.targetType)), domain.ErrInvalidArgument)
	}
	return matched[0], nil
}

func (c referenceMatchContext) candidateViews(ctx context.Context, ref string) ([]*node.NodeView, error) {
	query := node.NodeListQuery{OrgID: c.orgID, NodeType: nodeTypeKey(c.targetType), ByProperty: nil, BySourceRelation: nil, ByTargetRelation: nil, CreatedAfter: nil, CreatedBefore: nil, PropFilters: nil, Limit: 0, Cursor: ""}
	switch c.targetType.Reference.Strategy {
	case node.ReferenceUUIDOnly:
		return nil, loggedRepairError(ctx, fmt.Sprintf("target type %q only accepts UUID references", nodeTypeKey(c.targetType)), domain.ErrInvalidArgument)
	case node.ReferenceDirectSlug:
		propName := strings.TrimSpace(c.targetType.Reference.Property)
		if propName == "" {
			propName = "slug"
		}
		if propName != "name" {
			query.ByProperty = &node.PropertyMatch{PropName: propName, Value: service.MustRawString(ref)}
		}
	case node.ReferenceScopedProperty, node.ReferenceScopedSequence:
		if c.scopeID == uuid.Nil {
			return nil, loggedRepairError(ctx, fmt.Sprintf("target type %q requires repair scope", nodeTypeKey(c.targetType)), domain.ErrInvalidArgument)
		}
		query.ByProperty = &node.PropertyMatch{PropName: "parent_id", Value: service.MustRawString(c.scopeID.String())}
	default:
		return nil, loggedRepairError(ctx, fmt.Sprintf("target type %q reference strategy %q", nodeTypeKey(c.targetType), c.targetType.Reference.Strategy), domain.ErrInvalidArgument)
	}
	views, err := c.reader.List(ctx, query)
	if err != nil {
		return nil, loggedRepairError(ctx, fmt.Sprintf("list reference candidates for %q", ref), err)
	}
	return views, nil
}

func viewMatchesProfileReference(view *node.NodeView, targetType *node.NodeType, matchProperty string, input string) bool {
	if strings.TrimSpace(matchProperty) != "" {
		return strings.EqualFold(referencePropertyValue(view, strings.TrimSpace(matchProperty)), input)
	}
	return viewMatchesReference(view, targetType, input)
}

func viewMatchesReference(view *node.NodeView, targetType *node.NodeType, input string) bool {
	if view == nil || targetType == nil {
		return false
	}
	propName := strings.TrimSpace(targetType.Reference.Property)
	switch targetType.Reference.Strategy {
	case node.ReferenceUUIDOnly:
		return false
	case node.ReferenceDirectSlug:
		if propName == "" {
			propName = "slug"
		}
		return strings.EqualFold(referencePropertyValue(view, propName), input)
	case node.ReferenceScopedProperty, node.ReferenceScopedSequence:
		return strings.EqualFold(referencePropertyValue(view, propName), input)
	}
	return false
}
