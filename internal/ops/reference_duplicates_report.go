package ops

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
)

// FindDuplicateReferences finds reference values assigned to multiple nodes.
//
// Each group uses one organization, template name, and rendered value. Node
// types do not split a group. Results sort by organization UUID, template
// name, and rendered value.
//
// Layout:
//
//	OrgID + TemplateName + Encoded: two or more NodeIDs
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
