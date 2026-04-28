package ops

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func init() {
	Register(Operation{
		Name:        "backfill.default_children",
		Description: "For every NodeType with DefaultChildren set, walk every existing node of that type and create any missing default child by name. Idempotent.",
		Run:         runBackfillDefaultChildren,
	})
}

func runBackfillDefaultChildren(ctx context.Context, env *Env) error {
	orgIDs, err := listOrgIDs(ctx, env)
	if err != nil {
		return err
	}

	for orgID := range orgIDs {
		types, err := env.Stores.NodeTypes.List(ctx, orgID)
		if err != nil {
			env.Log.ErrorContext(ctx, "backfill: list node types",
				slog.String("org_id", orgID.String()),
				slog.String("err", err.Error()))
			continue
		}
		typeIndex := node.BuildTypeIndex(types)

		for _, nt := range types {
			if len(nt.DefaultChildren) == 0 {
				continue
			}

			parents, err := env.Stores.Views.List(ctx, node.NodeListQuery{
				OrgID:    orgID,
				NodeType: nt.TypeKey,
			})
			if err != nil {
				env.Log.ErrorContext(ctx, "backfill: list parents",
					slog.String("org_id", orgID.String()),
					slog.String("type", nt.TypeKey),
					slog.String("err", err.Error()))
				continue
			}

			created := 0
			skipped := 0
			for _, parent := range parents {
				parentIDRaw, _ := json.Marshal(parent.ID.String())
				for _, dc := range nt.DefaultChildren {
					childType := typeIndex[dc.TypeKey]
					if childType == nil {
						env.Log.WarnContext(ctx, "backfill: unknown child type",
							slog.String("parent_type", nt.TypeKey),
							slog.String("child_type", dc.TypeKey))
						continue
					}
					existing, err := env.Stores.Nodes.ListByProperty(ctx, orgID, dc.TypeKey, "parent_id", parentIDRaw)
					if err != nil {
						env.Log.WarnContext(ctx, "backfill: list children",
							slog.String("parent_id", parent.ID.String()),
							slog.String("err", err.Error()))
						continue
					}
					if hasNamed(existing, dc.Name) {
						skipped++
						continue
					}
					if err := createDefaultChild(ctx, env, orgID, parent.ID, dc); err != nil {
						env.Log.WarnContext(ctx, "backfill: create child",
							slog.String("parent_id", parent.ID.String()),
							slog.String("child_type", dc.TypeKey),
							slog.String("child_name", dc.Name),
							slog.String("err", err.Error()))
						continue
					}
					created++
				}
			}

			env.Log.InfoContext(ctx, "backfill.type",
				slog.String("org_id", orgID.String()),
				slog.String("type", nt.TypeKey),
				slog.Int("parents", len(parents)),
				slog.Int("children_created", created),
				slog.Int("children_skipped_existing", skipped))
		}
	}
	return nil
}

// hasNamed returns true when any candidate has the given Name. Names are
// compared verbatim; the seed declares names exactly as we want them stored.
func hasNamed(candidates []*node.Node, name string) bool {
	for _, n := range candidates {
		if n.Name == name {
			return true
		}
	}
	return false
}

// createDefaultChild writes one DefaultChild under parent. Mirrors the
// NodeService.Create path closely enough that ops can drive it without
// importing the service package, which would create a cycle.
func createDefaultChild(ctx context.Context, env *Env, orgID, parentID uuid.UUID, dc node.DefaultChild) error {
	id := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	props := make(map[string]json.RawMessage, len(dc.Props)+2)
	for k, v := range dc.Props {
		props[k] = v
	}
	parentRaw, _ := json.Marshal(parentID.String())
	props["parent_id"] = parentRaw
	props["scope_id"] = parentRaw

	n := &node.Node{
		ID:        id,
		OrgID:     orgID,
		NodeType:  dc.TypeKey,
		Name:      dc.Name,
		Props:     props,
		CreatedAt: now,
		UpdatedAt: now,
	}
	view := &node.NodeView{
		ID:        id,
		OrgID:     orgID,
		NodeType:  dc.TypeKey,
		Name:      dc.Name,
		Props:     props,
		CreatedAt: now,
		UpdatedAt: now,
	}
	rels := []*node.Relationship{{
		OrgID:        orgID,
		SourceID:     id,
		RelationType: node.RelChildOf,
		TargetID:     parentID,
		CreatedAt:    now,
	}}
	// parent_id is the only indexed prop guaranteed to apply to every type;
	// the resolver-critical ones (slug, identifier, sequence) are not stamped
	// here because default children do not declare them.
	return env.Stores.Nodes.CreateAtomic(ctx, n, view, rels, []string{"parent_id"})
}
