package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/clock"
)

const partitionTargetReference = "yugabyte/audit.events/partitions"

type partitionRecording struct {
	outbox OutboxWriter
	actor  Actor
}

type partitionMaintenanceEvidence struct {
	HeadroomBefore *int `json:"headroom_weeks_before,omitempty"`
	HeadroomAfter  *int `json:"headroom_weeks_after,omitempty"`
}

// RecordRuns configures the manager to record each maintenance run as a service
// operation with one step, using outbox and actor. It returns the manager.
func (m *PartitionManager) RecordRuns(outbox OutboxWriter, actor Actor) *PartitionManager {
	m.recording = &partitionRecording{outbox: outbox, actor: actor}
	return m
}

// RecordPartitionRuns configures the consumer to record partition maintenance
// through outbox using actor. Call it before Start.
func (c *Consumer) RecordPartitionRuns(outbox OutboxWriter, actor Actor) {
	c.partitions.RecordRuns(outbox, actor)
}

func (m *PartitionManager) maintain(ctx context.Context) error {
	if m.recording == nil {
		if err := m.store.RunMaintenance(ctx); err != nil {
			slog.ErrorContext(ctx, "audit.partition.maintain_failed", slog.String("err", err.Error()))
			return fmt.Errorf("maintain the audit partitions: %w", err)
		}
		return nil
	}
	before, err := m.headroomEvidence(ctx, "before")
	if err != nil {
		return err
	}
	planned := partitionMaintenanceEvidence{HeadroomBefore: before, HeadroomAfter: nil}
	encoded, err := encodePartitionEvidence(ctx, planned)
	if err != nil {
		return err
	}
	target := Target{
		Kind: TargetKindExternal, NodeType: "", Reference: partitionTargetReference, Name: "", URI: "", RelationType: "",
		ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	return RunServiceStep(ctx, m.recording.outbox, m.recording.actor, VerbAuditPartitionMaintain, target, encoded,
		m.store.RunMaintenance,
		func(ctx context.Context) (json.RawMessage, error) {
			after, err := m.headroomEvidence(ctx, "after")
			if err != nil {
				return nil, err
			}
			observed := planned
			observed.HeadroomAfter = after
			return encodePartitionEvidence(ctx, observed)
		})
}

// headroomEvidence reads audit partition headroom in weeks and returns any
// read error.
func (m *PartitionManager) headroomEvidence(ctx context.Context, moment string) (*int, error) {
	headroom, err := m.store.HeadroomWeeks(ctx, clock.Now().UTC())
	if err != nil {
		slog.ErrorContext(ctx, "audit.partition.headroom_failed", slog.String("moment", moment), slog.String("err", err.Error()))
		return nil, fmt.Errorf("read the audit partition headroom %s maintenance: %w", moment, err)
	}
	return &headroom, nil
}

func encodePartitionEvidence(ctx context.Context, evidence partitionMaintenanceEvidence) (json.RawMessage, error) {
	encoded, err := json.Marshal(evidence)
	if err != nil {
		slog.ErrorContext(ctx, "audit.partition.evidence_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("encode the partition maintenance evidence: %w", err)
	}
	return encoded, nil
}
