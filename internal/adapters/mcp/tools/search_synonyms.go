package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/domain/node"
)

// maxQueryVariants bounds the federated search fan-out, so a large synonym
// set cannot multiply query cost.
const maxQueryVariants = 8

// expandQuery returns the query plus one variant per synonym substitution of
// a whole query word, up to maxQueryVariants. Matching ignores case, and a
// variant that only differs by case from one already listed is dropped.
func expandQuery(query string, synonymSets [][]string) []string {
	variants := []string{query}
	seen := map[string]bool{strings.ToLower(query): true}
	words := strings.Fields(query)
	for wordIndex, word := range words {
		for _, terms := range synonymSets {
			if !containsFold(terms, word) {
				continue
			}
			for _, term := range terms {
				replaced := append([]string(nil), words...)
				replaced[wordIndex] = term
				variant := strings.Join(replaced, " ")
				key := strings.ToLower(variant)
				if seen[key] {
					continue
				}
				seen[key] = true
				variants = append(variants, variant)
				if len(variants) == maxQueryVariants {
					return variants
				}
			}
		}
	}
	return variants
}

func containsFold(terms []string, word string) bool {
	for _, term := range terms {
		if strings.EqualFold(term, word) {
			return true
		}
	}
	return false
}

// loadSynonymSets reads every synonym set node under the workspace. A synonym
// set is any node whose type declares the has_synonyms feature, and its terms
// are the comma-separated words in the property that applies to that feature.
// Multi-word terms are left out, because expandQuery substitutes single words.
func loadSynonymSets(ctx context.Context, resolver *Resolver, propertyDefs node.PropertyDefRepository, workspace *node.NodeView) ([][]string, error) {
	termsProperty, err := synonymTermsProperty(ctx, propertyDefs, workspace.OrgID)
	if err != nil {
		return nil, err
	}
	parentIDRaw, err := json.Marshal(workspace.ID.String())
	if err != nil {
		slog.ErrorContext(ctx, "search.synonym_parent_encode_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("encode workspace id %s: %w", workspace.ID, err)
	}
	var sets [][]string
	for _, nodeType := range resolver.typeIndex {
		if nodeType.OrgID != workspace.OrgID || !nodeType.Features.Has(node.FeatureHasSynonyms) {
			continue
		}
		page, err := resolver.reader.ListPage(ctx, node.NodeListQuery{
			OrgID:            workspace.OrgID,
			NodeType:         nodeType.TypeKey,
			ByProperty:       &node.PropertyMatch{PropName: "parent_id", Value: parentIDRaw},
			BySourceRelation: nil,
			ByTargetRelation: nil,
			CreatedAfter:     nil,
			CreatedBefore:    nil,
			PropFilters:      nil,
			Limit:            maxListLimit,
			Cursor:           "",
		})
		if err != nil {
			slog.ErrorContext(ctx, "search.synonym_sets_failed",
				slog.String("node_type", nodeType.TypeKey), slog.String("err", err.Error()))
			return nil, fmt.Errorf("list synonym sets of type %s: %w", nodeType.TypeKey, err)
		}
		for _, view := range page.Views {
			if terms := synonymTerms(stringProp(view, termsProperty)); len(terms) > 1 {
				sets = append(sets, terms)
			}
		}
	}
	return sets, nil
}

// synonymTermsProperty returns the name of the property whose definition
// applies to the has_synonyms feature, or "" when the org defines none.
func synonymTermsProperty(ctx context.Context, propertyDefs node.PropertyDefRepository, orgID uuid.UUID) (string, error) {
	defs, err := propertyDefs.List(ctx, orgID)
	if err != nil {
		slog.ErrorContext(ctx, "search.property_defs_failed",
			slog.String("org_id", orgID.String()), slog.String("err", err.Error()))
		return "", fmt.Errorf("list property definitions for org %s: %w", orgID, err)
	}
	for _, def := range defs {
		if slices.Contains(def.AppliesToFeatures, node.FeatureHasSynonyms) {
			return def.Name, nil
		}
	}
	return "", nil
}

// synonymTerms splits a comma-separated value into single-word terms.
func synonymTerms(value string) []string {
	terms := make([]string, 0)
	for part := range strings.SplitSeq(value, ",") {
		term := strings.TrimSpace(part)
		if term == "" || strings.ContainsAny(term, " \t") {
			continue
		}
		terms = append(terms, term)
	}
	return terms
}
