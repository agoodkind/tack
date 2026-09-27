package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

var _ searchdomain.RetentionReader = (*Adapter)(nil)

// errNotAcknowledged means OpenSearch did not acknowledge a cluster change.
var errNotAcknowledged = errors.New("the change was not acknowledged")

type aliasActions struct {
	Actions []aliasAction `json:"actions"`
}

type aliasAction struct {
	Remove *aliasBinding `json:"remove,omitempty"`
	Add    *aliasBinding `json:"add,omitempty"`
}

type aliasBinding struct {
	Index        string `json:"index"`
	Alias        string `json:"alias"`
	IsWriteIndex bool   `json:"is_write_index,omitempty"`
}

// PublicAliasTarget returns the one physical index the public alias selects.
func (a *Adapter) PublicAliasTarget(ctx context.Context) (string, error) {
	return a.AliasTarget(ctx, PublicAlias)
}

// SwitchPublicAlias removes the public alias from one physical index and
// adds it to another in one typed alias request. OpenSearch applies both
// actions atomically. An uncertain result requires the caller to read the
// alias.
func (a *Adapter) SwitchPublicAlias(ctx context.Context, from, to string) error {
	remove := aliasBinding{Index: from, Alias: PublicAlias, IsWriteIndex: false}
	add := aliasBinding{Index: to, Alias: PublicAlias, IsWriteIndex: true}
	body, err := json.Marshal(aliasActions{Actions: []aliasAction{{Remove: &remove, Add: nil}, {Remove: nil, Add: &add}}})
	if err != nil {
		return engineFailure(ctx, "search.alias.encode_failed", "encode alias switch to "+to, to, err)
	}
	response, err := a.api.Aliases(ctx, opensearchapi.AliasesReq{Body: bytes.NewReader(body)})
	if err != nil {
		return engineFailure(ctx, "search.alias.switch_failed", "switch alias "+PublicAlias+" from "+from+" to "+to, to, err)
	}
	if !response.Acknowledged {
		return engineFailure(ctx, "search.alias.switch_unacknowledged", "switch alias "+PublicAlias+" to "+to, to, errNotAcknowledged)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.alias.switched", slog.String("alias", PublicAlias),
		slog.String("previous_index", from), slog.String("index", to))
	return nil
}

// DeleteIndex deletes one physical index. A missing index satisfies the
// request.
func (a *Adapter) DeleteIndex(ctx context.Context, index string) error {
	ignoreMissing := true
	_, err := a.api.Indices.Delete(ctx, opensearchapi.IndicesDeleteReq{
		Indices: []string{index}, Header: nil,
		Params: opensearchapi.IndicesDeleteParams{IgnoreUnavailable: &ignoreMissing},
	})
	if err != nil {
		return engineFailure(ctx, "search.index.delete_failed", "delete index "+index, index, err)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.index.deleted", slog.String("index", index))
	return nil
}

type retiredCount struct {
	Query booleanTermClause `json:"query"`
}

// IndexRetention counts the retired pages of index and reads the bytes its
// primary shards store.
func (a *Adapter) IndexRetention(ctx context.Context, index string) (searchdomain.RetentionStats, error) {
	var none searchdomain.RetentionStats
	body, err := json.Marshal(retiredCount{Query: booleanTermClause{Term: map[string]bool{"retired": true}}})
	if err != nil {
		return none, engineFailure(ctx, "search.retention.encode_failed", "encode retired page count", index, err)
	}
	count, err := a.api.Indices.Count(ctx, &opensearchapi.IndicesCountReq{Indices: []string{index}, Body: bytes.NewReader(body)})
	if err != nil {
		return none, engineFailure(ctx, "search.retention.count_failed", "count retired pages of "+index, index, err)
	}
	stats, err := a.api.Indices.Stats(ctx, &opensearchapi.IndicesStatsReq{Indices: []string{index}, Metrics: []string{"store"}})
	if err != nil {
		return none, engineFailure(ctx, "search.retention.stats_failed", "read storage of "+index, index, err)
	}
	details, exists := stats.Indices[index]
	if !exists {
		return none, engineFailure(ctx, "search.retention.stats_absent", "read storage of "+index, index, errors.New("statistics are absent"))
	}
	return searchdomain.RetentionStats{RetiredPages: int64(count.Count), PrimaryBytes: details.Primaries.Store.SizeInBytes}, nil
}

// engineFailure logs one failed OpenSearch operation and returns it wrapped.
func engineFailure(ctx context.Context, event, operation, index string, err error) error {
	wrapped := fmt.Errorf("%s: %w", operation, err)
	telemetry.L(ctx).ErrorContext(ctx, event, slog.String("err", wrapped.Error()), slog.String("index", index))
	return wrapped
}
