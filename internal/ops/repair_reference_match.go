package ops

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
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

func collectReferenceCandidates(
	ctx context.Context,
	view *node.NodeView,
	profile RepairReferenceProfile,
	def *node.PropertyDef,
	matchContext referenceMatchContext,
	observed *[]RepairObservedSource,
) []RepairReferenceCandidate {
	candidates := make([]RepairReferenceCandidate, 0, len(profile.SourceFields)+2)
	for _, field := range profile.SourceFields {
		source := observedReferenceSource(view, field, profile.Normalization)
		*observed = append(*observed, source)
		if !source.Present || source.Normalized == "" {
			continue
		}

		candidates = append(candidates, matchContext.candidate(ctx, field, source.Normalized))
	}

	if targetRef := stringProp(view.Props, profile.TargetProperty); strings.TrimSpace(
		targetRef,
	) != "" {
		candidates = append(
			candidates,
			matchContext.candidate(ctx, profile.TargetProperty, targetRef),
		)
	}

	if len(resolvedReferenceCandidates(candidates)) == 0 && profile.UseDefaultFallback &&
		def != nil &&
		def.DefaultReference != nil {
		ref := normalizeReferenceInput(def.DefaultReference.Reference, profile.Normalization)
		candidates = append(candidates, matchContext.candidate(ctx, "__default__", ref))
	}

	return candidates
}

func (c referenceMatchContext) candidate(
	ctx context.Context,
	sourceField string,
	ref string,
) RepairReferenceCandidate {
	candidate := RepairReferenceCandidate{
		SourceField: sourceField,
		Reference:   ref,
		NodeID:      uuid.Nil,
		NodeName:    "",
		Rank:        0,
		Status:      "blocked",
		Error:       "",
	}
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

func (c referenceMatchContext) resolveReference(
	ctx context.Context,
	ref string,
) (*node.NodeView, error) {
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

func viewMatchesProfileReference(
	view *node.NodeView,
	targetType *node.NodeType,
	matchProperty string,
	input string,
) bool {
	if strings.TrimSpace(matchProperty) != "" {
		return strings.EqualFold(
			referencePropertyValue(view, strings.TrimSpace(matchProperty)),
			input,
		)
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
	case node.ReferenceDirectProperty:
		if propName == "" {
			propName = "slug"
		}

		return strings.EqualFold(referencePropertyValue(view, propName), input)
	case node.ReferenceScopedProperty, node.ReferenceScopedSequence:
		return strings.EqualFold(referencePropertyValue(view, propName), input)
	}

	return false
}
