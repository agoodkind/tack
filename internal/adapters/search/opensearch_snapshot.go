package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v5"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// PublicAlias selects the serving physical index for public search.
const PublicAlias = "node-pages"

// RankerSettings bounds point-in-time lifetime, query-token bytes, and raw
// batch size.
type RankerSettings struct {
	KeepAlive  time.Duration
	TokenBytes int
	BatchSize  int
}

// QueryRanker opens point-in-time snapshots and reads ranked page batches
// with the adapter's official client.
type QueryRanker struct {
	adapter  *Adapter
	settings RankerSettings
}

var _ searchdomain.Ranker = (*QueryRanker)(nil)

// Ranker returns the ranked query path over this adapter's client.
func (a *Adapter) Ranker(settings RankerSettings) *QueryRanker {
	return &QueryRanker{adapter: a, settings: settings}
}

// Open resolves the public alias, verifies the physical index and pinned
// model, computes the query tokens once, and creates one point in time. It
// deletes a point in time that OpenSearch created with failed shards.
func (r *QueryRanker) Open(ctx context.Context, query searchdomain.Query) (searchdomain.Snapshot, error) {
	var none searchdomain.Snapshot
	target, err := r.adapter.AliasTarget(ctx, PublicAlias)
	if err != nil {
		return none, snapshotFailure(ctx, query.Index, "resolve the public alias", err)
	}
	if query.Index == "" || target != query.Index {
		return none, snapshotFailure(ctx, query.Index, "verify the serving index",
			fmt.Errorf("alias %s selects %q, and FoundationDB records %q", PublicAlias, target, query.Index))
	}
	info, err := r.adapter.IndexInfo(ctx, target)
	if err != nil {
		return none, snapshotFailure(ctx, target, "read the index mapping", err)
	}
	if info.ModelID == "" || info.MappingVersion == "" || info.TokenizerSHA256 != PinnedModel.TokenizerDigest {
		return none, snapshotFailure(ctx, target, "verify the index mapping", errors.New("the mapping does not record the pinned model and tokenizer"))
	}
	model, err := r.adapter.getModel(ctx, info.ModelID)
	if err != nil {
		return none, snapshotFailure(ctx, target, "read the pinned model", err)
	}
	if cause := pinnedModelCause(model); cause != nil {
		return none, snapshotFailure(ctx, target, "verify the pinned model", cause)
	}
	tokens, err := r.adapter.predictQueryTokens(ctx, info.ModelID, query.Text, r.settings.TokenBytes)
	if err != nil {
		return none, queryStepError{operation: "open search snapshot on " + target, err: err}
	}
	created, err := r.adapter.api.PointInTime.Create(ctx, &opensearchapi.CreatePITReq{
		Indices: []string{target}, Header: nil,
		Params: &opensearchapi.CreatePITParams{KeepAlive: r.settings.KeepAlive},
	})
	if err != nil {
		return none, snapshotFailure(ctx, target, "create a point in time", engineCause(ctx, created.Inspect().Response, err))
	}
	if !created.PITID.IsSet() || created.Shards.Failed > 0 {
		partial := searchdomain.Snapshot{PITID: created.PITID.String(), Index: target, QueryTokens: nil}
		closeErr := r.Close(ctx, partial)
		return none, snapshotFailure(ctx, target, "create a point in time", errors.Join(
			fmt.Errorf("OpenSearch returned an empty ID or %d failed shards", created.Shards.Failed), closeErr))
	}
	telemetry.L(ctx).DebugContext(ctx, "search.snapshot.opened", slog.String("index", target), slog.Int("token_bytes", len(tokens)))
	return searchdomain.Snapshot{PITID: created.PITID.String(), Index: target, QueryTokens: tokens}, nil
}

// Close deletes the point in time. A point in time OpenSearch no longer
// recognizes is already closed.
func (r *QueryRanker) Close(ctx context.Context, snapshot searchdomain.Snapshot) error {
	if snapshot.PITID == "" {
		return nil
	}
	id, err := opensearchapi.ParsePITID(snapshot.PITID)
	if err != nil {
		return snapshotFailure(ctx, snapshot.Index, "parse a point in time", err)
	}
	response, err := r.adapter.api.PointInTime.Delete(ctx, &opensearchapi.DeletePITReq{Body: &opensearchapi.DeletePITBody{PITID: []opensearchapi.PITID{id}}})
	if err == nil {
		return nil
	}
	var deleted *opensearch.Response
	if response != nil {
		deleted = response.Inspect().Response
	}
	if deleted != nil && deleted.StatusCode == http.StatusNotFound {
		return nil
	}
	return snapshotFailure(ctx, snapshot.Index, "delete a point in time", engineCause(ctx, deleted, err))
}

func snapshotFailure(ctx context.Context, index, operation string, err error) error {
	wrapped := fmt.Errorf("search snapshot on %s: %s: %w", index, operation, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.snapshot.failed", slog.String("err", wrapped.Error()), slog.String("index", index))
	return wrapped
}

// `pinnedModelCause` classifies a mismatch only in model state as `ErrEngineUnavailable` because public queries require a `DEPLOYED` model.
func pinnedModelCause(model registeredModel) error {
	mismatches := modelMismatches(model)
	if len(mismatches) == 0 {
		return nil
	}
	joined := errors.Join(mismatches...)
	deployed := model
	deployed.State = deployedModelState
	if len(modelMismatches(deployed)) > 0 {
		return joined
	}
	return unavailableError{err: joined}
}
