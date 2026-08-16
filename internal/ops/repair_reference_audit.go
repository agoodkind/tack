package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

// The repair changes references a person may have written down, seeds the
// counters that decide future references, and claims the keys that make a
// reference resolvable. Each of those is a mutation the ledger has to name,
// and the chokepoint alone names only that the command ran. Without these
// rows a query for a repaired ticket's history returns nothing, which is the
// hole the 2026-08-07 production repair left (TACK-448).
const (
	referenceRepairRenameKeyPrefix  = "tack-448-rename:"
	referenceRepairCounterKeyPrefix = "tack-448-counter:"
	referenceRepairKeyKeyPrefix     = "tack-448-key:"
	referenceRepairCounterEntity    = "sequence_counter"
	referenceRepairNodeEntity       = "node"
)

// referenceRepairAuditNamespace derives one event identity per repaired fact.
// It is distinct from the reconstruction's namespace, so a live row and a
// reconstructed row for the same rename never collide.
var referenceRepairAuditNamespace = uuid.MustParse("2b3d9a41-6c8e-5f77-9a02-7d1c4be8f5a3")

// referenceRepairExtra is the payload each repair row carries. It has no
// reconstruction flag on purpose: these rows are contemporaneous, and a
// reader tells them from reconstructed history by that absence.
type referenceRepairExtra struct {
	Class        string `json:"class"`
	From         string `json:"from,omitempty"`
	To           string `json:"to,omitempty"`
	CounterKey   string `json:"counter_key,omitempty"`
	CounterValue int64  `json:"counter_value,omitempty"`
	TemplateName string `json:"template,omitempty"`
}

// recordReferenceRepair writes one ledger event per rename, per counter seed,
// and per reference key the run applied. Event identity is derived from the
// fact itself, so a second run over unchanged state adds nothing.
func recordReferenceRepair(
	ctx context.Context,
	outbox audit.OutboxWriter,
	principal audit.OperatorPrincipal,
	report RepairReferenceReport,
	occurredAt time.Time,
) error {
	idempotent, ok := outbox.(audit.IdempotentOutboxWriter)
	if !ok {
		err := fmt.Errorf("audit outbox does not support idempotent repair writes")
		telemetry.L(ctx).ErrorContext(ctx, "repair.reference_uniqueness.outbox_invalid",
			slog.String("err", err.Error()))
		return err
	}
	events, err := referenceRepairEvents(ctx, principal, report, occurredAt)
	if err != nil {
		return err
	}
	for _, event := range events {
		if _, writeErr := idempotent.WriteOutboxIfAbsent(ctx, event); writeErr != nil {
			wrapped := fmt.Errorf("record repaired %s: %w", event.Entity.Identifier, writeErr)
			telemetry.L(ctx).ErrorContext(ctx, "repair.reference_uniqueness.record_failed",
				slog.String("event_id", event.EventID.String()),
				slog.String("err", wrapped.Error()))
			return wrapped
		}
	}
	telemetry.L(ctx).InfoContext(ctx, "repair.reference_uniqueness.recorded",
		slog.Int("events", len(events)))
	return nil
}

