package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	indexTargetPrefix = "opensearch/index/"
	aliasTarget       = "opensearch/public-alias"
)

// SearchAudit configures audit recording for index creation, splits, deletion,
// public alias switches, and access rollout steps that can change the phase.
// The worker does not record audit events for page indexing.
type SearchAudit struct {
	Outbox audit.OutboxWriter
	Actor  audit.Actor
}

type indexStructureEvidence struct {
	Action        string `json:"action"`
	RebuildID     string `json:"rebuild_id"`
	Mode          string `json:"mode"`
	SourceIndex   string `json:"source_index"`
	TargetIndex   string `json:"target_index"`
	PrimaryShards int    `json:"primary_shards"`
	RoutingShards int    `json:"routing_shards"`
	Replicas      int    `json:"replicas"`
	AliasIndex    string `json:"alias_index,omitempty"`
}

type rolloutPhaseEvidence struct {
	AuthorityID      string `json:"authority_id"`
	Generation       int64  `json:"generation"`
	CandidateVersion string `json:"candidate_version"`
	PhaseBefore      string `json:"phase_before"`
	ActiveBefore     string `json:"active_before"`
	PhaseAfter       string `json:"phase_after,omitempty"`
	ActiveAfter      string `json:"active_after,omitempty"`
}

func replacementEvidence(action string, rebuild searchdomain.Rebuild) indexStructureEvidence {
	return indexStructureEvidence{
		Action: action, RebuildID: rebuild.ID.String(), Mode: string(rebuild.Mode), SourceIndex: rebuild.SourceIndex,
		TargetIndex: rebuild.TargetIndex, PrimaryShards: rebuild.PrimaryShards, RoutingShards: rebuild.RoutingShards,
		Replicas: rebuild.Replicas, AliasIndex: "",
	}
}

func (w *SearchWorker) recordIndexStructure(
	ctx context.Context,
	action, index string,
	rebuild searchdomain.Rebuild,
	effect func(context.Context) error,
) error {
	encoded, err := encodeStructureEvidence(ctx, replacementEvidence(action, rebuild))
	if err != nil {
		return err
	}
	return w.recordStructure(ctx, audit.VerbSearchIndexStructure, indexTargetPrefix+index, encoded, effect,
		func(context.Context) (json.RawMessage, error) { return encoded, nil })
}

func (w *SearchWorker) recordAliasSwitch(ctx context.Context, rebuild searchdomain.Rebuild, effect func(context.Context) error) error {
	planned := replacementEvidence("switch_alias", rebuild)
	encoded, err := encodeStructureEvidence(ctx, planned)
	if err != nil {
		return err
	}
	return w.recordStructure(ctx, audit.VerbSearchIndexStructure, aliasTarget, encoded, effect,
		func(ctx context.Context) (json.RawMessage, error) {
			current, err := w.ports.Replacer.PublicAliasTarget(ctx)
			if err != nil {
				return nil, structureFailure(ctx, "read the public alias after the switch", err)
			}
			observed := planned
			observed.AliasIndex = current
			return encodeStructureEvidence(ctx, observed)
		})
}

func (w *SearchWorker) recordRolloutPhase(ctx context.Context, rollout searchdomain.AccessRollout, effect func(context.Context) error) error {
	planned := rolloutPhaseEvidence{
		AuthorityID: rollout.AuthorityID.String(), Generation: rollout.Generation, CandidateVersion: rollout.CandidateVersion,
		PhaseBefore: string(rollout.Phase), ActiveBefore: rollout.ActiveVersion, PhaseAfter: "", ActiveAfter: "",
	}
	encoded, err := encodeStructureEvidence(ctx, planned)
	if err != nil {
		return err
	}
	return w.recordStructure(ctx, audit.VerbSearchAccessRolloutPhase,
		searchdomain.AccessRolloutTargetReference(planned.AuthorityID), encoded, effect,
		func(ctx context.Context) (json.RawMessage, error) {
			after, err := w.ports.Rollouts.Current(ctx, rollout.AuthorityID)
			if err != nil {
				return nil, structureFailure(ctx, "read the access rollout after the step", err)
			}
			observed := planned
			observed.PhaseAfter, observed.ActiveAfter = string(after.Phase), after.ActiveVersion
			return encodeStructureEvidence(ctx, observed)
		})
}

func (w *SearchWorker) recordStructure(
	ctx context.Context,
	verb audit.Verb,
	reference string,
	planned json.RawMessage,
	effect func(context.Context) error,
	observe func(context.Context) (json.RawMessage, error),
) error {
	target := audit.Target{
		Kind: audit.TargetKindExternal, NodeType: "", Reference: reference, Name: "", URI: "", RelationType: "",
		ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	if err := audit.RunServiceStep(ctx, w.ports.Audit.Outbox, w.ports.Audit.Actor, verb, target, planned, effect, observe); err != nil {
		return structureFailure(ctx, "run "+string(verb)+" on "+reference, err)
	}
	return nil
}

type structureEvidence interface {
	indexStructureEvidence | rolloutPhaseEvidence
}

func encodeStructureEvidence[Evidence structureEvidence](ctx context.Context, evidence Evidence) (json.RawMessage, error) {
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return nil, structureFailure(ctx, "encode the search worker step evidence", err)
	}
	return encoded, nil
}

func structureFailure(ctx context.Context, operation string, err error) error {
	wrapped := fmt.Errorf("search worker: %s: %w", operation, err)
	slog.ErrorContext(ctx, "search.worker.audit_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
