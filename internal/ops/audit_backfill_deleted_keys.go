package ops

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// deletedSubjectQueryLimit bounds the delete-row read. The window is short and
// deletions are rare, so a result that fills the limit means the read cannot
// prove it saw every deletion, and the run refuses rather than reconstruct a
// partial count.
const deletedSubjectQueryLimit = 5000

// deletedSubjectKeyPrefix namespaces the idempotency keys these events carry,
// so a rerun rebuilds the same event ids and writes nothing new.
const deletedSubjectKeyPrefix = "tack-429-reference-key-deleted:"

// auditRowQuerier reads ledger rows. audit.Reader satisfies it; a fake
// satisfies it in tests, which is why the derivation takes the interface
// rather than the concrete reader.
type auditRowQuerier interface {
	Query(ctx context.Context, filter audit.QueryFilter) ([]audit.Row, error)
}

// deletedReferenceSubject is one post-repair deletion of a node whose type
// carries reference templates, read from the ledger's own delete row.
type deletedReferenceSubject struct {
	NodeType *node.NodeType
	NodeID   uuid.UUID
	DeleteID uuid.UUID
	DeleteAt time.Time
}

// deletedSubjectKeyEvents reconstructs the reference keys the repair wrote for
// nodes that were deleted after it ran.
//
// The repair recorded how many keys it wrote. Deriving that count from the
// nodes that exist today misses every node deleted since, so the count check
// refuses and the backfill is blocked. The deletions are themselves in the
// ledger, so they are counted from there: one event per template of the
// deleted node's type, which is exactly what the repair wrote for it.
//
// The count check stays exact rather than becoming a floor. A node created
// after the repair and then deleted would push the derived count past the
// recorded one and refuse, which is the right answer, because the repair never
// wrote a key for a node that did not exist when it ran.
func deletedSubjectKeyEvents(
	ctx context.Context,
	querier auditRowQuerier,
	nodeTypes []*node.NodeType,
	principal audit.OperatorPrincipal,
	orgID uuid.UUID,
	occurredAt time.Time,
	repairStart time.Time,
) ([]audit.Event, error) {
	subjects, err := deletedReferenceSubjects(ctx, querier, nodeTypes, orgID, occurredAt, repairStart)
	if err != nil {
		return nil, err
	}
	events := make([]audit.Event, 0, len(subjects))
	for _, subject := range subjects {
		for _, template := range subject.NodeType.ReferenceTemplates {
			event, eventErr := deletedSubjectKeyEvent(ctx, principal, orgID, occurredAt, subject, template.Name)
			if eventErr != nil {
				return nil, eventErr
			}
			events = append(events, event)
		}
	}
	return events, nil
}

