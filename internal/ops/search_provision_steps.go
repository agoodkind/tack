package ops

import (
	"context"

	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
)

type searchModelEvidence struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	BundleDigest    string `json:"bundle_digest"`
	TokenizerDigest string `json:"tokenizer_digest"`
}

func (searchModelEvidence) isStepEvidence() {}

type searchIndexEvidence struct {
	Index          string `json:"index"`
	MappingVersion string `json:"mapping_version"`
	ModelID        string `json:"model_id"`
	Primaries      int    `json:"primary_shards"`
	RoutingShards  int    `json:"routing_shards"`
	Replicas       int    `json:"replicas"`
}

func (searchIndexEvidence) isStepEvidence() {}

type searchAliasEvidence struct {
	Alias string `json:"alias"`
	Index string `json:"index"`
}

func (searchAliasEvidence) isStepEvidence() {}

func provisionSearchSteps(
	ctx context.Context,
	f *cli.Factory,
	adapter *search.Adapter,
	topology config.SearchTopology,
	steps audit.StepRecorder,
) (audit.OutcomeDetails, error) {
	var model search.ModelInfo
	plannedModel := searchModelEvidence{
		Name:            search.PinnedModel.Name,
		Version:         search.PinnedModel.Version,
		BundleDigest:    search.PinnedModel.BundleDigest,
		TokenizerDigest: search.PinnedModel.TokenizerDigest,
	}
	details, err := searchStep(
		ctx,
		steps,
		"opensearch/model/"+plannedModel.Name,
		plannedModel,
		func(ctx context.Context) error {
			provisioned, provisionErr := adapter.Provision(ctx)
			if provisionErr != nil {
				return searchProvisionFailure(ctx, "provision search model", provisionErr)
			}

			model = provisioned

			return nil
		},
	)
	if err != nil {
		return details, err
	}

	spec := search.IndexSpec{
		Model:          model,
		MappingVersion: search.MappingVersion,
		Primaries:      topology.Primaries,
		RoutingShards:  topology.RoutingShards,
		Replicas:       topology.Replicas,
	}
	plannedIndex := searchIndexEvidence{
		Index:          provisionIndex,
		MappingVersion: spec.MappingVersion,
		ModelID:        model.ID,
		Primaries:      spec.Primaries,
		RoutingShards:  spec.RoutingShards,
		Replicas:       spec.Replicas,
	}
	details, err = searchStep(
		ctx,
		steps,
		"opensearch/index/"+provisionIndex,
		plannedIndex,
		func(ctx context.Context) error {
			if err := adapter.EnsureIndex(ctx, provisionIndex, spec); err != nil {
				return searchProvisionFailure(ctx, "ensure search index "+provisionIndex, err)
			}

			if err := adapter.WaitGreen(ctx, provisionIndex, search.DefaultGreenWait); err != nil {
				return searchProvisionFailure(
					ctx,
					"wait for green search index "+provisionIndex,
					err,
				)
			}

			return nil
		},
	)
	if err != nil {
		return details, err
	}

	return provisionSearchAlias(ctx, f, adapter, steps)
}

func provisionSearchAlias(
	ctx context.Context,
	f *cli.Factory,
	adapter *search.Adapter,
	steps audit.StepRecorder,
) (audit.OutcomeDetails, error) {
	plannedAlias := searchAliasEvidence{
		Alias: search.PublicAlias,
		Index: provisionIndex,
	}
	details, err := searchStep(
		ctx,
		steps,
		"opensearch/alias/"+search.PublicAlias,
		plannedAlias,
		func(ctx context.Context) error {
			if err := adapter.SetAlias(ctx, search.PublicAlias, provisionIndex); err != nil {
				return searchProvisionFailure(ctx, "set search alias "+search.PublicAlias, err)
			}

			if _, err := adapter.AliasTarget(ctx, search.PublicAlias); err != nil {
				return searchProvisionFailure(ctx, "read search alias "+search.PublicAlias, err)
			}

			return nil
		},
	)
	if err != nil {
		return details, err
	}

	if err := recordServingSearchIndex(ctx, f, provisionIndex); err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}

	return details, nil
}

func searchStep[Evidence stepEvidence](
	ctx context.Context,
	steps audit.StepRecorder,
	reference string,
	planned Evidence,
	effect func(context.Context) error,
) (audit.OutcomeDetails, error) {
	plannedJSON, err := encodeEvidence(ctx, planned)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}

	return recordedStep(
		ctx,
		steps,
		audit.ExternalStep(externalTarget(reference), plannedJSON),
		effect,
	)
}
