package search

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/telemetry"
)

// Mapping returns the strict native semantic mapping.
func (s IndexSpec) Mapping(ctx context.Context) (mappingDef, error) {
	properties, err := mappingProperties(ctx, s.Model)
	if err != nil {
		return mappingDef{}, err
	}
	return mappingDef{
		Dynamic: "strict",
		Meta: mappingMeta{
			MappingVersion: s.MappingVersion, ModelID: s.Model.ID, TokenizerSHA256: s.Model.TokenizerDigest,
		},
		Properties: properties,
	}, nil
}

// documentFieldTypes returns the fixed scalar document properties and their
// OpenSearch types.
func documentFieldTypes() map[string]string {
	return map[string]string{
		"node_id": "keyword", "node_type": "keyword", "node_revision": "keyword",
		"projection_version": "keyword", "page_ordinal": "long", "name": "text",
		"retired": "boolean", "search_generation": "long",
	}
}

func mappingProperties(ctx context.Context, model ModelInfo) (map[string]json.RawMessage, error) {
	properties := map[string]json.RawMessage{}
	for key, value := range documentFieldTypes() {
		encoded, err := json.Marshal(fieldDef{Type: value})
		if err != nil {
			return nil, mappingEncodeError(ctx, key, err)
		}
		properties[key] = encoded
	}
	access, err := json.Marshal(accessDef{Dynamic: "strict", Fields: map[string]fieldDef{
		"versions": {Type: "keyword"}, "keys": {Type: "keyword"}, "generation": {Type: "long"},
	}})
	if err != nil {
		return nil, mappingEncodeError(ctx, "access", err)
	}
	properties["access"] = access
	semantic, err := json.Marshal(semanticDef{
		Type: "semantic", RawFieldType: "text", ModelID: model.ID, SemanticInfoFieldName: generatedSemanticField,
		Chunking: []semanticChunking{{
			Algorithm:  "fixed_char_length",
			Parameters: chunkParameters{CharLimit: 160, OverlapRate: 0.5, MaxChunkLimit: -1},
		}},
		SparseEncodingConfig:  sparseEncodingConfig{PruneType: "max_ratio", PruneRatio: 0.1},
		SkipExistingEmbedding: true,
	})
	if err != nil {
		return nil, mappingEncodeError(ctx, "page_text", err)
	}
	properties["page_text"] = semantic
	return properties, nil
}

func mappingEncodeError(ctx context.Context, field string, err error) error {
	wrapped := fmt.Errorf("encode mapping field %s: %w", field, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.mapping.encode_failed", slog.String("err", wrapped.Error()), slog.String("field", field))
	return loggedModelError{err: wrapped}
}
