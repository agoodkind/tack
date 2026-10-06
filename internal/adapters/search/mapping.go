package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/telemetry"
)

// IndexSpec sets the model, mapping version, and shard layout of one physical
// search index. OpenSearch cannot change the model, mapping version, primary
// count, or routing shard count after it creates the index.
type IndexSpec struct {
	Model          ModelInfo
	MappingVersion string
	Primaries      int
	RoutingShards  int
	Replicas       int
}

// IndexInfo is the mapping metadata that the verification command compares.
type IndexInfo struct {
	MappingVersion  string
	ModelID         string
	TokenizerSHA256 string
}

// MappingVersion mismatches require a full index replacement through ops search reindex.
const MappingVersion = "2"

// approvedSplitTargets lists the primary shard counts a native split may
// produce from one primary shard. The reserved routing shard count must be
// divisible by every target.
var approvedSplitTargets = []int{2, 4, 8}

type mappingMeta struct {
	MappingVersion  string `json:"mapping_version"`
	ModelID         string `json:"model_id"`
	TokenizerSHA256 string `json:"tokenizer_sha256"`
}

type fieldDef struct {
	Type string `json:"type"`
}

type accessDef struct {
	Dynamic string              `json:"dynamic"`
	Fields  map[string]fieldDef `json:"properties"`
}

type chunkParameters struct {
	CharLimit     float64 `json:"char_limit"`
	OverlapRate   float64 `json:"overlap_rate"`
	MaxChunkLimit float64 `json:"max_chunk_limit"`
}

type semanticChunking struct {
	Algorithm  string          `json:"algorithm"`
	Parameters chunkParameters `json:"parameters"`
}

type semanticDef struct {
	Type                  string               `json:"type"`
	RawFieldType          string               `json:"raw_field_type"`
	ModelID               string               `json:"model_id"`
	SemanticInfoFieldName string               `json:"semantic_info_field_name"`
	Chunking              []semanticChunking   `json:"chunking"`
	SparseEncodingConfig  sparseEncodingConfig `json:"sparse_encoding_config"`
	SkipExistingEmbedding bool                 `json:"skip_existing_embedding"`
}

type sparseEncodingConfig struct {
	PruneType  string  `json:"prune_type"`
	PruneRatio float64 `json:"prune_ratio"`
}

type mappingDef struct {
	Dynamic    string                     `json:"dynamic"`
	Meta       mappingMeta                `json:"_meta"`
	Properties map[string]json.RawMessage `json:"properties"`
}

type indexBody struct {
	Settings indexSettings `json:"settings"`
	Mappings mappingDef    `json:"mappings"`
}

type indexSettings struct {
	NumberOfShards        int `json:"number_of_shards,omitempty"`
	NumberOfRoutingShards int `json:"number_of_routing_shards,omitempty"`
	NumberOfReplicas      int `json:"number_of_replicas"`
}

type indexSettingsBody struct {
	Index indexSettings `json:"index"`
}

// Validate checks the model identity, mapping version, and shard settings
// before index creation. It returns one joined error that lists every failed
// check.
func (s IndexSpec) Validate(ctx context.Context) error {
	problems := s.problems()
	if len(problems) == 0 {
		return nil
	}
	invalid := errors.Join(problems...)
	telemetry.L(ctx).ErrorContext(ctx, "search.index.spec_invalid", slog.String("err", invalid.Error()),
		slog.String("mapping_version", s.MappingVersion), slog.String("model_id", s.Model.ID))
	return loggedModelError{err: invalid}
}

func (s IndexSpec) problems() []error {
	var problems []error
	if s.Model.ID == "" {
		problems = append(problems, errors.New("search index requires a registered model ID"))
	}
	if s.Model.Name != PinnedModel.Name || s.Model.Version != PinnedModel.Version {
		problems = append(problems, fmt.Errorf("search index model %s version %s is not the pinned model", s.Model.Name, s.Model.Version))
	}
	if s.Model.TokenizerDigest != PinnedModel.TokenizerDigest {
		problems = append(problems, fmt.Errorf("search index tokenizer SHA-256 %q is not the pinned tokenizer", s.Model.TokenizerDigest))
	}
	if s.MappingVersion == "" {
		problems = append(problems, errors.New("search index requires a mapping version"))
	}
	if s.Replicas < 0 {
		problems = append(problems, fmt.Errorf("search index replicas %d must be nonnegative", s.Replicas))
	}
	if s.Primaries <= 0 || s.RoutingShards <= 0 {
		problems = append(problems, fmt.Errorf("search index primary shards %d and routing shards %d must be positive", s.Primaries, s.RoutingShards))
		return problems
	}
	if s.RoutingShards%s.Primaries != 0 {
		problems = append(problems, fmt.Errorf("search index routing shards %d must be divisible by primary shards %d", s.RoutingShards, s.Primaries))
	}
	for _, target := range approvedSplitTargets {
		if s.RoutingShards%target != 0 {
			problems = append(problems, fmt.Errorf("search index routing shards %d must be divisible by approved split target %d", s.RoutingShards, target))
		}
	}
	return problems
}