// deletedReferenceSubjects reads the post-repair deletions that removed a
// reference-bearing node, in a stable order.
func deletedReferenceSubjects(
	ctx context.Context,
	querier auditRowQuerier,
	nodeTypes []*node.NodeType,
	orgID uuid.UUID,
	occurredAt time.Time,
	repairStart time.Time,
) ([]deletedReferenceSubject, error) {
	typeByDeleteTool := referenceBearingDeleteTools(nodeTypes)
	if len(typeByDeleteTool) == 0 {
		return nil, nil
	}
	rows, err := querier.Query(ctx, audit.QueryFilter{
		OrgID: orgID, Oldest: repairStart, Latest: occurredAt.Add(time.Second),
		Action: string(audit.VerbNodeDelete), ActorID: uuid.Nil, EntityID: uuid.Nil,
		RequestID: "", TraceID: "", Limit: deletedSubjectQueryLimit,
	})
	if err != nil {
		wrapped := fmt.Errorf("read post-repair node deletions for org %s: %w", orgID, err)
		telemetry.L(ctx).Error("audit.reference_repair_backfill.delete_query_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	if len(rows) >= deletedSubjectQueryLimit {
		err := fmt.Errorf(
			"post-repair node deletions filled the %d row read limit, so the deleted-subject count cannot be proven complete",
			deletedSubjectQueryLimit)
		telemetry.L(ctx).Error("audit.reference_repair_backfill.delete_query_truncated", slog.String("err", err.Error()))
		return nil, err
	}
	subjects := make([]deletedReferenceSubject, 0, len(rows))
	for _, row := range rows {
		nodeType, ok := typeByDeleteTool[row.Context.Tool]
		if !ok {
			continue
		}
		// A delete that failed removed nothing, so its node is still present
		// and already counted by the live derivation. An outcome the ledger
		// never recorded is not evidence of failure, and every production row
		// in this window predates the outcome column, so only an observed
		// error excludes the row.
		if row.Outcome == audit.OutcomeError {
			continue
		}
		subjects = append(subjects, deletedReferenceSubject{
			NodeType: nodeType, NodeID: row.EntityID, DeleteID: row.EventID, DeleteAt: row.EventTime.UTC(),
		})
	}
	sort.Slice(subjects, func(i, j int) bool {
		return subjects[i].DeleteID.String() < subjects[j].DeleteID.String()
	})
	return subjects, nil
}

// referenceBearingDeleteTools maps the MCP delete tool of every type that
// renders reference keys to that type. A type with no templates had no key for
// the repair to write, so deleting one of its nodes changes no count.
func referenceBearingDeleteTools(nodeTypes []*node.NodeType) map[string]*node.NodeType {
	tools := make(map[string]*node.NodeType, len(nodeTypes))
	for _, nodeType := range nodeTypes {
		if len(nodeType.ReferenceTemplates) == 0 || nodeType.Slug == "" {
			continue
		}
		tools["tack_delete_"+nodeType.Slug] = nodeType
	}
	return tools
}

func deletedSubjectKeyEvent(
	ctx context.Context,
	principal audit.OperatorPrincipal,
	orgID uuid.UUID,
	occurredAt time.Time,
	subject deletedReferenceSubject,
	templateName string,
) (audit.Event, error) {
	extra := reconstructionExtra{
		Class: "reference keys", Reconstruction: true, HistoricalTime: referenceRepairDate,
		Evidence: referenceRepairEvidence, Scope: "", Template: templateName, Run: "",
		SeedEmail: "", OrgSlug: "", WorkspaceSlug: "", HistoricalReferenceKey: "",
		// The node is gone, so its key text cannot be read back from live
		// state the way a present node's can. The deletion proves the repair
		// wrote a key here; nothing proves which key.
		HistoricalReferenceKeyTextUnproven: true, ObservedReferenceKey: "",
		ObservedReferenceTemplate: "", HistoricalDefinitionSetUnproven: false,
		ObservedSeedDefinition:    nil,
		SubjectDeletedAt:          subject.DeleteAt.Format(time.RFC3339Nano),
		SubjectDeletionEventID:    subject.DeleteID.String(),
		SubjectIdentityUnrecorded: subject.NodeID == uuid.Nil,
	}
	return reconstructionEvent(
		ctx,
		audit.VerbOpsRepairReferenceUniqueness,
		principal,
		audit.Entity{
			Type: "node", NodeType: subject.NodeType.TypeKey, ID: subject.NodeID, Identifier: "", Name: "",
		},
		orgID,
		deletedSubjectKeyPrefix+subject.DeleteID.String()+":"+templateName,
		extra,
		occurredAt,
	)
}

// newAuditRowQuerier opens the ledger reader the deleted-subject derivation
// reads through. The derivation cannot run without it: guessing that no node
// was deleted would silently lower the reconstructed count.
func newAuditRowQuerier(ctx context.Context, env *Env) (*audit.Reader, error) {
	if env == nil || env.Cfg == nil || strings.TrimSpace(env.Cfg.AuditReaderDSN) == "" {
		err := fmt.Errorf("reference repair reconstruction requires AUDIT_READER_DSN to read post-repair deletions")
		telemetry.L(ctx).Error("audit.reference_repair_backfill.reader_dsn_missing", slog.String("err", err.Error()))
		return nil, err
	}
	reader, err := audit.NewReader(ctx, env.Cfg.AuditReaderDSN)
	if err != nil {
		wrapped := fmt.Errorf("open audit reader for reference repair reconstruction: %w", err)
		telemetry.L(ctx).Error("audit.reference_repair_backfill.reader_open_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	return reader, nil
}
