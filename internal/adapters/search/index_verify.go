package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/telemetry"
)

// generatedSemanticField is the nested chunk field OpenSearch adds for page_text.
const generatedSemanticField = "page_text_semantic_info"

// VerifyIndex compares the stored mapping, including the model ID in its
// metadata, and the shard topology with spec. It does not read the model
// registration; VerifyModel checks that.
func (a *Adapter) VerifyIndex(ctx context.Context, index string, spec IndexSpec) error {
	if err := spec.Validate(ctx); err != nil {
		wrapped := fmt.Errorf("verify OpenSearch index %s: %w", index, err)
		if !isLoggedModelError(err) {
			telemetry.L(ctx).ErrorContext(ctx, "search.index.verify_invalid", slog.String("err", wrapped.Error()), slog.String("index", index))
		}
		return wrapped
	}
	expected, err := spec.Mapping(ctx)
	if err != nil {
		wrapped := fmt.Errorf("build expected OpenSearch index %s mapping: %w", index, err)
		if !isLoggedModelError(err) {
			telemetry.L(ctx).ErrorContext(ctx, "search.index.mapping_encode_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		}
		return wrapped
	}
	response, err := a.api.Indices.Mapping.Get(ctx, &opensearchapi.MappingGetReq{Indices: []string{index}})
	if err != nil {
		wrapped := fmt.Errorf("read OpenSearch index %s mapping: %w", index, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.index.mapping_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	entry, exists := response.GetIndices()[index]
	if !exists {
		wrapped := fmt.Errorf("OpenSearch index %s mapping is absent", index)
		telemetry.L(ctx).ErrorContext(ctx, "search.index.mapping_absent", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	var actual mappingDef
	if err := json.Unmarshal(entry.Mappings, &actual); err != nil {
		wrapped := fmt.Errorf("decode OpenSearch index %s mapping: %w", index, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.index.mapping_invalid", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	if mismatches := compareIndexMapping(actual, expected); len(mismatches) > 0 {
		wrapped := fmt.Errorf("verify OpenSearch index %s mapping: %w", index, errors.Join(mismatches...))
		telemetry.L(ctx).ErrorContext(ctx, "search.index.mapping_mismatch", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	settings, err := a.IndexSettings(ctx, index)
	if err != nil {
		return err
	}
	if mismatches := compareTopology(settings, spec); len(mismatches) > 0 {
		wrapped := fmt.Errorf("OpenSearch index %s shard topology differs: %w", index, errors.Join(mismatches...))
		telemetry.L(ctx).ErrorContext(ctx, "search.index.topology_mismatch", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	return nil
}

// compareTopology returns one error for each shard value that differs.
func compareTopology(actual PhysicalSettings, spec IndexSpec) []error {
	var mismatches []error
	if actual.Primaries != spec.Primaries {
		mismatches = append(mismatches, fmt.Errorf("primary shards are %d, want %d", actual.Primaries, spec.Primaries))
	}
	if actual.RoutingShards != spec.RoutingShards {
		mismatches = append(mismatches, fmt.Errorf("routing shards are %d, want %d", actual.RoutingShards, spec.RoutingShards))
	}
	if actual.Replicas != spec.Replicas {
		mismatches = append(mismatches, fmt.Errorf("replicas are %d, want %d", actual.Replicas, spec.Replicas))
	}
	return mismatches
}

// compareIndexMapping returns one error for each mapping value that differs.
func compareIndexMapping(actual, expected mappingDef) []error {
	var mismatches []error
	if actual.Dynamic != expected.Dynamic {
		mismatches = append(mismatches, fmt.Errorf("mapping dynamic setting is %q, want %q", actual.Dynamic, expected.Dynamic))
	}
	if actual.Meta.MappingVersion != expected.Meta.MappingVersion {
		mismatches = append(mismatches, fmt.Errorf("mapping version is %q, want %q", actual.Meta.MappingVersion, expected.Meta.MappingVersion))
	}
	if actual.Meta.ModelID != expected.Meta.ModelID {
		mismatches = append(mismatches, fmt.Errorf("mapping model ID is %q, want %q", actual.Meta.ModelID, expected.Meta.ModelID))
	}
	if actual.Meta.TokenizerSHA256 != expected.Meta.TokenizerSHA256 {
		mismatches = append(mismatches, fmt.Errorf("mapping tokenizer SHA-256 is %q, want %q", actual.Meta.TokenizerSHA256, expected.Meta.TokenizerSHA256))
	}
	for _, field := range slices.Sorted(maps.Keys(expected.Properties)) {
		actualJSON, exists := actual.Properties[field]
		if !exists {
			mismatches = append(mismatches, fmt.Errorf("mapping field %s is absent", field))
			continue
		}
		for _, mismatch := range compareMappedField(field, actualJSON, expected.Properties[field]) {
			mismatches = append(mismatches, fmt.Errorf("mapping field %s differs: %w", field, mismatch))
		}
	}
	for _, field := range slices.Sorted(maps.Keys(actual.Properties)) {
		if _, declared := expected.Properties[field]; !declared && field != generatedSemanticField {
			mismatches = append(mismatches, fmt.Errorf("mapping field %s is not declared", field))
		}
	}
	return append(mismatches, compareGeneratedField(actual.Properties)...)
}

func compareGeneratedField(properties map[string]json.RawMessage) []error {
	generatedJSON, exists := properties[generatedSemanticField]
	if !exists {
		return []error{fmt.Errorf("generated mapping field %s is absent", generatedSemanticField)}
	}
	// OpenSearch maps the generated field as an object. With chunking, the
	// object stores each chunk text and its embedding in a nested chunks
	// property.
	var generated struct {
		Type       string `json:"type"`
		Properties struct {
			Chunks fieldDef `json:"chunks"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(generatedJSON, &generated); err != nil {
		return []error{fmt.Errorf("decode generated mapping field %s: %w", generatedSemanticField, err)}
	}
	if generated.Type != "" && generated.Type != "object" {
		return []error{fmt.Errorf("generated mapping field %s has type %q, want an object", generatedSemanticField, generated.Type)}
	}
	if generated.Properties.Chunks.Type != "nested" {
		return []error{fmt.Errorf("generated mapping field %s.chunks has type %q, want %q", generatedSemanticField, generated.Properties.Chunks.Type, "nested")}
	}
	return nil
}

func compareMappedField(field string, actualJSON, expectedJSON json.RawMessage) []error {
	if field == "access" {
		return compareAccessField(actualJSON, expectedJSON)
	}
	if field == "page_text" {
		return compareSemanticField(actualJSON, expectedJSON)
	}
	return compareScalarField(actualJSON, expectedJSON)
}
