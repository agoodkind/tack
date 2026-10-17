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

func runBackfillDefaultChildren(ctx context.Context, env *Env) error {
	return RunDefaultChildrenBackfill(ctx, env, false)
}

func dryRunBackfillDefaultChildren(ctx context.Context, env *Env) error {
	return RunDefaultChildrenBackfill(ctx, env, true)
}

// RunDefaultChildrenBackfill reads the organization IDs and returns the error
// if that read fails.
//
// It reads every organization's node types through listOrgNodeTypes. If any
// node type read fails, it returns one error that joins every failed read
// before validation or writes.
//
// It validates the complete property map of every default child in every
// organization. If any default child sets an undeclared property, it refuses
// the entire run with one error before any write.
//
// If dryRun is true, it stops after validation.
//
// Otherwise, it creates every missing default child in every organization.
// After processing every organization, it returns one error that joins every
// failed parent listing, child listing, or child creation. It returns nil if
// every operation succeeds.
func RunDefaultChildrenBackfill(ctx context.Context, env *Env, dryRun bool) error {
	orgIDs, err := listOrgIDs(ctx, env)
	if err != nil {
		return err
	}
	orgTypes, err := listOrgNodeTypes(ctx, env, orgIDs)
	if err != nil {
		return err
	}
	if err := checkDefaultChildProps(ctx, env, orgTypes); err != nil {
		return err
	}
	if dryRun {
		return nil
	}
	var failures []error
	for orgID, types := range orgTypes {
		failures = append(failures, backfillOrgDefaultChildren(ctx, env, orgID, types)...)
	}
	if len(failures) == 0 {
		return nil
	}
	failed := fmt.Errorf("default children backfill failed %d operations: %w", len(failures), errors.Join(failures...))
	env.Log.ErrorContext(ctx, "backfill.default_children_failed",
		slog.Int("failures", len(failures)), slog.String("err", failed.Error()))
	return failed
}

// backfillOrgDefaultChildren creates missing default children for every node
// of the given node types in orgID.
//
// backfillOrgDefaultChildren logs each failed parent listing and each failed
// child listing. createDefaultChild logs each failed child creation.
//
// backfillOrgDefaultChildren continues with the remaining nodes after each
// failure and returns a slice with one error per failed listing or creation.
func backfillOrgDefaultChildren(ctx context.Context, env *Env, orgID uuid.UUID, types []*node.NodeType) []error {
	typeIndex := node.BuildTypeIndex(types)
	var failures []error

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
			failures = append(failures, fmt.Errorf("list nodes of type %q in organization %s: %w", nt.TypeKey, orgID, err))
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
					failures = append(failures, fmt.Errorf("list %s children of node %s: %w", dc.TypeKey, parent.ID, err))
					continue
				}
				if hasNamed(existing, dc.Name) {
					skipped++
					continue
				}
				if err := createDefaultChild(ctx, env, orgID, parent.ID, dc); err != nil {
					failures = append(failures, fmt.Errorf("create default child %q under node %s: %w", dc.Name, parent.ID, err))
					continue
				}
				created++
			}
		}

		env.Log.DebugContext(ctx, "backfill.type",
			slog.String("org_id", orgID.String()),
			slog.String("type", nt.TypeKey),
			slog.Int("parents", len(parents)),
			slog.Int("children_created", created),
			slog.Int("children_skipped_existing", skipped))
	}
	return failures
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
	err := env.Stores.Nodes.CreateAtomic(ctx, n, view, rels, []string{"parent_id"}, nil, nil)
	if err != nil {
		env.Log.ErrorContext(ctx, "backfill.child_create_failed", slog.String("err", err.Error()))
		return fmt.Errorf("create default child %s: %w", dc.TypeKey, err)
	}
	return nil
}
