package ops

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
)

const (
	referenceRenameEvidenceCitation = "TACK-342"
	referenceRenameBackfillPrefix   = "tack-429-reference-rename:"
)

var referenceRenameBackfillNamespace = uuid.MustParse("44907b0b-7377-581f-9c9b-b251947d4661")

//go:embed backfilldata/renames_2026_08_07.json
var referenceRenameEvidenceJSON []byte

type referenceRenameEvidence struct {
	OldReference string `json:"old_reference"`
	NewReference string `json:"new_reference"`
	NodeID       string `json:"node_id"`
}

type referenceRenameExtra struct {
	OldReference   string `json:"old_reference"`
	NewReference   string `json:"new_reference"`
	Reconstruction bool   `json:"reconstruction"`
	HistoricalTime string `json:"historical_time"`
	Evidence       string `json:"evidence"`
}

type referenceRenameResolver interface {
	Resolve(ctx context.Context, nodeID uuid.UUID) (*node.NodeResolve, error)
}

func loadReferenceRenameEvidence() ([]referenceRenameEvidence, error) {
	var renames []referenceRenameEvidence
	if err := json.Unmarshal(referenceRenameEvidenceJSON, &renames); err != nil {
		wrapped := fmt.Errorf("decode reference rename evidence: %w", err)
		slog.Error("audit.reference_rename_backfill.evidence_decode_failed",
			slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	if len(renames) == 0 {
		err := errors.New("reference rename evidence is empty")
		slog.Error("audit.reference_rename_backfill.evidence_invalid", slog.String("err", err.Error()))
		return nil, err
	}
	return renames, nil
}

func reconstructReferenceRenameEvents(
	ctx context.Context,
	outbox audit.OutboxWriter,
	cfg *config.Config,
) error {
	idempotentOutbox, ok := outbox.(audit.IdempotentOutboxWriter)
	if !ok {
		err := errors.New("audit outbox does not support idempotent reconstruction writes")
		slog.ErrorContext(ctx, "audit.reference_rename_backfill.outbox_invalid",
			slog.String("err", err.Error()))
		return err
	}
	principal, ok := audit.OperatorPrincipalFromContext(ctx)
	if !ok {
		err := errors.New("reference rename reconstruction has no operator principal")
		slog.ErrorContext(ctx, "audit.reference_rename_backfill.principal_missing",
			slog.String("err", err.Error()))
		return err
	}
	env, err := NewEnv(ctx, cfg)
	if err != nil {
		wrapped := fmt.Errorf("open ops environment for reference rename reconstruction: %w", err)
		slog.ErrorContext(ctx, "audit.reference_rename_backfill.env_failed",
			slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer env.Close()
	return recordReferenceRenameEvidence(ctx, idempotentOutbox, env.Stores.Views, principal, clock.Now().UTC())
}

func recordReferenceRenameEvidence(
	ctx context.Context,
	outbox audit.IdempotentOutboxWriter,
	reader referenceRenameResolver,
	principal audit.OperatorPrincipal,
	occurredAt time.Time,
) error {
	renames, err := loadReferenceRenameEvidence()
	if err != nil {
		return err
	}
	for _, rename := range renames {
		event, eventErr := referenceRenameEvent(ctx, reader, principal, rename, occurredAt)
		if eventErr != nil {
			return eventErr
		}
		_, writeErr := outbox.WriteOutboxIfAbsent(ctx, event)
		if writeErr != nil {
			wrapped := fmt.Errorf("record reconstructed reference rename %s to %s: %w",
				rename.OldReference, rename.NewReference, writeErr)
			slog.ErrorContext(ctx, "audit.reference_rename_backfill.write_failed",
				slog.String("node_id", rename.NodeID),
				slog.String("err", wrapped.Error()))
			return wrapped
		}
	}
	return nil
}

func referenceRenameEvent(
	ctx context.Context,
	reader referenceRenameResolver,
	principal audit.OperatorPrincipal,
	rename referenceRenameEvidence,
	occurredAt time.Time,
) (audit.Event, error) {
	nodeID, err := uuid.Parse(rename.NodeID)
	if err != nil {
		wrapped := fmt.Errorf("parse evidence node %q: %w", rename.NodeID, err)
		slog.ErrorContext(ctx, "audit.reference_rename_backfill.node_parse_failed",
			slog.String("err", wrapped.Error()))
		return audit.Event{}, wrapped
	}
	resolution, err := reader.Resolve(ctx, nodeID)
	if err != nil {
		wrapped := fmt.Errorf("resolve evidence node %s: %w", nodeID, err)
		slog.ErrorContext(ctx, "audit.reference_rename_backfill.node_resolve_failed",
			slog.String("err", wrapped.Error()))
		return audit.Event{}, wrapped
	}
	extra, err := json.Marshal(referenceRenameExtra{
		OldReference:   rename.OldReference,
		NewReference:   rename.NewReference,
		Reconstruction: true,
		HistoricalTime: historicalReferenceRenameTime(rename),
		Evidence:       referenceRenameEvidenceCitation,
	})
	if err != nil {
		wrapped := fmt.Errorf("encode reconstructed reference rename %s: %w", rename.NewReference, err)
		slog.ErrorContext(ctx, "audit.reference_rename_backfill.extra_encode_failed",
			slog.String("err", wrapped.Error()))
		return audit.Event{}, wrapped
	}
	return audit.Event{
		Verb:    string(audit.VerbNodeReferenceRename),
		EventID: referenceRenameEventID(rename),
		Actor: audit.Actor{
			Type:          audit.ActorOperator,
			ID:            principal.ID,
			Email:         principal.Email,
			Name:          principal.Name,
			SessionID:     "",
			IP:            "",
			UserAgent:     "",
			RequestID:     "",
			APITokenLabel: "",
		},
		Entity: audit.Entity{
			Type:       "node",
			NodeType:   resolution.NodeType,
			ID:         nodeID,
			Identifier: rename.NewReference,
			Name:       "",
		},
		Context: audit.EventContext{
			OrgID:       resolution.OrgID,
			WorkspaceID: uuid.Nil,
			ScopeID:     uuid.Nil,
			ParentID:    uuid.Nil,
			RequestID:   "",
			TraceID:     "",
			Source:      audit.SourceSystem,
			Tool:        "",
			RPC:         "",
			Reason:      "",
		},
		Delta:          nil,
		Outcome:        audit.OutcomeOK,
		Error:          nil,
		IdempotencyKey: referenceRenameIdempotencyKey(rename),
		OccurredAt:     occurredAt.UTC(),
		Extra:          extra,
	}, nil
}

func historicalReferenceRenameTime(rename referenceRenameEvidence) string {
	if rename.OldReference == "TACK-403" && rename.NewReference == "TACK-420" {
		return "2026-08-08"
	}
	return "2026-08-07"
}

func referenceRenameEventID(rename referenceRenameEvidence) uuid.UUID {
	return uuid.NewSHA1(referenceRenameBackfillNamespace, []byte(referenceRenameIdempotencyKey(rename)))
}

func referenceRenameIdempotencyKey(rename referenceRenameEvidence) string {
	return referenceRenameBackfillPrefix + rename.NodeID + ":" + rename.OldReference + ":" +
		rename.NewReference + ":" + historicalReferenceRenameTime(rename)
}
