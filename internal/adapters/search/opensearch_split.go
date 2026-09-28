package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

var _ searchdomain.IndexReplacer = (*Adapter)(nil)

// CreateReplacement creates one empty strict replacement index with the
// pinned model, or verifies the index a previous attempt created. Each node
// configuration sets action.auto_create_index to refuse automatic creation
// of node-pages-* indexes. A delayed bulk write to a deleted index fails.
func (a *Adapter) CreateReplacement(ctx context.Context, index string, primaries, routing, replicas int) error {
	model, err := a.Provision(ctx)
	if err != nil {
		return engineFailure(ctx, "search.replacement.model_failed", "provision model for replacement "+index, index, err)
	}
	spec := IndexSpec{Model: model, MappingVersion: "1", Primaries: primaries, RoutingShards: routing, Replicas: replicas}
	if err := a.EnsureIndex(ctx, index, spec); err != nil {
		return engineFailure(ctx, "search.replacement.create_failed", "create replacement index "+index, index, err)
	}
	return nil
}

// ValidateSplit reads source and returns its topology when a native split
// to primaries is permitted. The source must use the pinned mapping version
// and tokenizer. primaries must be an approved split target, larger than the
// source primary count, and a multiple of that count. The source routing
// shard count must be a multiple of primaries. The check runs before any
// write block.
func (a *Adapter) ValidateSplit(ctx context.Context, source string, primaries int) (PhysicalSettings, error) {
	info, err := a.IndexInfo(ctx, source)
	if err != nil {
		return PhysicalSettings{}, engineFailure(ctx, "search.split.info_failed", "read mapping of "+source, source, err)
	}
	if info.MappingVersion != "1" || info.TokenizerSHA256 != PinnedModel.TokenizerDigest {
		return PhysicalSettings{}, engineFailure(ctx, "search.split.mapping_changed", "validate split of "+source, source,
			fmt.Errorf("mapping version %q or tokenizer differs from the pinned contract; a full replacement is required", info.MappingVersion))
	}
	settings, err := a.IndexSettings(ctx, source)
	if err != nil {
		return PhysicalSettings{}, err
	}
	approved := slices.Contains(approvedSplitTargets, primaries)
	if !approved || primaries <= settings.Primaries || primaries%settings.Primaries != 0 || settings.RoutingShards%primaries != 0 {
		return PhysicalSettings{}, engineFailure(ctx, "search.split.target_rejected", "validate split of "+source, source,
			fmt.Errorf("split from %d to %d primary shards with %d routing shards is not a permitted split", settings.Primaries, primaries, settings.RoutingShards))
	}
	return settings, nil
}

type splitSettings struct {
	Settings map[string]int `json:"settings"`
}

// SplitIndex splits the write-blocked source into target with primaries
// shards and replicas replica copies through the typed Split Index API.
// OpenSearch gives a split target the cluster default replica count unless
// the request sets one. A target that already has the requested primary
// count satisfies the request.
func (a *Adapter) SplitIndex(ctx context.Context, source, target string, primaries, replicas int) error {
	exists, err := opensearch.Do[json.RawMessage](ctx, a.client, http.MethodHead, opensearchapi.IndicesExistsReq{Indices: []string{target}}, nil)
	if err != nil {
		return engineFailure(ctx, "search.split.exists_failed", "check split target "+target, target, err)
	}
	if exists.StatusCode != http.StatusNotFound {
		settings, settingsErr := a.IndexSettings(ctx, target)
		if settingsErr != nil {
			return settingsErr
		}
		if settings.Primaries != primaries {
			return engineFailure(ctx, "search.split.target_invalid", "verify split target "+target, target,
				fmt.Errorf("target has %d primary shards, want %d", settings.Primaries, primaries))
		}
		return nil
	}
	body, err := json.Marshal(splitSettings{Settings: map[string]int{
		"index.number_of_shards": primaries, "index.number_of_replicas": replicas,
	}})
	if err != nil {
		return engineFailure(ctx, "search.split.encode_failed", "encode split of "+source, source, err)
	}
	response, err := a.api.Indices.Split(ctx, opensearchapi.IndicesSplitReq{Index: source, Target: target, Body: bytes.NewReader(body)})
	if err != nil {
		return engineFailure(ctx, "search.split.failed", "split "+source+" into "+target, source, err)
	}
	if !response.Acknowledged {
		return engineFailure(ctx, "search.split.unacknowledged", "split "+source+" into "+target, source, errNotAcknowledged)
	}
	return nil
}

type writeBlockSettings struct {
	Index writeBlockIndex `json:"index"`
}

type writeBlockIndex struct {
	Blocks writeBlock `json:"blocks"`
}

type writeBlock struct {
	Write bool `json:"write"`
}

// SetWriteBlock sets or clears the write block of index. A blocked index
// rejects every document write and keeps serving reads. SetWriteBlock
// returns [searchdomain.ErrIndexNotFound] when index does not exist.
func (a *Adapter) SetWriteBlock(ctx context.Context, index string, blocked bool) error {
	body, err := json.Marshal(writeBlockSettings{Index: writeBlockIndex{Blocks: writeBlock{Write: blocked}}})
	if err != nil {
		return engineFailure(ctx, "search.block.encode_failed", "encode write block of "+index, index, err)
	}
	response, err := a.api.Indices.Settings.Put(ctx, opensearchapi.SettingsPutReq{Indices: []string{index}, Body: bytes.NewReader(body)})
	if err != nil && response != nil && response.Inspect().Response != nil && response.Inspect().Response.StatusCode == http.StatusNotFound {
		telemetry.L(ctx).InfoContext(ctx, "search.block.index_missing", slog.String("index", index), slog.Bool("blocked", blocked))
		return searchdomain.ErrIndexNotFound
	}
	if err != nil {
		return engineFailure(ctx, "search.block.failed", fmt.Sprintf("set write block %t on %s", blocked, index), index, err)
	}
	if !response.Acknowledged {
		return engineFailure(ctx, "search.block.unacknowledged", fmt.Sprintf("set write block %t on %s", blocked, index), index, errNotAcknowledged)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.block.updated", slog.String("index", index), slog.Bool("blocked", blocked))
	return nil
}
