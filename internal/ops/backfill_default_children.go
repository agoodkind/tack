package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
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