// referenceRepairEvents builds every event the run owes the ledger.
func referenceRepairEvents(
	ctx context.Context,
	principal audit.OperatorPrincipal,
	report RepairReferenceReport,
	occurredAt time.Time,
) ([]audit.Event, error) {
	total := len(report.Renumbered) + len(report.Counters) + len(report.Keys)
	events := make([]audit.Event, 0, total)
	for _, rename := range report.Renumbered {
		event, err := referenceRepairRenameEvent(ctx, principal, rename, occurredAt)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	for _, counter := range report.Counters {
		event, err := referenceRepairCounterEvent(ctx, principal, counter, occurredAt)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	for _, key := range report.Keys {
		event, err := referenceRepairKeyEvent(ctx, principal, key, occurredAt)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func referenceRepairRenameEvent(
	ctx context.Context,
	principal audit.OperatorPrincipal,
	rename ReferenceRename,
	occurredAt time.Time,
) (audit.Event, error) {
	key := referenceRepairRenameKeyPrefix + rename.NodeID.String() + ":" + rename.From + ":" + rename.To
	// The verb matches the one reconstructed renames carry, so a history query
	// for a ticket answers the same way whether the rename was recorded when
	// it happened or reconstructed afterwards.
	return referenceRepairEvent(ctx, audit.VerbNodeReferenceRename, principal, rename.OrgID, key,
		audit.Entity{
			Type: referenceRepairNodeEntity, NodeType: "", ID: rename.NodeID,
			Identifier: rename.To, Name: "",
		},
		referenceRepairExtra{
			Class: "reference renames", From: rename.From, To: rename.To,
			CounterKey: "", CounterValue: 0, TemplateName: "",
		},
		occurredAt)
}

func referenceRepairCounterEvent(
	ctx context.Context,
	principal audit.OperatorPrincipal,
	counter ReferenceCounterSeed,
	occurredAt time.Time,
) (audit.Event, error) {
	key := referenceRepairCounterKeyPrefix + counter.Key + ":" + strconv.FormatInt(counter.Value, 10)
	return referenceRepairEvent(ctx, audit.VerbOpsRepairReferenceUniqueness, principal, counter.OrgID, key,
		audit.Entity{
			Type: referenceRepairCounterEntity, NodeType: "",
			ID:         uuid.NewSHA1(referenceRepairAuditNamespace, []byte(counter.Key)),
			Identifier: counter.Key, Name: "",
		},
		referenceRepairExtra{
			Class: "counter seeds", From: "", To: "",
			CounterKey: counter.Key, CounterValue: counter.Value, TemplateName: "",
		},
		occurredAt)
}

func referenceRepairKeyEvent(
	ctx context.Context,
	principal audit.OperatorPrincipal,
	key ReferenceKeyWrite,
	occurredAt time.Time,
) (audit.Event, error) {
	identity := referenceRepairKeyKeyPrefix + key.NodeID.String() + ":" + key.TemplateName + ":" + key.Encoded
	return referenceRepairEvent(ctx, audit.VerbOpsRepairReferenceUniqueness, principal, key.OrgID, identity,
		audit.Entity{
			Type: referenceRepairNodeEntity, NodeType: key.NodeType, ID: key.NodeID,
			Identifier: key.Encoded, Name: "",
		},
		referenceRepairExtra{
			Class: "reference keys", From: "", To: "",
			CounterKey: "", CounterValue: 0, TemplateName: key.TemplateName,
		},
		occurredAt)
}

// referenceRepairEvent assembles one event with an identity derived from the
// fact it records, so a rerun over unchanged state produces the same id and
// the ledger keeps one row.
func referenceRepairEvent(
	ctx context.Context,
	verb audit.Verb,
	principal audit.OperatorPrincipal,
	orgID uuid.UUID,
	idempotencyKey string,
	entity audit.Entity,
	extra referenceRepairExtra,
	occurredAt time.Time,
) (audit.Event, error) {
	encoded, err := json.Marshal(extra)
	if err != nil {
		wrapped := fmt.Errorf("encode repaired %s event: %w", extra.Class, err)
		telemetry.L(ctx).ErrorContext(ctx, "repair.reference_uniqueness.extra_encode_failed",
			slog.String("err", wrapped.Error()))
		return audit.Event{}, wrapped
	}
	return audit.Event{
		Verb:    string(verb),
		EventID: uuid.NewSHA1(referenceRepairAuditNamespace, []byte(idempotencyKey)),
		Actor: audit.Actor{
			Type: principal.ActorType(), ID: principal.ID,
			Email: principal.Email, Name: principal.Name,
			SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: entity,
		Context: audit.EventContext{
			OrgID: orgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: "", TraceID: "", Source: audit.SourceSystem, Tool: "", RPC: "", Reason: "",
		},
		Delta: nil, Outcome: audit.OutcomeOK, Error: nil,
		IdempotencyKey: idempotencyKey, OccurredAt: occurredAt.UTC(), Extra: encoded,
	}, nil
}
