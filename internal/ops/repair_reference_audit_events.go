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
	"goodkind.io/tack/internal/response"
	"goodkind.io/tack/internal/telemetry"
)

// Each reference rename, counter seed, and key claim requires a ledger event.
// The command audit records execution but does not identify these mutations
// for queries of a repaired node's history (TACK-448).
const (
	referenceRepairRenameKeyPrefix  = "tack-448-rename:"
	referenceRepairCounterKeyPrefix = "tack-448-counter:"
	referenceRepairKeyKeyPrefix     = "tack-448-key:"
	referenceRepairCounterEntity    = "sequence_counter"
	referenceRepairNodeEntity       = "node"
)

// referenceRepairAuditNamespace derives event identities independently of
// reconstruction events to prevent collisions for the same rename.
var referenceRepairAuditNamespace = uuid.MustParse("2b3d9a41-6c8e-5f77-9a02-7d1c4be8f5a3")

// referenceRepairExtra records repair details. It does not include a
// reconstruction flag; these events record changes at the time of execution.
type referenceRepairExtra struct {
	Class        string `json:"class"`
	From         string `json:"from,omitempty"`
	To           string `json:"to,omitempty"`
	CounterKey   string `json:"counter_key,omitempty"`
	CounterValue int64  `json:"counter_value,omitempty"`
	TemplateName string `json:"template,omitempty"`
}

// referenceRepairEvents builds an event for each change in the report.
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
	// Live and reconstructed rename events use the same verb for history queries.
	return referenceRepairEvent(
		ctx,
		audit.VerbNodeReferenceRename,
		principal,
		rename.OrgID,
		key,
		audit.Entity{
			Type:       referenceRepairNodeEntity,
			NodeType:   "",
			ID:         rename.NodeID,
			Identifier: rename.To,
			Name:       "",
		},
		referenceRepairExtra{
			Class:        "reference renames",
			From:         rename.From,
			To:           rename.To,
			CounterKey:   "",
			CounterValue: 0,
			TemplateName: "",
		},
		occurredAt,
	)
}

func referenceRepairCounterEvent(
	ctx context.Context,
	principal audit.OperatorPrincipal,
	counter ReferenceCounterSeed,
	occurredAt time.Time,
) (audit.Event, error) {
	key := referenceRepairCounterKeyPrefix + counter.Key + ":" + strconv.FormatInt(
		counter.Value,
		10,
	)

	return referenceRepairEvent(
		ctx,
		audit.VerbOpsRepairReferenceUniqueness,
		principal,
		counter.OrgID,
		key,
		audit.Entity{
			Type:       referenceRepairCounterEntity,
			NodeType:   "",
			ID:         uuid.NewSHA1(referenceRepairAuditNamespace, []byte(counter.Key)),
			Identifier: counter.Key,
			Name:       "",
		},
		referenceRepairExtra{
			Class:        "counter seeds",
			From:         "",
			To:           "",
			CounterKey:   counter.Key,
			CounterValue: counter.Value,
			TemplateName: "",
		},
		occurredAt,
	)
}

func referenceRepairKeyEvent(
	ctx context.Context,
	principal audit.OperatorPrincipal,
	key ReferenceKeyWrite,
	occurredAt time.Time,
) (audit.Event, error) {
	identity := referenceRepairKeyKeyPrefix + key.NodeID.String() + ":" + key.TemplateName + ":" + key.Encoded

	return referenceRepairEvent(
		ctx,
		audit.VerbOpsRepairReferenceUniqueness,
		principal,
		key.OrgID,
		identity,
		audit.Entity{
			Type:       referenceRepairNodeEntity,
			NodeType:   key.NodeType,
			ID:         key.NodeID,
			Identifier: key.Encoded,
			Name:       "",
		},
		referenceRepairExtra{
			Class:        "reference keys",
			From:         "",
			To:           "",
			CounterKey:   "",
			CounterValue: 0,
			TemplateName: key.TemplateName,
		},
		occurredAt,
	)
}

// referenceRepairEvent derives its event ID from the recorded change.
// Repeating an unchanged report uses the same ID and preserves one ledger row.
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
		telemetry.L(ctx).ErrorContext(
			ctx,
			"repair.reference_uniqueness.extra_encode_failed",
			slog.String("err", wrapped.Error()),
		)

		return audit.Event{}, wrapped
	}

	correlation := response.FromContext(ctx)

	return audit.Event{
		Verb:    string(verb),
		EventID: uuid.NewSHA1(referenceRepairAuditNamespace, []byte(idempotencyKey)),
		Actor: audit.Actor{
			Type:          principal.ActorType(),
			ID:            principal.ID,
			Email:         principal.Email,
			Name:          principal.Name,
			SessionID:     "",
			IP:            "",
			UserAgent:     "",
			RequestID:     correlation.RequestID,
			APITokenLabel: "",
		},
		Entity: entity,
		Context: audit.EventContext{
			OrgID:       orgID,
			WorkspaceID: uuid.Nil,
			ScopeID:     uuid.Nil,
			ParentID:    uuid.Nil,
			RequestID:   correlation.RequestID,
			TraceID:     correlation.TraceID,
			Source:      audit.SourceSystem,
			Tool:        "",
			RPC:         "",
			Reason:      "",
		},
		Delta:          nil,
		Outcome:        audit.OutcomeOK,
		Error:          nil,
		IdempotencyKey: idempotencyKey,
		OccurredAt:     occurredAt.UTC(),
		Extra:          encoded,
	}, nil
}
