package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
)

func init() {
	Register(Operation{
		Name:        "backfill.default_children",
		Lifetime:    clispec.Permanent,
		Audit:       audit.Spec{Verb: string(audit.VerbOpsBackfillDefaultChildren), Mutates: true},
		Description: "For every NodeType with DefaultChildren set, walk every existing node of that type and create any missing default child by name. Idempotent.",
		Run:         runBackfillDefaultChildren,
		DryRun:      dryRunBackfillDefaultChildren,
	})
}

// runBackfillDefaultChildren calls checkedDefaultChildTypes and returns its
// error before any write. It calls backfillOrgDefaultChildren to create every
// missing default child in every organization. After processing every
// organization, it returns one error that joins all failed parent listings,
// child listings, and child creations. It returns nil when all operations
// succeed.
func runBackfillDefaultChildren(ctx context.Context, env *Env) error {
	orgTypes, err := checkedDefaultChildTypes(ctx, env)
	if err != nil {
		return err
	}

	var failures []error

	for orgID, types := range orgTypes {
		failures = append(failures, backfillOrgDefaultChildren(ctx, env, orgID, types)...)
	}

	if len(failures) == 0 {
		return nil
	}

	failed := fmt.Errorf(
		"default children backfill failed %d operations: %w",
		len(failures),
		errors.Join(failures...),
	)
	env.Log.ErrorContext(
		ctx,
		"backfill.default_children_failed",
		slog.Int("failures", len(failures)),
		slog.String("err", failed.Error()),
	)

	return failed
}

// dryRunBackfillDefaultChildren stops after checkedDefaultChildTypes validates.
func dryRunBackfillDefaultChildren(ctx context.Context, env *Env) error {
	_, err := checkedDefaultChildTypes(ctx, env)
	return err
}

// checkedDefaultChildTypes reads organization IDs with listOrgIDs and returns
// its error immediately. It reads every organization's node types with
// listOrgNodeTypes and joins all failed reads before validation or writes.
// It calls checkDefaultChildProps to validate every default child's complete
// property map in every organization. Any undeclared property stops the run
// with one error before any write. On success, it returns the node types by
// organization ID.
func checkedDefaultChildTypes(
	ctx context.Context,
	env *Env,
) (map[uuid.UUID][]*node.NodeType, error) {
	orgIDs, err := listOrgIDs(ctx, env)
	if err != nil {
		return nil, err
	}

	orgTypes, err := listOrgNodeTypes(ctx, env, orgIDs)
	if err != nil {
		return nil, err
	}

	if err := checkDefaultChildProps(ctx, env, orgTypes); err != nil {
		return nil, err
	}

	return orgTypes, nil
}

// backfillOrgDefaultChildren creates missing default children for every node
// of the given node types in orgID.
//
// backfillOrgDefaultChildren continues with the remaining nodes after each
// failure and returns a slice with one error per failed listing or creation.
func backfillOrgDefaultChildren(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	types []*node.NodeType,
) []error {
	typeIndex := node.BuildTypeIndex(types)
	var failures []error

	for _, nt := range types {
		if len(nt.DefaultChildren) == 0 {
			continue
		}

		parents, err := env.Stores.Views.List(
			ctx,
			node.NodeListQuery{
				OrgID:    orgID,
				NodeType: nt.TypeKey,
			},
		)
		if err != nil {
			failures = append(
				failures,
				fmt.Errorf("list nodes of type %q in organization %s: %w", nt.TypeKey, orgID, err),
			)
			continue
		}

		created := 0
		skipped := 0

		for _, parent := range parents {
			parentIDRaw, _ := json.Marshal(parent.ID.String())

			for _, dc := range nt.DefaultChildren {
				childType := typeIndex[dc.TypeKey]
				if childType == nil {
					env.Log.WarnContext(
						ctx,
						"backfill: unknown child type",
						slog.String("parent_type", nt.TypeKey),
						slog.String("child_type", dc.TypeKey),
					)
					continue
				}

				existing, err := env.Stores.Nodes.ListByProperty(
					ctx,
					orgID,
					dc.TypeKey,
					"parent_id",
					parentIDRaw,
				)
				if err != nil {
					failures = append(
						failures,
						fmt.Errorf("list %s children of node %s: %w", dc.TypeKey, parent.ID, err),
					)
					continue
				}

				if hasNamed(existing, dc.Name) {
					skipped++
					continue
				}

				child, view, rels := defaultChildRecords(orgID, parent.ID, dc)
				// CreateAtomic indexes parent_id here. This backfill does not assign slug,
				// identifier, or sequence values for resolution; the default child records use
				// the declared properties and the generated parent_id and scope_id values.
				if err := env.Stores.Nodes.CreateAtomic(
					ctx,
					child,
					view,
					rels,
					[]string{"parent_id"},
					nil,
					nil,
				); err != nil {
					failures = append(
						failures,
						fmt.Errorf(
							"create default child %q under node %s: %w",
							dc.Name,
							parent.ID,
							err,
						),
					)
					continue
				}

				created++
			}
		}

		logDefaultChildCounts(ctx, env, orgID, nt.TypeKey, len(parents), created, skipped)
	}

	return failures
}

// hasNamed reports whether a candidate's Name exactly matches name. The seed
// supplies the names to store, and this comparison does not normalize them.
func hasNamed(candidates []*node.Node, name string) bool {
	for _, n := range candidates {
		if n.Name == name {
			return true
		}
	}

	return false
}

// defaultChildRecords builds a node, view, and child_of relationship under
// parentID with a new UUIDv7 ID and the current time. defaultChildProps adds
// parent_id and scope_id. This helper does not write the records.
//
// The ops package builds these records locally. The property check imports the
// service package for validation.
func defaultChildRecords(
	orgID uuid.UUID,
	parentID uuid.UUID,
	dc node.DefaultChild,
) (*node.Node, *node.NodeView, []*node.Relationship) {
	id := uuid.Must(uuid.NewV7())
	now := clock.Now().UTC()

	parentRaw, _ := json.Marshal(parentID.String())
	props := defaultChildProps(dc, parentRaw)

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
	rels := []*node.Relationship{
		{
			OrgID:        orgID,
			SourceID:     id,
			RelationType: node.RelChildOf,
			TargetID:     parentID,
			CreatedAt:    now,
		},
	}

	return n, view, rels
}
