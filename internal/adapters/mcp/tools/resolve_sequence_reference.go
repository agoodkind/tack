package tools

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func (r *Resolver) parseSequenceReference(input string, typeKeys []string) (string, int, error) {
	hasTemplate := false
	for _, typeKey := range typeKeys {
		nodeType := r.typeIndex[typeKey]
		if nodeType == nil {
			continue
		}
		template := nodeType.PrimaryReferenceTemplate()
		if template == nil {
			continue
		}
		hasTemplate = true
		parsed, err := template.Parse(input)
		if err != nil {
			continue
		}
		scopeReference := parsed.ScopeRefs[node.FeatureIsScope]
		propertyName := nodeType.Reference.Property
		if propertyName == "" {
			propertyName = "sequence"
		}
		sequenceID, err := strconv.Atoi(parsed.Props[propertyName])
		if err != nil || sequenceID <= 0 || scopeReference == "" {
			continue
		}
		return scopeReference, sequenceID, nil
	}
	if hasTemplate {
		return "", 0, domain.ErrInvalidArgument
	}
	return ParseNodeIdentifier(input)
}

// referenceKeyHolder resolves input through the uniqueness index. A miss is
// uuid.Nil with a nil error, per the LookupReference contract; a non-nil error
// is a real store failure and is propagated rather than masked as a miss, so
// an outage surfaces instead of degrading into a scan or a false not-found.
func (r *Resolver) referenceKeyHolder(ctx context.Context, orgID uuid.UUID, typeKeys []string, input string) (uuid.UUID, bool, error) {
	for _, typeKey := range typeKeys {
		nodeType := r.typeIndex[typeKey]
		if nodeType == nil {
			continue
		}
		template := nodeType.PrimaryReferenceTemplate()
		if template == nil {
			continue
		}
		held, err := r.nodes.LookupReference(ctx, orgID, template.Name, input)
		if err != nil {
			wrapped := fmt.Errorf("lookup reference %q for template %q: %w", input, template.Name, err)
			slog.ErrorContext(ctx, "resolver.reference_lookup_failed", slog.String("err", wrapped.Error()))
			return uuid.Nil, false, wrapped
		}
		if held == uuid.Nil {
			continue
		}
		stampAuditOrg(ctx, orgID)
		return held, true, nil
	}
	return uuid.Nil, false, nil
}
