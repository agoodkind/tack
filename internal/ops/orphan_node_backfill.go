package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
)

// orphanBackfillTool is the tool name the ledger events of the orphan
// backfill record.
const orphanBackfillTool = "ops.backfill.once-orphan-nodes"

// OrphanScanStore reads one bounded page of the node resolution records of
// every organization and returns the nodes without a hierarchy parent.
type OrphanScanStore interface {
	ScanOrphans(ctx context.Context, cursor string) (fdbadapter.OrphanPage, error)
}

// SubtreeDeleter deletes a node and its hierarchy descendants in bounded
// steps until the delete job finishes, and reports the number of deleted
// nodes.
type SubtreeDeleter interface {
	DeleteSubtree(ctx context.Context, orgID, nodeID, actorID uuid.UUID) (service.DeleteResult, error)
}

// OrphanBackfillResult reports one orphan backfill run. Orphans counts the
// nodes without a hierarchy parent that the scan found, and NodeIDs lists
// them. Deleted counts the nodes an executed run deleted, orphans and their
// descendants together. ExtraParentEdges counts the child_of edges that do
// not target their node's parent_id, Edges lists them, and RemovedEdges
// counts the edges an executed run removed.
type OrphanBackfillResult struct {
	Scanned          int                     `json:"scanned"`
	Orphans          int                     `json:"orphans"`
	NodeIDs          []uuid.UUID             `json:"node_ids"`
	Deleted          int                     `json:"deleted"`
	ExtraParentEdges int                     `json:"extra_parent_edges"`
	Edges            []fdbadapter.ParentEdge `json:"edges"`
	RemovedEdges     int                     `json:"removed_edges"`
}

// RunOrphanNodeBackfill scans the nodes of every organization. The scan finds
// nodes of a type that lists CanLiveUnder types without a child_of edge to
// another existing node. Such an orphan exists because deletion of its parent
// occurred before the cascading delete existed. The scan also finds child_of
// edges that do not target the parent_id of a node with a hierarchy parent.
// Unless dryRun is true, the backfill removes each such edge and deletes each
// orphan with its hierarchy descendants. Each removed edge writes one
// relationship.remove ledger event, and each deleted node writes one
// node.delete ledger event, with the operator on ctx as the actor.
func RunOrphanNodeBackfill(ctx context.Context, scanner OrphanScanStore, remover ParentEdgeRemover, deleter SubtreeDeleter, dryRun bool) (OrphanBackfillResult, error) {
	result := OrphanBackfillResult{
		Scanned: 0, Orphans: 0, NodeIDs: []uuid.UUID{}, Deleted: 0,
		ExtraParentEdges: 0, Edges: []fdbadapter.ParentEdge{}, RemovedEdges: 0,
	}
	orphans := []fdbadapter.OrphanNode{}
	cursor := ""
	for {
		page, err := scanner.ScanOrphans(ctx, cursor)
		if err != nil {
			wrapped := fmt.Errorf("scan orphan nodes after cursor %q: %w", cursor, err)
			slog.ErrorContext(ctx, "orphan_nodes.scan_failed", slog.String("err", wrapped.Error()))
			return result, wrapped
		}
		result.Scanned += page.Scanned
		orphans = append(orphans, page.Orphans...)
		result.Edges = append(result.Edges, page.ExtraParentEdges...)
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	result.Orphans, result.ExtraParentEdges = len(orphans), len(result.Edges)
	for _, orphan := range orphans {
		result.NodeIDs = append(result.NodeIDs, orphan.ID)
	}
	if !dryRun {
		if err := applyOrphanBackfill(ctx, remover, deleter, orphans, &result); err != nil {
			return result, err
		}
	}
	telemetry.L(ctx).InfoContext(ctx, "orphan_nodes.backfilled", slog.Bool("dry_run", dryRun),
		slog.Int("scanned", result.Scanned), slog.Int("orphans", result.Orphans), slog.Int("deleted", result.Deleted),
		slog.Int("extra_parent_edges", result.ExtraParentEdges), slog.Int("removed_edges", result.RemovedEdges))
	return result, nil
}

// applyOrphanBackfill removes the extra parent edges in result, deletes the orphans, and
// stores the counts in result. Each change event uses the actor and operation ID from ctx
// and orphanBackfillTool as its tool name.
func applyOrphanBackfill(
	ctx context.Context, remover ParentEdgeRemover, deleter SubtreeDeleter, orphans []fdbadapter.OrphanNode, result *OrphanBackfillResult,
) error {
	principal, ok := audit.OperatorPrincipalFromContext(ctx)
	if !ok {
		err := errors.New("apply the orphan backfill: the operator principal is missing")
		slog.ErrorContext(ctx, "orphan_nodes.apply_failed", slog.String("err", err.Error()))
		return err
	}
	writeContext, err := audit.WithMutationTool(ctx, orphanBackfillTool)
	if err != nil {
		slog.ErrorContext(ctx, "orphan_nodes.apply_failed", slog.String("err", err.Error()))
		return fmt.Errorf("apply the orphan backfill: %w", err)
	}
	removed, err := removeExtraParentEdges(writeContext, remover, result.Edges)
	result.RemovedEdges = removed
	if err != nil {
		return err
	}
	deleted, err := deleteOrphans(writeContext, deleter, principal.ID, orphans)
	result.Deleted = deleted
	return err
}

// deleteOrphans deletes each orphan and its descendants and returns the
// count of deleted nodes.
func deleteOrphans(ctx context.Context, deleter SubtreeDeleter, actorID uuid.UUID, orphans []fdbadapter.OrphanNode) (int, error) {
	deleted := 0
	for _, orphan := range orphans {
		result, err := deleter.DeleteSubtree(ctx, orphan.OrgID, orphan.ID, actorID)
		if err != nil {
			wrapped := fmt.Errorf("delete orphan node %s: %w", orphan.ID, err)
			slog.ErrorContext(ctx, "orphan_nodes.delete_failed", slog.String("err", wrapped.Error()))
			return deleted, wrapped
		}
		deleted += result.Deleted
	}
	return deleted, nil
}
