package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// renameResolutions is what the evidence file resolves to against live state:
// the node record for every renamed node that still exists, the org they all
// belong to, and the ids of any that no longer resolve.
type renameResolutions struct {
	ByNode       map[uuid.UUID]*node.NodeResolve
	OrgID        uuid.UUID
	Unresolvable []uuid.UUID
}

// resolveRenameEvidence resolves every node the rename evidence names, in one
// pass, before any event is built.
//
// A node the repair renamed can be deleted afterwards, and it was: deleting one
// of the 104 used to abort the whole reconstruction on a resolve error, so the
// operator got no counts at all rather than a count with a gap named in it
// (TACK-466). A deletion does not unmake the rename, so the reconstruction
// still owes that history; what it loses is the node's type, which live state
// was the only source of.
//
// The org comes from the nodes that do resolve. They must agree, which the
// caller already required, and at least one must resolve, because nothing else
// names the org the reconstructed events belong to.
func resolveRenameEvidence(
	ctx context.Context,
	reader referenceRenameResolver,
	renames []referenceRenameEvidence,
) (renameResolutions, error) {
	out := renameResolutions{ByNode: map[uuid.UUID]*node.NodeResolve{}, OrgID: uuid.Nil, Unresolvable: nil}
	for _, rename := range renames {
		nodeID, err := uuid.Parse(rename.NodeID)
		if err != nil {
			wrapped := fmt.Errorf("parse evidence node %q: %w", rename.NodeID, err)
			telemetry.L(ctx).Error("audit.reference_rename_backfill.node_parse_failed", slog.String("err", wrapped.Error()))
			return renameResolutions{ByNode: nil, OrgID: uuid.Nil, Unresolvable: nil}, wrapped
		}
		resolution, err := reader.Resolve(ctx, nodeID)
		if errors.Is(err, domain.ErrNotFound) || (err == nil && resolution == nil) {
			// The node is gone. Counted, named, and carried into the event so
			// the operator sees the gap instead of an aborted command.
			out.Unresolvable = append(out.Unresolvable, nodeID)
			continue
		}
		if err != nil {
			// A read that failed is not an absence. Reconstructing over it
			// would silently drop history that is still there.
			wrapped := fmt.Errorf("resolve evidence node %s: %w", nodeID, err)
			telemetry.L(ctx).Error("audit.reference_rename_backfill.node_resolve_failed", slog.String("err", wrapped.Error()))
			return renameResolutions{ByNode: nil, OrgID: uuid.Nil, Unresolvable: nil}, wrapped
		}
		if out.OrgID != uuid.Nil && out.OrgID != resolution.OrgID {
			err := fmt.Errorf("reference rename evidence spans orgs %s and %s", out.OrgID, resolution.OrgID)
			telemetry.L(ctx).Error("audit.reference_repair_backfill.org_mismatch", slog.String("err", err.Error()))
			return renameResolutions{ByNode: nil, OrgID: uuid.Nil, Unresolvable: nil}, err
		}
		out.OrgID = resolution.OrgID
		out.ByNode[nodeID] = resolution
	}
	if out.OrgID == uuid.Nil {
		err := fmt.Errorf(
			"no renamed node resolves, so the org the reconstruction belongs to cannot be read: %d evidence node(s) are absent",
			len(out.Unresolvable))
		telemetry.L(ctx).Error("audit.reference_rename_backfill.all_nodes_absent", slog.String("err", err.Error()))
		return renameResolutions{ByNode: nil, OrgID: uuid.Nil, Unresolvable: nil}, err
	}
	sort.Slice(out.Unresolvable, func(i, j int) bool {
		return out.Unresolvable[i].String() < out.Unresolvable[j].String()
	})
	if len(out.Unresolvable) > 0 {
		telemetry.L(ctx).Warn("audit.reference_rename_backfill.nodes_absent",
			slog.Int("count", len(out.Unresolvable)),
			slog.String("first", out.Unresolvable[0].String()))
	}
	return out, nil
}
