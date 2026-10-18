package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// RepairApplyStatus identifies the execution outcome for one manifest entry.
type RepairApplyStatus string

const (
	// RepairApplyApplied indicates that this execution committed at least one
	// phase of the entry.
	RepairApplyApplied RepairApplyStatus = "applied"
	// RepairApplyUnchanged indicates that this execution did not commit a phase.
	// The entry is complete, or the runner does not execute its next phase.
	RepairApplyUnchanged RepairApplyStatus = "unchanged"
	// RepairApplyConflicted indicates that a record or checkpoint conflict
	// prevented further execution of the entry.
	RepairApplyConflicted RepairApplyStatus = "conflicted"
	// RepairApplyUnresolved identifies an unresolved entry that this execution
	// did not apply.
	RepairApplyUnresolved RepairApplyStatus = "unresolved"
	// RepairApplyFailed indicates that execution failed for the entry.
	RepairApplyFailed RepairApplyStatus = "failed"
)

// NodeIntegrityEntryApply records one entry's execution outcome and committed phases.
type NodeIntegrityEntryApply struct {
	RepairID  string             `json:"repair_id"`
	NodeID    uuid.UUID          `json:"node_id"`
	Status    RepairApplyStatus  `json:"status"`
	Committed []node.RepairPhase `json:"committed"`
	NextPhase node.RepairPhase   `json:"next_phase,omitempty"`
	Reason    string             `json:"reason,omitempty"`
}

// NodeIntegrityApply records execution outcomes and counts for a frozen manifest.
type NodeIntegrityApply struct {
	ManifestDigest string                    `json:"manifest_digest"`
	Counts         map[RepairApplyStatus]int `json:"counts"`
	Entries        []NodeIntegrityEntryApply `json:"entries"`
}

var errRepairEntriesFailed = errors.New("at least one repair entry failed")

type repairApplier struct {
	manifest  NodeIntegrityManifest
	state     repairStateReader
	phases    repairPhaseRunner
	principal audit.OperatorPrincipal
}

func (applier repairApplier) applyFrozenManifest(ctx context.Context) (NodeIntegrityApply, error) {
	result := NodeIntegrityApply{
		ManifestDigest: applier.manifest.Digest, Counts: map[RepairApplyStatus]int{},
		Entries: make([]NodeIntegrityEntryApply, 0, len(applier.manifest.Entries)),
	}
	for _, entry := range applier.manifest.Entries {
		outcome := applier.applyEntry(ctx, entry)
		result.Counts[outcome.Status]++
		result.Entries = append(result.Entries, outcome)
	}
	telemetry.L(ctx).InfoContext(ctx, "repair.manifest.applied", slog.String("manifest_digest", applier.manifest.Digest),
		slog.Int("applied", result.Counts[RepairApplyApplied]), slog.Int("unchanged", result.Counts[RepairApplyUnchanged]),
		slog.Int("conflicted", result.Counts[RepairApplyConflicted]), slog.Int("unresolved", result.Counts[RepairApplyUnresolved]),
		slog.Int("failed", result.Counts[RepairApplyFailed]))
	if result.Counts[RepairApplyFailed] > 0 {
		wrapped := fmt.Errorf("apply manifest %s: %d entries: %w", applier.manifest.Digest, result.Counts[RepairApplyFailed], errRepairEntriesFailed)
		slog.ErrorContext(ctx, "repair.manifest.apply_failed", slog.String("err", wrapped.Error()))
		return result, wrapped
	}
	return result, nil
}

func (applier repairApplier) applyEntry(ctx context.Context, entry NodeIntegrityEntry) NodeIntegrityEntryApply {
	outcome := NodeIntegrityEntryApply{
		RepairID: entry.RepairID, NodeID: entry.NodeID, Status: RepairApplyUnresolved,
		Committed: []node.RepairPhase{}, NextPhase: "", Reason: "",
	}
	for {
		preview, err := previewRepairEntry(ctx, applier.state, entry)
		if err != nil {
			outcome.Status, outcome.Reason = RepairApplyFailed, err.Error()
			return outcome
		}
		switch preview.Status {
		case RepairPreviewUnresolved:
			outcome.Status, outcome.Reason = RepairApplyUnresolved, preview.Reason
			return outcome
		case RepairPreviewConflicted:
			outcome.Status, outcome.Reason = RepairApplyConflicted, preview.Reason
			return outcome
		case RepairPreviewCompleted, RepairPreviewPlanned, RepairPreviewInProgress:
		}
		outcome.NextPhase = preview.NextPhase
		if preview.Status == RepairPreviewCompleted || !applier.phases.runs(preview.NextPhase) {
			outcome.Status = RepairApplyUnchanged
			if len(outcome.Committed) > 0 {
				outcome.Status = RepairApplyApplied
			}
			return outcome
		}
		if err := applier.phases.runPhase(ctx, applier.manifest.Digest, applier.principal, entry, preview.NextPhase); err != nil {
			outcome.Status, outcome.Reason = RepairApplyFailed, err.Error()
			return outcome
		}
		outcome.Committed = append(outcome.Committed, preview.NextPhase)
	}
}
