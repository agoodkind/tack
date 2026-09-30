package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
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
// nodes. template is the ledger event of the root delete.
type SubtreeDeleter interface {
	DeleteSubtree(ctx context.Context, orgID, nodeID, actorID uuid.UUID, template json.RawMessage) (service.DeleteResult, error)
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

// RunOrphanNodeBackfill scans the nodes of every organization. It finds the
// nodes of a type that lists CanLiveUnder types without a child_of edge to an
// existing node that NodeType metadata places above them; a parent deleted
// before the cascading delete existed left such nodes behind. It also finds
// the child_of edges that do not target the parent_id of a node with a
// hierarchy parent. Unless dryRun is true, it removes each such edge and
// deletes each orphan with its hierarchy descendants. Each removed edge
// writes one relationship.remove ledger event and each deleted node one
// node.delete ledger event, by the operator on ctx.
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

// applyOrphanBackfill removes the extra parent edges in result and deletes
// orphans, and records the counts in result.
func applyOrphanBackfill(
	ctx context.Context, remover ParentEdgeRemover, deleter SubtreeDeleter, orphans []fdbadapter.OrphanNode, result *OrphanBackfillResult,
) error {
	principal, ok := audit.OperatorPrincipalFromContext(ctx)
	if !ok {
		err := errors.New("apply the orphan backfill: the operator principal is missing")
		slog.ErrorContext(ctx, "orphan_nodes.apply_failed", slog.String("err", err.Error()))
		return err
	}
	removed, err := removeExtraParentEdges(ctx, remover, principal, result.Edges)
	result.RemovedEdges = removed
	if err != nil {
		return err
	}
	deleted, err := deleteOrphans(ctx, deleter, principal, orphans)
	result.Deleted = deleted
	return err
}

// deleteOrphans deletes each orphan and its descendants and returns the
// count of deleted nodes.
func deleteOrphans(ctx context.Context, deleter SubtreeDeleter, principal audit.OperatorPrincipal, orphans []fdbadapter.OrphanNode) (int, error) {
	deleted := 0
	for _, orphan := range orphans {
		template, err := orphanDeleteEvent(ctx, principal, orphan)
		if err != nil {
			return deleted, err
		}
		result, err := deleter.DeleteSubtree(ctx, orphan.OrgID, orphan.ID, principal.ID, template)
		if err != nil {
			wrapped := fmt.Errorf("delete orphan node %s: %w", orphan.ID, err)
			slog.ErrorContext(ctx, "orphan_nodes.delete_failed", slog.String("err", wrapped.Error()))
			return deleted, wrapped
		}
		deleted += result.Deleted
	}
	return deleted, nil
}

// orphanDeleteEvent builds the node.delete ledger event of one orphan, with
// the operator as its actor.
func orphanDeleteEvent(ctx context.Context, principal audit.OperatorPrincipal, orphan fdbadapter.OrphanNode) (json.RawMessage, error) {
	eventID, err := uuid.NewV7()
	if err != nil {
		wrapped := fmt.Errorf("create the delete event id of orphan node %s: %w", orphan.ID, err)
		slog.ErrorContext(ctx, "orphan_nodes.event_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	event := audit.Event{
		Verb: string(audit.VerbNodeDelete), EventID: eventID,
		Actor: audit.Actor{
			Type: principal.ActorType(), ID: principal.ID, Email: principal.Email, Name: principal.Name,
			SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: audit.Entity{Type: "node", NodeType: orphan.NodeType, ID: orphan.ID, Identifier: "", Name: ""},
		Context: audit.EventContext{
			OrgID: orphan.OrgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: telemetry.RequestID(ctx), TraceID: telemetry.TraceID(ctx),
			Source: audit.SourceSystem, Tool: orphanBackfillTool, RPC: "", Reason: "",
		},
		Delta: nil, Outcome: audit.OutcomeOK, Error: nil, IdempotencyKey: "",
		OccurredAt: clock.Now().UTC(), Extra: nil,
	}
	payload, err := audit.MarshalEvent(event)
	if err != nil {
		wrapped := fmt.Errorf("encode the delete event of orphan node %s: %w", orphan.ID, err)
		slog.ErrorContext(ctx, "orphan_nodes.event_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	return payload, nil
}
