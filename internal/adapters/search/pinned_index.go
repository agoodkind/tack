package search

import (
	"context"
	"errors"
)

var errUnpinnedIndexMapping = errors.New(
	"the mapping does not record the pinned model and tokenizer",
)

func (r *QueryRanker) readSnapshotIndex(
	ctx context.Context,
	target string,
	timing *snapshotTiming,
) (IndexInfo, error) {
	var none IndexInfo
	info, err := r.adapter.IndexInfo(ctx, target)
	timing.mark(&timing.mapping)
	if err != nil {
		return none, snapshotFailure(ctx, target, "read the index mapping", err)
	}

	if info.ModelID == "" || info.MappingVersion == "" ||
		info.TokenizerSHA256 != PinnedModel.TokenizerDigest {
		return none, snapshotFailure(
			ctx,
			target,
			"verify the index mapping",
			errUnpinnedIndexMapping,
		)
	}

	model, err := r.adapter.getModel(ctx, info.ModelID)
	timing.mark(&timing.model)
	if err != nil {
		return none, snapshotFailure(ctx, target, "read the pinned model", err)
	}

	if cause := pinnedModelCause(model); cause != nil {
		return none, snapshotFailure(ctx, target, "verify the pinned model", cause)
	}

	return info, nil
}
