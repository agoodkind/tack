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
// descendants together.
type OrphanBackfillResult struct {
	Scanned int         `json:"scanned"`
	Orphans int         `json:"orphans"`
	NodeIDs []uuid.UUID `json:"node_ids"`
	Deleted int         `json:"deleted"`
}

// RunOrphanNodeBackfill scans the nodes of every organization for nodes of a
// type that lists CanLiveUnder types and has no edge to an existing node
// that NodeType metadata places above it. Before cascading delete existed, a
// deleted parent left such nodes behind. Unless dryRun is true, it deletes
// each orphan and its hierarchy descendants, and each deleted node writes one
// node.delete ledger event by the operator on ctx.
func RunOrphanNodeBackfill(ctx context.Context, scanner OrphanScanStore, deleter SubtreeDeleter, dryRun bool) (OrphanBackfillResult, error) {
	result := OrphanBackfillResult{Scanned: 0, Orphans: 0, NodeIDs: []uuid.UUID{}, Deleted: 0}
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
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	result.Orphans = len(orphans)
	for _, orphan := range orphans {
		result.NodeIDs = append(result.NodeIDs, orphan.ID)
	}
	if !dryRun {
		deleted, err := deleteOrphans(ctx, deleter, orphans)
		result.Deleted = deleted
		if err != nil {
			return result, err
		}
	}
	telemetry.L(ctx).InfoContext(ctx, "orphan_nodes.backfilled", slog.Bool("dry_run", dryRun),
		slog.Int("scanned", result.Scanned), slog.Int("orphans", result.Orphans), slog.Int("deleted", result.Deleted))
	return result, nil
}

// deleteOrphans deletes each orphan and its descendants and returns the
// count of deleted nodes.
func deleteOrphans(ctx context.Context, deleter SubtreeDeleter, orphans []fdbadapter.OrphanNode) (int, error) {
	principal, ok := audit.OperatorPrincipalFromContext(ctx)
	if !ok {
		err := errors.New("delete orphan nodes: the operator principal is missing")
		slog.ErrorContext(ctx, "orphan_nodes.delete_failed", slog.String("err", err.Error()))
		return 0, err
	}
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
