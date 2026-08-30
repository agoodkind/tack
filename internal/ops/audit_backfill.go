package ops

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
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
	// SubjectAbsent says the renamed node no longer exists, so the event's
	// node type could not be read from live state. The rename itself is
	// unaffected: the evidence file records it, and it happened.
	SubjectAbsent bool `json:"subject_absent,omitempty"`
}

type referenceRenameResolver interface {
	Resolve(ctx context.Context, nodeID uuid.UUID) (*node.NodeResolve, error)
}

func loadReferenceRenameEvidence(ctx context.Context) ([]referenceRenameEvidence, error) {
	var renames []referenceRenameEvidence
	if err := json.Unmarshal(referenceRenameEvidenceJSON, &renames); err != nil {
		wrapped := fmt.Errorf("decode reference rename evidence: %w", err)
		telemetry.L(ctx).Error("audit.reference_rename_backfill.evidence_decode_failed",
			slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	if len(renames) == 0 {
		err := errors.New("reference rename evidence is empty")
		telemetry.L(ctx).Error("audit.reference_rename_backfill.evidence_invalid", slog.String("err", err.Error()))
		return nil, err
	}
	return renames, nil
}

func reconstructReferenceRepairEvents(
	ctx context.Context,
	outbox audit.OutboxWriter,
	cfg *config.Config,
) (auditBackfillPlan, error) {
	idempotentOutbox, ok := outbox.(audit.IdempotentOutboxWriter)
	if !ok {
		err := errors.New("audit outbox does not support idempotent reconstruction writes")
		telemetry.L(ctx).ErrorContext(ctx, "audit.reference_rename_backfill.outbox_invalid",
			slog.String("err", err.Error()))
		return auditBackfillPlan{}, err
	}
	principal, ok := audit.OperatorPrincipalFromContext(ctx)
	if !ok {
		err := errors.New("reference rename reconstruction has no operator principal")
		telemetry.L(ctx).ErrorContext(ctx, "audit.reference_rename_backfill.principal_missing",
			slog.String("err", err.Error()))
		return auditBackfillPlan{}, err
	}
	env, err := NewEnv(ctx, cfg)
	if err != nil {
		wrapped := fmt.Errorf("open ops environment for reference rename reconstruction: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "audit.reference_rename_backfill.env_failed",
			slog.String("err", wrapped.Error()))
		return auditBackfillPlan{}, wrapped
	}
	defer env.Close()
	plan, err := deriveReferenceRepairHistory(ctx, env, principal, clock.Now().UTC())
	if err != nil {
		return auditBackfillPlan{}, err
	}
	if err := plan.Write(ctx, idempotentOutbox); err != nil {
		return auditBackfillPlan{}, err
	}
	return plan, nil
}

func previewReferenceRepairHistory(
	ctx context.Context,
	cfg *config.Config,
	principal audit.OperatorPrincipal,
) (auditBackfillPlan, error) {
	env, err := NewEnv(ctx, cfg)
	if err != nil {
		wrapped := fmt.Errorf("open ops environment for reference repair reconstruction: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "audit.reference_repair_backfill.env_failed", slog.String("err", wrapped.Error()))
		return auditBackfillPlan{}, wrapped
	}
	defer env.Close()
	return deriveReferenceRepairHistory(ctx, env, principal, clock.Now().UTC())
}
